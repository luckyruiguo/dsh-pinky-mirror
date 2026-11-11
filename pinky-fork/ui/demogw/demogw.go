// Package demogw runs the real gateway on internal/demo's scripted phones,
// with scripted agents calling it in the same process, for the client's
// demo (pinky-ui-demo -gateway) and its end-to-end tests. Nothing here
// touches a real device.
package demogw

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// Options configure the gateway.
type Options struct {
	// DataDir keeps history and screenshots; empty keeps history in memory,
	// without step screenshots.
	DataDir string
	// ConfigDir keeps the policy's rules.yaml and apps.yaml, as the app's
	// config folder does, to edit them by hand; empty uses the bundled
	// rules without files.
	ConfigDir string
	// Video streams the demo phones' screens through ffmpeg so the mirror
	// shows them; without it the mirror shows an error.
	Video bool
	// TrustNewAgents skips the first-connection approval.
	TrustNewAgents bool
	// ApprovalTimeout and MaxBlock as in gateway.Options; 0 keeps theirs.
	ApprovalTimeout, MaxBlock time.Duration
	Logger                    *slog.Logger
}

// World is the gateway and its phones.
type World struct {
	Gateway *gateway.Gateway
	Pixel   *demo.Device // fake-pixel-9
	IPhone  *demo.Device // fake-iphone-17
}

// New starts the gateway on the demo phones.
func New(opt Options) (*World, error) {
	providers, devices := demo.World()
	for _, d := range devices {
		d.SetVideo(opt.Video)
	}
	pol := policy.Default()
	if opt.ConfigDir != "" {
		var err error
		if pol, err = policy.Load(filepath.Join(opt.ConfigDir, "rules.yaml"), filepath.Join(opt.ConfigDir, "apps.yaml")); err != nil {
			return nil, err
		}
	}
	for cat, e := range demo.PolicyApps() {
		pol.AddApps(cat, e)
	}
	log := opt.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	g, err := gateway.New(gateway.Options{
		Providers: providers, DataDir: opt.DataDir, Policy: pol, Logger: log,
		TrustNewAgents: opt.TrustNewAgents, ApprovalTimeout: opt.ApprovalTimeout, MaxBlock: opt.MaxBlock,
		DiscoverEvery: 500 * time.Millisecond,
	})
	if err != nil {
		return nil, err
	}
	return &World{Gateway: g, Pixel: devices[0], IPhone: devices[1]}, nil
}

// Agent is a scripted agent with its own session, as an MCP client would
// have.
type Agent struct {
	s    *gateway.Session
	dev  string
	Log  func(format string, args ...any)
	last *gateway.Result
}

// NewAgent opens a session for an agent named name on a phone.
func (w *World) NewAgent(name, deviceID string) *Agent {
	return &Agent{s: w.Gateway.OpenSession(gateway.AgentInfo{Name: name, Client: "pinky-ui-demo"}), dev: deviceID,
		Log: func(string, ...any) {}}
}

// Close ends the session.
func (ag *Agent) Close() { ag.s.Close() }

// ErrEnded is returned when the person stopped or declined the task.
var ErrEnded = errors.New("demogw: the task ended")

// pending repeats a call while it waits for the person, as an agent does
// with approval_pending.
func (ag *Agent) pending(ctx context.Context, call func(id string) *gateway.Result) *gateway.Result {
	id := ""
	for {
		res := call(id)
		if res.Status != gateway.StatusPending || ctx.Err() != nil {
			return res
		}
		id = res.ApprovalID
	}
}

func (ag *Agent) check(res *gateway.Result, what string) error {
	if res.Screen != nil {
		ag.last = res
	}
	if res.Error != "" {
		return fmt.Errorf("%s: %s: %s", what, res.Error, res.Message)
	}
	switch res.Status {
	case gateway.StatusStopped, gateway.StatusDenied:
		ag.Log("%s: %s (%s)", what, res.Status, res.Message)
		return ErrEnded
	}
	return nil
}

// Connect starts a task, waiting for the person to allow a first
// connection.
func (ag *Agent) Connect(ctx context.Context, task string) error {
	res := ag.pending(ctx, func(id string) *gateway.Result {
		return ag.s.Connect(ctx, gateway.ConnectRequest{Call: gateway.Call{Device: ag.dev, ApprovalID: id}, Task: task})
	})
	return ag.check(res, "connect")
}

// Act runs one action, waiting for approvals and for the person to hand
// the phone back when they took over.
func (ag *Agent) Act(ctx context.Context, intent string, a gateway.Action) error {
	for tries := 0; ; tries++ {
		res := ag.pending(ctx, func(id string) *gateway.Result {
			return ag.s.Act(ctx, gateway.Call{Device: ag.dev, Intent: intent, ApprovalID: id}, a)
		})
		if res.Status == gateway.StatusTookOver || res.Status == gateway.StatusPaused {
			if tries > 3 {
				return ErrEnded
			}
			if err := ag.WaitForPerson(ctx, "Hand the phone back when you're done"); err != nil {
				return err
			}
			return errReplan // the screen may have changed: look again
		}
		return ag.check(res, a.Tool)
	}
}

var errReplan = errors.New("demogw: look at the screen again")

// WaitForPerson calls phone_handoff and waits until the person hands the
// phone back.
func (ag *Agent) WaitForPerson(ctx context.Context, reason string) error {
	res := ag.pending(ctx, func(id string) *gateway.Result {
		return ag.s.Handoff(ctx, gateway.HandoffRequest{Call: gateway.Call{Device: ag.dev, ApprovalID: id}, Reason: reason})
	})
	return ag.check(res, "handoff")
}

// Ask asks the person a question with phone_request_approval and waits for
// the answer; a no is ErrEnded.
func (ag *Agent) Ask(ctx context.Context, summary string) error {
	res := ag.pending(ctx, func(id string) *gateway.Result {
		return ag.s.RequestApproval(ctx, gateway.ApprovalRequest{Call: gateway.Call{Device: ag.dev, ApprovalID: id}, Summary: summary})
	})
	return ag.check(res, "request approval")
}

// Observe reads the screen.
func (ag *Agent) Observe(ctx context.Context) error {
	res := ag.s.Observe(ctx, gateway.ObserveRequest{Call: gateway.Call{Device: ag.dev}})
	return ag.check(res, "observe")
}

// Release ends the task.
func (ag *Agent) Release(ctx context.Context, outcome, summary string) {
	ag.s.Release(ctx, gateway.ReleaseRequest{Call: gateway.Call{Device: ag.dev}, Outcome: outcome, Summary: summary})
}

// find returns the ref of the element whose label starts with label.
func (ag *Agent) find(label string) (screen.Element, bool) {
	if ag.last == nil || ag.last.Screen == nil {
		return screen.Element{}, false
	}
	for _, e := range ag.last.Screen.Elements {
		if strings.HasPrefix(e.Label, label) {
			return e, true
		}
	}
	return screen.Element{}, false
}

func (ag *Agent) tap(ctx context.Context, label, intent string) error {
	e, ok := ag.find(label)
	if !ok {
		return fmt.Errorf("demogw: %q is not on screen", label)
	}
	return ag.Act(ctx, intent, gateway.Action{Tool: "tap", Ref: e.Ref})
}

// Pace is the pause between an agent's steps, so a person can follow.
var Pace = 1200 * time.Millisecond

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// ReorderLunch is the prototype's main flow on the demo phone: open
// Dinely, reorder last week's chicken rice bowl, add a note and pay, which
// the payment rule asks the person about. It reads the screen before each
// step, so it goes on from wherever the person left the phone.
func (ag *Agent) ReorderLunch(ctx context.Context) error {
	if err := ag.Connect(ctx, "Reorder last week's lunch"); err != nil {
		return err
	}
	noted := false
	for step := 0; step < 30; step++ {
		if err := sleep(ctx, Pace); err != nil {
			return err
		}
		if err := ag.Observe(ctx); err != nil {
			return err
		}
		var err error
		_, onHome := ag.find("Back to home")
		_, placed := ag.find("Order placed")
		switch {
		case placed && onHome:
			ag.Release(ctx, "done", "Reordered last week's chicken rice bowl for $12.80, with the note “No cilantro”.")
			return nil
		case has(ag, "Place order") && !noted:
			e, _ := ag.find("Note for the restaurant")
			err = ag.Act(ctx, "Add the note No cilantro", gateway.Action{Tool: "type", Ref: e.Ref, Text: "No cilantro"})
			if err == nil {
				noted = true
			}
		case has(ag, "Place order"):
			err = ag.tap(ctx, "Place order", "Pay $12.80 for 1 chicken rice bowl")
		case has(ag, "Cart · "):
			err = ag.tap(ctx, "Cart · ", "Open the cart")
		case has(ag, "Reorder · Chicken rice bowl"):
			err = ag.tap(ctx, "Reorder · Chicken rice bowl", "Reorder the chicken rice bowl")
		case has(ag, "Orders"):
			err = ag.tap(ctx, "Orders", "Find last week's order")
		default:
			err = ag.Act(ctx, "Open Dinely", gateway.Action{Tool: "launch", App: "Dinely"})
		}
		switch {
		case errors.Is(err, errReplan):
		case errors.Is(err, ErrEnded):
			ag.Release(ctx, "cancelled", "The person said no.")
			return err
		case err != nil:
			return err
		}
	}
	return errors.New("demogw: the lunch order took too many steps")
}

// SignIn opens Dinely's account screen and asks the person to sign in, as
// phone_handoff does, then goes on once they hand the phone back.
func (ag *Agent) SignIn(ctx context.Context) error {
	if err := ag.Connect(ctx, "Check Dinely's saved address"); err != nil {
		return err
	}
	if err := ag.Act(ctx, "Open Dinely", gateway.Action{Tool: "launch", App: "Dinely"}); err != nil && !errors.Is(err, errReplan) {
		return err
	}
	_ = sleep(ctx, Pace)
	if err := ag.Observe(ctx); err != nil {
		return err
	}
	if has(ag, "Account") {
		if err := ag.tap(ctx, "Account", "Open the account screen"); err != nil && !errors.Is(err, errReplan) {
			return err
		}
	}
	_ = sleep(ctx, Pace)
	if err := ag.WaitForPerson(ctx, "Sign in to Dinely, please"); err != nil {
		if errors.Is(err, ErrEnded) {
			ag.Release(ctx, "cancelled", "The person declined to sign in.")
		}
		return err
	}
	_ = sleep(ctx, Pace)
	if err := ag.Observe(ctx); err != nil {
		return err
	}
	ag.Release(ctx, "done", "You signed in; the saved address is 12 Market St.")
	return nil
}

func has(ag *Agent, label string) bool {
	_, ok := ag.find(label)
	return ok
}

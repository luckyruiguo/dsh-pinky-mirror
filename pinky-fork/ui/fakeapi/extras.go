package fakeapi

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// The gateway's methods beyond gateway.API, which the client looks for:
// step screenshots, agents and their scopes, read-only mode and deleting
// history. The signatures are internal/gateway's.

// agent is one agent the fake knows.
type agent struct {
	name     string
	approved bool
	scope    gateway.Scope
}

func defaultAgents() map[string]*agent {
	return map[string]*agent{
		agentName: {name: agentName, approved: true,
			scope: gateway.Scope{Mode: gateway.ScopeApps, Apps: []string{"Dinely", "Chatter", "Calendar", "Tally"}}},
	}
}

// mediaID names a device's screenshot. The fake has one still per
// device, its test source, so every step on it shows the same picture.
func mediaID(deviceID string) string { return "shot-" + deviceID }

// MediaPath returns the file of a step screenshot, writing it the first
// time.
func (a *API) MediaPath(ctx context.Context, id string) (string, error) {
	devID, ok := strings.CutPrefix(id, "shot-")
	if !ok || devID == "" {
		return "", fmt.Errorf("fakeapi: no media %q", id)
	}
	a.mu.Lock()
	if p, ok := a.mediaFiles[id]; ok {
		a.mu.Unlock()
		return p, nil
	}
	d := a.device(devID)
	if d == nil {
		// A phone that is gone, from the seeded history: its size only.
		w, h := 1080, 2400
		if devID == deviceID("iPhone 17") {
			w, h = 1206, 2622
		}
		d = &device{info: driver.Info{ID: devID}, width: w, height: h, source: "testsrc2"}
	}
	if a.mediaDir == "" {
		dir, err := os.MkdirTemp("", "pinky-fake-media-*")
		if err != nil {
			a.mu.Unlock()
			return "", err
		}
		a.mediaDir = dir
	}
	dir := a.mediaDir
	a.mu.Unlock()

	img := a.screenshot(d)
	// Halved, as the gateway scales its screenshots down by a whole factor.
	small := halve(img)
	path := filepath.Join(dir, id+".png")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	if err := png.Encode(f, small); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	a.mu.Lock()
	a.mediaFiles[id] = path
	a.mu.Unlock()
	return path, nil
}

// halve scales a picture down by two, averaging each 2×2 block.
func halve(src image.Image) image.Image {
	b := src.Bounds()
	w, h := b.Dx()/2, b.Dy()/2
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, bl uint32
			for _, o := range [4][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
				cr, cg, cb, _ := src.At(b.Min.X+2*x+o[0], b.Min.Y+2*y+o[1]).RGBA()
				r, g, bl = r+cr, g+cg, bl+cb
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = uint8(r/4>>8), uint8(g/4>>8), uint8(bl/4>>8), 255
		}
	}
	return dst
}

// Agents lists the agents that connected.
func (a *API) Agents(ctx context.Context) ([]gateway.AgentView, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]gateway.AgentView, 0, len(a.agents))
	for _, ag := range a.agents {
		out = append(out, gateway.AgentView{Name: ag.name, Approved: ag.approved, Scope: cloneScope(ag.scope)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// SetAgentScope changes what an agent may do and approves it, as the
// gateway's does.
func (a *API) SetAgentScope(name string, scope gateway.Scope) {
	a.mu.Lock()
	ag := a.agents[name]
	if ag == nil {
		ag = &agent{name: name}
		a.agents[name] = ag
	}
	ag.approved, ag.scope = true, cloneScope(scope)
	a.mu.Unlock()
}

func cloneScope(s gateway.Scope) gateway.Scope {
	s.Apps = append([]string(nil), s.Apps...)
	return s
}

// SetReadOnly turns read-only mode on or off, for one phone or ("") all.
// The fake's agents do not change what they do; ReadOnly reports it.
func (a *API) SetReadOnly(deviceID string, on bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.readOnly == nil {
		a.readOnly = map[string]bool{}
	}
	a.readOnly[deviceID] = on
	return nil
}

// ReadOnly reports what SetReadOnly set for a phone, or ("") all.
func (a *API) ReadOnly(deviceID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.readOnly[deviceID]
}

// WipeHistory deletes the finished tasks.
func (a *API) WipeHistory(ctx context.Context) error {
	a.mu.Lock()
	a.history = nil
	for _, t := range a.tasks {
		if isFinished(t.view.State) {
			t.removed = true
		}
	}
	a.mu.Unlock()
	a.emit(gateway.Event{Kind: gateway.EventTasks})
	return nil
}

// Exemptions lists the Always allow answers, as the gateway's does.
func (a *API) Exemptions() []gateway.Exemption {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]gateway.Exemption, len(a.always))
	for i, x := range a.always {
		x.With = append([]string(nil), x.With...)
		out[i] = x
	}
	return out
}

// RemoveExemption takes an Always allow answer back.
func (a *API) RemoveExemption(x gateway.Exemption) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	kept := a.always[:0:0]
	for _, y := range a.always {
		if y.Rule != x.Rule || y.AppID != x.AppID || y.AppName != x.AppName {
			kept = append(kept, y)
		}
	}
	a.always = kept
	return nil
}

// AlwaysAllow keeps an Always allow answer, as answering an approval with
// it does; the fake's agents ask all the same.
func (a *API) AlwaysAllow(x gateway.Exemption) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.keepAlwaysLocked(x)
}

func (a *API) keepAlwaysLocked(x gateway.Exemption) {
	for _, y := range a.always {
		if y.Rule == x.Rule && y.AppID == x.AppID && y.AppName == x.AppName {
			return
		}
	}
	a.always = append(a.always, x)
}

// fakeBuiltIn are the rules the default preset (payments only) and the
// blocked apps add, as the gateway lists them.
var fakeBuiltIn = []gateway.RuleView{
	{Name: "Payments", Actions: []string{"payment"}, Decision: "ask"},
	{Name: "Blocked by you", Categories: []string{"blocked"}, Decision: "deny"},
}

// Rules returns the rules, starting from the bundled defaults' two.
func (a *API) Rules() gateway.RulesView {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.rulesSet {
		a.rules = []gateway.RuleView{
			{Name: "Finance apps", Categories: []string{"finance"}, Decision: "deny"},
			{Name: "Password managers", Categories: []string{"passwords"}, Decision: "deny"},
		}
		a.rulesSet = true
	}
	return gateway.RulesView{File: cloneRules(a.rules), BuiltIn: cloneRules(fakeBuiltIn)}
}

// SetRules replaces the rules, refusing a rule without a name or a match,
// or a name taken, as the gateway does, and a change made from rules that
// are not the fake's (gateway.ErrRulesChanged); the fake's agents ask all
// the same.
func (a *API) SetRules(base, rules []gateway.RuleView) error {
	if cur := a.Rules().File; !slices.EqualFunc(base, cur, sameRule) {
		return gateway.ErrRulesChanged
	}
	seen := map[string]bool{}
	for _, r := range append(cloneRules(fakeBuiltIn), rules...) {
		k := strings.ToLower(strings.TrimSpace(r.Name))
		switch {
		case k == "":
			return errors.New("fakeapi: a rule has no name")
		case seen[k]:
			return fmt.Errorf("fakeapi: two rules are called %q", r.Name)
		case len(r.Apps)+len(r.Categories)+len(r.Actions)+len(r.Labels) == 0:
			return fmt.Errorf("fakeapi: rule %q matches everything", r.Name)
		}
		seen[k] = true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rules, a.rulesSet = cloneRules(rules), true
	return nil
}

func sameRule(x, y gateway.RuleView) bool {
	return x.Name == y.Name && x.Decision == y.Decision && slices.Equal(x.Apps, y.Apps) &&
		slices.Equal(x.Categories, y.Categories) && slices.Equal(x.Actions, y.Actions) && slices.Equal(x.Labels, y.Labels)
}

// FileProblems lists the rules files the fake was told cannot be used.
func (a *API) FileProblems() []gateway.FileProblem {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]gateway.FileProblem(nil), a.fileProblems...)
}

// SetFileProblems makes FileProblems list these, for tests and the demo:
// rules files edited by hand that cannot be used.
func (a *API) SetFileProblems(list []gateway.FileProblem) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fileProblems = append([]gateway.FileProblem(nil), list...)
}

func cloneRules(rules []gateway.RuleView) []gateway.RuleView {
	out := make([]gateway.RuleView, len(rules))
	for i, r := range rules {
		r.Apps = append([]string(nil), r.Apps...)
		r.Categories = append([]string(nil), r.Categories...)
		r.Actions = append([]string(nil), r.Actions...)
		r.Labels = append([]string(nil), r.Labels...)
		out[i] = r
	}
	return out
}

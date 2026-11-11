package gateway

import (
	"context"
	"errors"
	"fmt"
	"image"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/DIYgod/pinky/internal/approval"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/history"
	"github.com/DIYgod/pinky/internal/screen"
)

// This file is the gateway's agent-facing side. The MCP server opens one
// Session per MCP session and calls these methods; nothing here can approve,
// stop or take over, which only the client API can do.

// AgentInfo identifies the agent behind a session.
type AgentInfo struct {
	Name   string // display name, such as "Claude Code"
	Client string // the MCP client's own name and version
	// Resume lets a newer session of this agent continue what its older
	// sessions left: the leases they do not use at the moment, their open
	// approvals, and a Stop (resume.go). Agents over HTTP, whose clients
	// may open a session for every call.
	Resume bool
	// Reconnect lets a newer session of this agent continue a lease its
	// older session left when it closed, or that phone_connect names by
	// its task (resume.go). Agents on this computer, whose client may drop
	// a session and start a new one.
	Reconnect bool
	// Trusted marks Pinky's own agent, which the person starts with a task
	// in Pinky's window: it needs no New agent approval, as the person just
	// asked for it. Its scope and everything else work as any agent's.
	Trusted bool
}

// Status is the outcome of a call, from DESIGN.md's Results table.
type Status string

// Statuses.
const (
	StatusSettled   Status = "settled"
	StatusUncertain Status = "uncertain"
	StatusBlocked   Status = "blocked_by_policy"
	StatusDenied    Status = "approval_denied"
	StatusPending   Status = "approval_pending"
	StatusStale     Status = "stale"
	StatusTookOver  Status = "user_took_over"
	StatusPaused    Status = "paused"
	StatusStopped   Status = "stopped"
	StatusLocked    Status = "device_locked"
	// StatusNeedsSetup: the phone is connected, but Pinky cannot use it
	// until the person does a step on it or on the computer (trust this
	// computer, allow USB debugging, Developer Mode, …); Result.Setup names
	// the step and the message says what to do. The lease waits with it.
	StatusNeedsSetup Status = "device_needs_setup"
)

// ErrorCode is an error a call returns instead of a status.
type ErrorCode string

// Error codes. The first five are DESIGN.md's; invalid_argument covers bad
// arguments and misused approval ids, internal covers gateway failures.
const (
	ErrDeviceOffline ErrorCode = "device_offline"
	// ErrDevicePaused: the person paused Pinky's connection to the phone
	// in Pinky (pause.go); it is unusable until they resume it.
	ErrDevicePaused    ErrorCode = "device_paused"
	ErrLeaseHeld       ErrorCode = "lease_held"
	ErrScopeDenied     ErrorCode = "scope_denied"
	ErrNotFound        ErrorCode = "not_found"
	ErrUnsupported     ErrorCode = "unsupported"
	ErrInvalidArgument ErrorCode = "invalid_argument"
	ErrInternal        ErrorCode = "internal"
)

// Result is what an agent call returns. Either Error or Status is set.
type Result struct {
	Status     Status
	Error      ErrorCode
	Message    string // one or two plain sentences for the agent
	Device     string
	DeviceName string
	// Screen is the screen after the call as the agent may see it: blocked
	// apps are hidden and events are filtered.
	Screen *screen.Screen
	// Diff holds the lines that changed since the agent's last screen; when
	// Full is set, send Screen whole instead.
	Diff       []string
	Full       bool
	Settle     string // "timeout" when settling hit its limit
	Early      bool   // settle_ms ended the wait while the screen changed: it may not show the action's effect yet
	ApprovalID string
	Rule       string
	Note       string
	Decision   string // phone_request_approval: "allow" or "deny"
	Holder     string // lease_held: who holds the device
	Setup      string // device_needs_setup: the step the phone waits for (driver.Info.Setup), "" when its driver does not say
	Matched    *bool  // phone_wait_for
	Apps       []driver.App
	Screenshot image.Image
	Steps      []*Result // phone_batch, one per step that ran
	Task       string    // phone_connect: the task id
	Folder     string    // phone_files: the folder on the computer
	Files      []FileView
	Saved      []SavedFile  // files written for the agent on Pinky's computer (save.go)
	Log        string       // phone_logs: the log lines or crash reports, as text
	Switches   []SwitchView // phone_device
	About      *PhoneAbout  // phone_device's read: the phone's model, system, battery and storage
}

// IsError reports whether the result is an error rather than a status.
func (r *Result) IsError() bool { return r.Error != "" }

func errResult(code ErrorCode, format string, args ...any) *Result {
	return &Result{Error: code, Message: fmt.Sprintf(format, args...)}
}

func statusResult(st Status, format string, args ...any) *Result {
	return &Result{Status: st, Message: fmt.Sprintf(format, args...)}
}

// Progress reports progress of a blocking call: done and total in seconds.
type Progress func(done, total float64, message string)

// Call holds the arguments every action tool takes.
type Call struct {
	Device     string
	Intent     string
	ApprovalID string
	Progress   Progress

	// reuse lets a batch step start from the previous step's read.
	reuse bool
}

// reuseAge is how old the previous step's read may be for the next batch
// step to start from it instead of reading the screen again.
const reuseAge = time.Second

// ConnectRequest is phone_connect.
type ConnectRequest struct {
	Call
	Task string
	// Apps is the task's app allow-list, ids or names. Launching any other
	// app is refused and typing into one asks the person.
	Apps []string
	// ReadOnly runs the task read-only, whatever the agent's scope: the
	// agent can ask for less than the person gave it, never more.
	ReadOnly bool
	// OwnScreen runs the task on the phone's own screen even when the
	// person has agents work on a background display, which has no home
	// screen: for work on the home screen itself (useOwnScreen).
	OwnScreen bool
	// Connection asks for a way to connect the phone for this task, as
	// TaskConnection reads it: "helper" (the iPhone's WebDriverAgent
	// helper, which shares no screen), "coredevice", or "auto"/"" for
	// Pinky's choice (taskconn.go).
	Connection string
}

// ReleaseRequest is phone_release.
type ReleaseRequest struct {
	Call
	Outcome string // done, failed, partial or cancelled
	Summary string
	// Export saves the task's report in the phone's folder on Pinky's
	// computer once it ends (save.go).
	Export bool
}

// ObserveRequest is phone_observe.
type ObserveRequest struct {
	Call
	WaitMS     int
	Screenshot bool
	// OCR asks for all the text the screen's pixels show that no element
	// does, however complete the tree looks (ocrAll).
	OCR bool
	// Save saves the screenshot under this name in the phone's folder on
	// Pinky's computer (save.go), with or without Screenshot.
	Save string
}

// WaitForRequest is phone_wait_for.
type WaitForRequest struct {
	Call
	Text string
	// Any lists more texts to wait for, for when several things may come
	// next: the wait ends when Text or the first of them shows, or with
	// Gone, when none of them shows any more.
	Any  []string
	Gone bool
	// Area, when set, is the part of the screen whose text counts: an
	// element counts when its centre, the @x,y on its line, lies inside.
	// Events have no place on the screen and do not count then.
	Area      *screen.Rect
	TimeoutMS int
}

// AppsRequest is phone_apps.
type AppsRequest struct {
	Call
	Query string
}

// ApprovalRequest is phone_request_approval.
type ApprovalRequest struct {
	Call
	Summary string
	Details string
}

// HandoffRequest is phone_handoff.
type HandoffRequest struct {
	Call
	Reason string
}

// BatchRequest is phone_batch.
type BatchRequest struct {
	Call
	Steps []Action
}

// Limits from DESIGN.md's tool table.
const (
	MaxTextLen     = 16384
	MaxObserveWait = 30 * time.Second
	MaxWaitFor     = 45 * time.Second
	// MaxWaitTexts is the most texts one phone_wait_for takes (text and any).
	MaxWaitTexts  = 10
	MaxBatchSteps = 50
	// MaxSettle is the most an action's settle_ms may ask for: the
	// default limit, SettleMax, is never raised.
	MaxSettle = 3 * time.Second
)

// waitForOCREvery is how often phone_wait_for also reads the screen with
// OCR, which is slow on phones that need a screenshot for it.
const waitForOCREvery = time.Second

// Session is one agent connection. It may hold several devices.
type Session struct {
	g       *Gateway
	id      string
	agent   AgentInfo
	started time.Time
	seq     uint64 // the order sessions opened in

	// Guarded by Gateway.mu.
	leases    map[string]*lease // by device id
	barred    map[string]bool   // devices the person stopped this session on
	pausedOn  map[string]bool   // devices whose lease ended as the person paused them (pause.go)
	used      []string          // every device it leased
	closed    bool
	stopped   bool     // the person pressed Stop all
	successor *Session // the newer session that continued its last lease (resume.go)
	// shared is set once the session showed two tasks at once: several
	// conversations of its client may use it, so its calls must name their
	// phone (shared.go).
	shared bool
}

// OpenSession starts a session for an agent connection.
func (g *Gateway) OpenSession(a AgentInfo) *Session {
	if strings.TrimSpace(a.Name) == "" {
		a.Name = "Agent"
	}
	s := &Session{g: g, id: history.NewID("s_"), agent: a, started: time.Now(),
		leases: map[string]*lease{}, barred: map[string]bool{}}
	g.mu.Lock()
	g.sessionSeq++
	s.seq = g.sessionSeq
	g.sessions[s.id] = s
	g.mu.Unlock()
	g.recordSession(s)
	return s
}

// ID returns the session id.
func (s *Session) ID() string { return s.id }

// Agent returns who the session belongs to.
func (s *Session) Agent() AgentInfo { return s.agent }

// Close ends the session: its leases end and its open approvals are
// withdrawn without acting.
// For an agent whose sessions resume, its leases wait a while for its
// next session instead (resume.go).
func (s *Session) Close() { s.end(OutcomeDisconnected, s.agent.Resume || s.agent.Reconnect) }

// CloseNow ends the session like Close, and its leases at once.
func (s *Session) CloseNow() { s.end(OutcomeDisconnected, false) }

func (s *Session) end(outcome string, keep bool) {
	g := s.g
	g.mu.Lock()
	if s.closed {
		g.mu.Unlock()
		return
	}
	s.closed = true
	delete(g.sessions, s.id)
	kept := map[string]bool{}
	for _, l := range s.leases {
		if keep {
			kept[l.id] = true
			g.keepLocked(l)
			continue
		}
		g.endLeaseLocked(l, outcome, "")
	}
	if len(kept) > 0 {
		g.noteNewerSharedLocked(s)
	}
	g.mu.Unlock()
	gone := g.approvals.WithdrawWhere(func(i approval.Info) bool { return i.Binding.Session == s.id && !kept[i.Binding.Lease] }, "the agent disconnected")
	g.closePendingSteps(gone, "withdrawn")
	g.recordSession(s)
}

// acquire finds the lease a call acts on and marks it busy.
func (s *Session) acquire(ref string) (*lease, *Result) {
	g := s.g
	g.mu.Lock()
	defer g.mu.Unlock()
	if s.closed {
		return nil, errResult(ErrInternal, "This session is closed.")
	}
	var l *lease
	if ref != "" {
		d := g.findDeviceLocked(ref)
		if d == nil {
			return nil, errResult(ErrInvalidArgument, "No phone matches %q. Connected phones: %s.", ref, g.deviceListLocked())
		}
		if g.barredLocked(s, d) {
			return nil, statusResult(StatusStopped, "The person stopped the task on %s and its lease is gone. End the task.", d.name())
		}
		if g.paused[d.id] {
			return nil, pausedResult(d.name())
		}
		if l = s.leases[d.id]; l == nil {
			if res := g.busyElsewhereLocked(s, d, ""); res != nil {
				return nil, res
			}
			if l = g.adoptLocked(s, d, ""); l == nil {
				return nil, errResult(ErrInvalidArgument, "This session is not connected to %s; call phone_connect with device %q first.", d.name(), d.id)
			}
		}
	} else {
		if g.noteSharedLocked(s) {
			// The session's one phone may run another conversation's task.
			return nil, g.sharedResultLocked(s)
		}
		ds := g.adoptableLocked(s, "")
		if len(s.leases) == 0 && len(ds) == 1 {
			// A newer session of an agent that resumes: the one phone its
			// older sessions left.
			g.adoptLocked(s, ds[0], "")
		}
		switch len(s.leases) {
		case 0:
			if g.stoppedLocked(s) {
				return nil, statusResult(StatusStopped, "The person pressed Stop and the lease is gone. End the task.")
			}
			if d := g.pausedLeaseLocked(s); d != nil {
				return nil, pausedResult(d.name())
			}
			if len(ds) > 1 {
				var names []string
				for _, d := range ds {
					names = append(names, fmt.Sprintf("%s (device %q, task “%s”)", d.name(), d.id, d.lease.task.title))
				}
				return nil, errResult(ErrInvalidArgument, "Earlier sessions of %s hold %d phones: %s. Pass device.", s.agent.Name, len(names), strings.Join(names, ", "))
			}
			return nil, errResult(ErrInvalidArgument, "No phone is connected in this session; call phone_connect first.")
		case 1:
			for _, x := range s.leases {
				l = x
			}
		default:
			var names []string
			for _, x := range s.leases {
				names = append(names, fmt.Sprintf("%s (%s)", x.dev.name(), x.dev.id))
			}
			sort.Strings(names)
			return nil, errResult(ErrInvalidArgument, "This session holds %d phones: %s. Pass device.", len(names), strings.Join(names, ", "))
		}
	}
	l.inflight++
	l.lastUsed = time.Now()
	return l, nil
}

func (s *Session) done(l *lease) {
	s.g.mu.Lock()
	l.inflight--
	l.lastUsed = time.Now()
	s.g.mu.Unlock()
}

// finish turns any result of a lease the person stopped meanwhile into
// stopped, and fills in the device.
func (g *Gateway) finish(l *lease, r *Result) *Result {
	g.mu.Lock()
	ended, by := l.ended, l.endedBy
	name := l.dev.name()
	note := l.bgNote
	l.bgNote = ""
	g.mu.Unlock()
	if note != "" {
		r.Message = joinNote(r.Message, note)
	}
	if ended && by == OutcomePaused {
		ran := r.Status == StatusSettled || r.Status == StatusUncertain
		r = pausedResult(name)
		if ran {
			r.Message += " The last action may have happened before the pause."
		}
	}
	if ended && by == OutcomeStopped {
		ran := r.Status == StatusSettled || r.Status == StatusUncertain
		r = statusResult(StatusStopped, "The person pressed Stop; the lease is gone. End the task.")
		if ran {
			r.Message += " The last action may have happened before the stop."
		}
	}
	if r.Device == "" {
		r.Device, r.DeviceName = l.dev.id, name
	}
	return r
}

func (g *Gateway) findDeviceLocked(ref string) *device {
	if d, ok := g.devices[ref]; ok && !foundLocked(d) {
		return d
	}
	var match []*device
	for _, d := range g.devices {
		if equalFold(d.name(), ref) && !foundLocked(d) {
			match = append(match, d)
		}
	}
	if len(match) == 0 && len(ref) >= 4 {
		for _, d := range g.devices {
			if hasPrefixFold(d.id, ref) && !foundLocked(d) {
				match = append(match, d)
			}
		}
	}
	if len(match) == 1 {
		return match[0]
	}
	return nil
}

func (g *Gateway) deviceListLocked() string {
	var out []string
	for _, d := range g.sortedDevicesLocked() {
		if foundLocked(d) {
			continue // found on Wi-Fi, and maybe not the person's (wifi.go)
		}
		state := "online"
		switch {
		case g.paused[d.id]:
			state = "paused by the person"
		case d.online:
		case waitsForStep(d.info.State):
			state = "connected, needs setup"
		default:
			state = "offline"
		}
		out = append(out, fmt.Sprintf("%s (device %q, %s)", d.name(), d.id, state))
	}
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, ", ")
}

// Connect implements phone_connect: it leases a device for a task and
// returns the first screen.
func (s *Session) Connect(ctx context.Context, req ConnectRequest) *Result {
	g := s.g
	ctx = g.withBudget(ctx) // the agent's approval and the first read share it
	title := strings.TrimSpace(req.Task)
	if title == "" {
		return errResult(ErrInvalidArgument, "task is required: one line naming the task, shown to the person.")
	}
	if r := []rune(title); len(r) > 200 {
		title = string(r[:200])
	}
	way, ok := TaskConnection(req.Connection)
	if !ok {
		return errResult(ErrInvalidArgument, "connection must be auto, helper or coredevice, not %q.", req.Connection)
	}
	if res := s.approveAgent(ctx, req.Call, title, req.ReadOnly); res != nil {
		return res
	}

	g.mu.Lock()
	if s.closed {
		g.mu.Unlock()
		return errResult(ErrInternal, "This session is closed.")
	}
	var d *device
	if req.Device != "" {
		if d = g.findDeviceLocked(req.Device); d == nil {
			list := g.deviceListLocked()
			g.mu.Unlock()
			return errResult(ErrInvalidArgument, "No phone matches %q. Connected phones: %s.", req.Device, list)
		}
	} else if d = g.taskPhoneLocked(s, title); d == nil {
		// No phone runs this task: the one that is online, or with none,
		// the one that waits for a step (connecting to it says which
		// step). The session's phone for another task is no default, as
		// that task may be another conversation's (shared.go).
		var online, waiting, paused []*device
		for _, x := range g.devices {
			switch {
			case g.paused[x.id]:
				paused = append(paused, x)
			case x.online:
				online = append(online, x)
			case foundLocked(x):
				// Found on Wi-Fi, and maybe not the person's (wifi.go).
			case waitsForStep(x.info.State):
				waiting = append(waiting, x)
			}
		}
		if len(online) == 0 {
			online = waiting
		}
		if len(online) == 0 && len(paused) == 1 {
			// The only phone is paused: connecting to it says so.
			online = paused
		}
		switch len(online) {
		case 0:
			g.mu.Unlock()
			return errResult(ErrDeviceOffline, "No phone is connected to Pinky. Ask the person to connect one.")
		case 1:
			d = online[0]
		default:
			list := g.deviceListLocked()
			note := g.otherTasksNoteLocked(s, title)
			g.mu.Unlock()
			return errResult(ErrInvalidArgument, "%s", joinNote(fmt.Sprintf("Several phones are connected: %s. Pass device.", list), note))
		}
	}
	if g.paused[d.id] {
		g.mu.Unlock()
		return pausedResult(d.name())
	}
	if s.barred[d.id] || s.stopped {
		g.mu.Unlock()
		return statusResult(StatusStopped, "The person stopped this agent's task on %s; this session may not use it again. End the task.", d.name())
	}
	if g.barredLocked(s, d) {
		g.mu.Unlock()
		return statusResult(StatusStopped, "The person stopped %s's task on %s, so it may not use the phone until %s after the Stop. End the task.",
			s.agent.Name, d.name(), approval.Human(stopHold))
	}
	if !d.online && !waitsForStep(d.info.State) {
		g.mu.Unlock()
		return errResult(ErrDeviceOffline, "%s is offline.", d.name())
	}
	if res := g.busyElsewhereLocked(s, d, title); res != nil {
		g.mu.Unlock()
		return res
	}
	g.adoptLocked(s, d, title)
	if l := d.lease; s.runsOtherTask(l, title) {
		// Never end another task of this session, or of an earlier one of
		// its agent, for this one: it may be another conversation's.
		res := g.otherTaskResultLocked(s, d, l)
		g.mu.Unlock()
		return res
	}
	if l := d.lease; l != nil && l.session != s && l.session.closed && !l.session.agent.Resume && l.inflight == 0 {
		// Kept for a local agent's next session (resume.go), which has
		// not come: it does not keep the phone from anyone else.
		g.endLeaseLocked(l, OutcomeDisconnected, "")
	}
	if l := d.lease; l != nil && l.session != s {
		holder := fmt.Sprintf("%s · %s", l.session.agent.Name, l.task.title)
		g.mu.Unlock()
		res := errResult(ErrLeaseHeld, "%s is in use by %s.", d.name(), holder)
		res.Holder = holder
		return res
	}
	if l := d.lease; l != nil && l.task.title == title {
		// The same task again: keep the lease, refresh the app list. A
		// task that asked for read-only stays so.
		if len(req.Apps) > 0 {
			l.apps = append([]string(nil), req.Apps...)
		}
		if req.ReadOnly {
			l.readOnly, l.task.readOnly = true, true
		}
		l.inflight++
		ro := l.readOnly
		shared := s.shared
		g.mu.Unlock()
		defer s.done(l)
		var unknown []unknownApp
		if len(req.Apps) > 0 {
			unknown = g.resolveTaskApps(ctx, l)
		}
		if req.OwnScreen && g.useOwnScreen(l) {
			// The person sees the task change screens, and why.
			g.addStep(l, connectStep(d.name(), title, req, true))
			g.emit(Event{Kind: EventTasks, DeviceID: d.id, TaskID: l.task.id})
		}
		if res, note := g.taskConnection(ctx, l, way); res != nil {
			return res
		} else {
			return sharedNote(withNote(readOnlyNote(withUnknownApps(g.firstScreen(ctx, l), unknown), ro), note), shared)
		}
	}
	l := g.newLeaseLocked(s, d, title, req.Apps)
	l.readOnly, l.task.readOnly = req.ReadOnly, req.ReadOnly
	l.inflight++
	shared := g.noteSharedLocked(s)
	g.mu.Unlock()
	defer s.done(l)

	unknown := g.resolveTaskApps(ctx, l)
	g.recordSession(s)
	own := req.OwnScreen && g.useOwnScreen(l)
	g.addStep(l, connectStep(d.name(), title, req, own))
	g.emit(Event{Kind: EventTasks, DeviceID: d.id, TaskID: l.task.id})
	g.emit(Event{Kind: EventDevices, DeviceID: d.id})
	res, note := g.taskConnection(ctx, l, way)
	if res != nil {
		return res
	}
	return sharedNote(withNote(readOnlyNote(withUnknownApps(g.firstScreen(ctx, l), unknown), req.ReadOnly), note), shared)
}

// waitingLocked reports whether a phone is connected but not open because
// it waits for a step (gateway.DeviceNeedsSetup). The caller holds g.mu.
func waitingLocked(d *device) bool {
	return !d.online && waitsForStep(d.info.State)
}

// setupResultLocked is device_needs_setup for a phone that waits for a
// step: the first sentence, the step in its driver's words, and what the
// agent can do meanwhile. The caller holds g.mu.
func setupResultLocked(d *device, first string) *Result {
	msg := first
	if what := strings.TrimSpace(d.info.Detail); what != "" {
		if !strings.HasSuffix(what, ".") {
			what += "."
		}
		msg += " " + what
	}
	switch d.info.Setup {
	case driver.SetupPreparing:
		msg += " Nothing for the person to do: wait a few seconds, then call phone_observe."
	case driver.SetupUpdate:
		msg += " Pinky cannot use the phone until the person updates it: tell them, and end the task."
	case driver.SetupUSBDriver:
		msg += " Ask the person to install the driver with phone_handoff. Afterwards the phone shows up under another device id: call phone_connect again."
	case driver.SetupPasscode:
		msg += " Pinky shows the person a card in Needs you asking for it, but iOS stops asking after a while: call phone_handoff now to wait until they entered it (say so in reason); the handoff ends by itself once the phone is ready."
	case driver.SetupPhoneChanged:
		msg += " Pinky will not use it until the person trusts it on its page in Pinky or plugs the phone in: tell them, and end the task."
	case driver.SetupMediaInUse:
		msg += " The person may be on a call: ask them with phone_handoff to end it or close that app when they can, saying why; the handoff ends by itself once the phone is ready."
	default:
		msg += " Ask the person to do it with phone_handoff, saying what to do; the handoff ends by itself once the phone is ready."
	}
	res := statusResult(StatusNeedsSetup, "%s", msg)
	res.Setup = d.info.Setup
	if res.Setup == "" && d.info.State == driver.StateUnauthorized {
		res.Setup = driver.FixAuthorize // as the checkup names it (ConnectionChecks)
	}
	return res
}

// connectStep is the history step of phone_connect. own says the task
// left the background display the person chose for the phone's own
// screen, as the agent asked.
func connectStep(device, title string, req ConnectRequest, own bool) StepView {
	args := map[string]any{"task": title}
	if len(req.Apps) > 0 {
		args["apps"] = req.Apps
	}
	text := fmt.Sprintf("Connected to %s for “%s”", device, title)
	if req.ReadOnly {
		args["read_only"] = true
		text += ", read only"
	}
	if own {
		args["own_screen"] = true
		text += ", on the phone's own screen"
	}
	if c := strings.TrimSpace(req.Connection); c != "" {
		args["connection"] = c
	}
	return StepView{Tool: "phone_connect", Args: argsJSON(args), Text: text, Status: string(StatusSettled), At: time.Now()}
}

// readOnlyNote tells an agent that asked for read-only what that means.
func readOnlyNote(res *Result, readOnly bool) *Result {
	if readOnly && !res.IsError() {
		res.Message = strings.TrimSpace(res.Message + " This task is read-only: anything the person's rules would ask about " +
			"is refused, and you can type only into search fields.")
	}
	return res
}

func (g *Gateway) newLeaseLocked(s *Session, d *device, title string, apps []string) *lease {
	now := time.Now()
	t := &task{id: history.NewID("t_"), sessionID: s.id, title: title, agent: s.agent.Name, deviceID: d.id,
		deviceName: d.name(), state: TaskWorking, started: now}
	l := &lease{id: history.NewID("l_"), session: s, dev: d, task: t, created: now, lastUsed: now,
		apps: append([]string(nil), apps...), grants: map[string]bool{}}
	l.ctx, l.cancel = context.WithCancel(g.ctx)
	// A watch-only agent watches the phone's own screen; a background
	// display would show it nothing.
	scope := g.opt.DefaultScope
	if st, ok := g.agents[s.agent.Name]; ok {
		scope = st.scope
	}
	l.bgWanted = g.backgroundLocked(d) && scope.Mode != ScopeWatchOnly
	d.lease = l
	if d.person {
		// The person is still in control from before: the task starts paused.
		t.state = TaskPaused
		l.tookOverSeen = true
	}
	g.noteWorkLocked(d)
	d.events = nil
	s.leases[d.id] = l
	s.used = appendUniq(s.used, d.id)
	g.recordTaskLocked(t)
	return l
}

func appendUniq(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// unknownApp is a name in a task's app list that no installed app has for
// sure, with the apps it may mean.
type unknownApp struct {
	name    string
	similar []driver.App
}

// resolveTaskApps adds the ids and names of installed apps that the task's
// allow-list names, so the list matches either. It returns the names that
// match no installed app for sure, such as 天气 on a phone whose Weather app
// has an English name, or 微信 where only 微信读书 holds it (findApp); nil
// when the phone cannot list its apps.
func (g *Gateway) resolveTaskApps(ctx context.Context, l *lease) []unknownApp {
	g.mu.Lock()
	want := append([]string(nil), l.apps...)
	g.mu.Unlock()
	if len(want) == 0 {
		return nil
	}
	dev := g.driverOf(l.dev)
	if dev == nil {
		return nil
	}
	apps, err := dev.Apps(ctx)
	if err != nil {
		return nil
	}
	out := append([]string(nil), want...)
	var unknown []unknownApp
	for _, w := range want {
		if m := findApp(apps, w); m.ok {
			// An app clone counts by the package it shares with the
			// app it copies, since the screen reads them alike.
			for _, a := range []driver.App{m.app, m.app.Original()} {
				out = appendUniq(out, a.ID)
				if a.Name != "" {
					out = appendUniq(out, a.Name)
				}
			}
		} else {
			unknown = append(unknown, unknownApp{name: w, similar: m.similar})
		}
	}
	g.mu.Lock()
	l.apps = out
	g.mu.Unlock()
	return unknown
}

// withUnknownApps tells the agent at once which names in its task's app
// list match no installed app, rather than at the first refused launch.
func withUnknownApps(res *Result, unknown []unknownApp) *Result {
	if len(unknown) == 0 || res == nil || res.IsError() {
		return res
	}
	names := make([]string, len(unknown))
	var similar []string
	for i, u := range unknown {
		names[i] = "“" + u.name + "”"
		if len(u.similar) > 0 {
			similar = append(similar, fmt.Sprintf("for “%s”: %s", u.name, appList(u.similar)))
		}
	}
	res.Message = strings.TrimSpace(res.Message + fmt.Sprintf(
		" No installed app matches %s in the task's app list, so opening those apps will be refused.", strings.Join(names, ", ")))
	if len(similar) > 0 {
		res.Message += " Similar names " + strings.Join(similar, "; ") + "."
	}
	res.Message += " Call phone_apps for the names this phone uses, then phone_connect again with the same task and the exact names or ids."
	return res
}

// firstScreen reads the whole screen for a new lease, or says which step
// the phone waits for.
func (g *Gateway) firstScreen(ctx context.Context, l *lease) *Result {
	g.mu.Lock()
	if waitingLocked(l.dev) {
		res := setupResultLocked(l.dev, fmt.Sprintf("Connected to %s for “%s”. The phone is connected but waits for a step before Pinky can use it.",
			l.dev.name(), l.task.title))
		res.Task = l.task.id
		g.mu.Unlock()
		return g.finish(l, res)
	}
	g.mu.Unlock()
	ctx, cancel := g.callCtx(ctx, l)
	defer cancel()
	g.readyScreen(ctx, l)
	g.mu.Lock()
	g.startVideoLocked(l)
	g.mu.Unlock()
	cur, err := g.observe(ctx, l.dev, true)
	if err != nil {
		return g.finish(l, g.readError(l, err))
	}
	res := g.screenResult(l, cur, true)
	g.startShot(l.dev, cur)
	res.Status = StatusSettled
	res.Task = l.task.id
	res.Message = fmt.Sprintf("Connected to %s for “%s”.", l.dev.name(), l.task.title)
	g.mu.Lock()
	person := l.dev.person
	onBG := l.bg != nil
	g.mu.Unlock()
	if onBG {
		// A background display starts empty, without a home screen.
		res.Message += " This task runs on a background display of the phone, which starts empty and has no home screen: open apps with phone_launch. " + ownScreenHint
	}
	switch {
	case person:
		res.Status = StatusPaused
		res.Message += " The person is in control of the phone; call phone_handoff to wait until they hand it back."
	case cur.Locked:
		res.Status = StatusLocked
		res.Message += " The phone is locked; call phone_handoff so the person can unlock it."
	}
	return g.finish(l, res)
}

// approveAgent holds an agent's first connection until the person allows
// it. It returns nil once the agent may connect.
func (s *Session) approveAgent(ctx context.Context, c Call, title string, readOnly bool) *Result {
	g := s.g
	if g.agentApproved(ctx, s.agent) {
		return nil
	}
	if g.opt.TrustNewAgents || s.agent.Trusted {
		g.allowAgent(s.agent)
		return nil
	}
	b := approval.Binding{Session: s.id, Tool: "phone_connect", Args: "agent"}
	if s.agent.Resume {
		// Its next session repeats the call (resume.go).
		b.Session = "agent:" + s.agent.Name
	}
	id := c.ApprovalID
	if id != "" {
		if err := g.approvals.Check(id, b); err != nil {
			if errors.Is(err, approval.ErrMismatch) {
				return errResult(ErrInvalidArgument, "approval_id %s belongs to another call.", id)
			}
			id = ""
		}
	}
	if id == "" {
		for _, info := range g.approvals.List(true) {
			if info.Binding == b {
				id = info.ID
			}
		}
	}
	if id == "" {
		mode := g.opt.DefaultScope.Mode
		if readOnly {
			mode = ScopeReadOnly
		}
		info := g.approvals.Open(approval.Request{
			Kind: approval.KindAgent, Binding: b, Rule: "New agent", Agent: s.agent.Name,
			Title:  fmt.Sprintf("Let %s use your phones?", s.agent.Name),
			Detail: fmt.Sprintf("%s wants to connect for “%s”. Allowing gives it %s access.", s.agent.Name, title, mode),
			Data:   agentRequest{readOnly: readOnly},
		})
		id = info.ID
	}
	info, _ := g.approvals.Get(id)
	out, err := g.waitApproval(ctx, nil, id, info, c.Progress)
	if err != nil {
		g.approvals.Withdraw(id, "the agent cancelled the call")
		return errResult(ErrInternal, "The call was cancelled.")
	}
	switch {
	case out.State == approval.Open:
		return &Result{Status: StatusPending, ApprovalID: id, Rule: "New agent",
			Message: fmt.Sprintf("This is %s's first connection, and the person must allow it in Pinky. Repeat phone_connect with approval_id %q.", s.agent.Name, id)}
	case out.State == approval.Used && out.Decision.Allows():
		g.allowAgent(s.agent)
		return nil
	}
	return &Result{Status: StatusDenied, Rule: "New agent", Note: denyNote(out),
		Message: "The person did not allow this agent to use Pinky."}
}

func denyNote(out approval.Outcome) string {
	switch {
	case out.Note != "":
		return out.Note
	case out.TimedOut:
		return "No answer."
	case out.State == approval.Withdrawn && out.Reason != "":
		return "Withdrawn: " + out.Reason + "."
	}
	return ""
}

// agentState returns what the person allowed an agent, loading it from
// history the first time.
func (g *Gateway) agentState(ctx context.Context, a AgentInfo) *agentState {
	g.mu.Lock()
	st, ok := g.agents[a.Name]
	g.mu.Unlock()
	if ok {
		return st
	}
	st = &agentState{name: a.Name, scope: g.opt.DefaultScope}
	if g.store != nil {
		if rec, found, err := g.store.Agent(ctx, a.Name); err == nil && found {
			st.approved = !rec.ApprovedAt.IsZero()
			st.scope = ParseScope(rec.Scope)
		} else if err == nil {
			now := time.Now()
			_ = g.store.SaveAgent(ctx, history.Agent{Name: a.Name, Client: a.Client, Scope: st.scope.String(), CreatedAt: now, LastSeen: now})
		}
	}
	g.mu.Lock()
	if cur, ok := g.agents[a.Name]; ok {
		st = cur
	} else {
		g.agents[a.Name] = st
	}
	g.mu.Unlock()
	return st
}

// agentApproved reports whether the person approved the agent.
func (g *Gateway) agentApproved(ctx context.Context, a AgentInfo) bool {
	st := g.agentState(ctx, a)
	g.mu.Lock()
	defer g.mu.Unlock()
	return st.approved
}

// setAgentApproved approves an agent with scope.
func (g *Gateway) setAgentApproved(a AgentInfo, scope Scope) {
	g.approveAgentWith(a, func(*agentState) Scope { return scope })
}

// allowAgent approves an agent whose first connection the person allowed.
// A scope the person chose meanwhile is kept: the client calls
// SetAgentScope, which also approves, right before Decide. Otherwise the
// agent gets DefaultScope.
func (g *Gateway) allowAgent(a AgentInfo) {
	g.approveAgentWith(a, func(st *agentState) Scope {
		if st.approved {
			return st.scope
		}
		return g.opt.DefaultScope
	})
}

// approveAgentWith approves an agent with the scope pick returns, chosen
// and saved under the lock so a concurrent SetAgentScope cannot be undone
// by a stale value.
func (g *Gateway) approveAgentWith(a AgentInfo, pick func(*agentState) Scope) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st, ok := g.agents[a.Name]
	if !ok {
		st = &agentState{name: a.Name}
		g.agents[a.Name] = st
	}
	scope := pick(st)
	st.approved, st.scope = true, scope
	if g.store == nil {
		return
	}
	// Queued under the lock, so history keeps the order of the changes.
	g.rec.do(func(ctx context.Context) error {
		now := time.Now()
		rec, found, err := g.store.Agent(ctx, a.Name)
		if err != nil {
			return err
		}
		if !found {
			rec = history.Agent{Name: a.Name, CreatedAt: now}
		}
		if a.Client != "" {
			rec.Client = a.Client
		}
		rec.Scope, rec.ApprovedAt, rec.LastSeen = scope.String(), now, now
		return g.store.SaveAgent(ctx, rec)
	})
}

func (g *Gateway) scopeOf(s *Session) Scope {
	g.mu.Lock()
	defer g.mu.Unlock()
	if st, ok := g.agents[s.agent.Name]; ok {
		return st.scope
	}
	return g.opt.DefaultScope
}

// Release implements phone_release.
func (s *Session) Release(ctx context.Context, req ReleaseRequest) *Result {
	g := s.g
	outcome := strings.ToLower(strings.TrimSpace(req.Outcome))
	switch outcome {
	case "":
		outcome = OutcomeDone
	case OutcomeDone, OutcomeFailed, "partial", "cancelled", "canceled":
	default:
		return errResult(ErrInvalidArgument, "outcome must be done, failed, partial or cancelled.")
	}
	if outcome == "canceled" {
		outcome = "cancelled"
	}
	if req.Export && g.opt.DataDir == "" {
		return errResult(ErrUnsupported, "This Pinky keeps no data folder, so it cannot save the task's report. Release without export.")
	}
	g.mu.Lock()
	var ls []*lease
	if req.Device != "" {
		d := g.findDeviceLocked(req.Device)
		if d != nil && s.leases[d.id] == nil {
			g.adoptLocked(s, d, "")
		}
		if d == nil || s.leases[d.id] == nil {
			g.mu.Unlock()
			return errResult(ErrInvalidArgument, "This session does not hold %q.", req.Device)
		}
		ls = append(ls, s.leases[d.id])
	} else {
		if g.noteSharedLocked(s) {
			// Every phone of the session would end another conversation's
			// task too.
			res := g.sharedResultLocked(s)
			g.mu.Unlock()
			return res
		}
		if len(s.leases) == 0 {
			for _, d := range g.adoptableLocked(s, "") {
				g.adoptLocked(s, d, "")
			}
		}
		for _, l := range s.leases {
			ls = append(ls, l)
		}
	}
	// The tasks whose screen recordings the person may give the agent.
	videos := map[*lease]*taskVideo{}
	if req.Export {
		for _, l := range ls {
			if v := l.dev.video; v != nil && v.t.id == l.task.id {
				videos[l] = v
			}
		}
	}
	g.mu.Unlock()
	if len(ls) == 0 {
		return &Result{Status: StatusSettled, Message: "Nothing to release."}
	}
	sort.Slice(ls, func(i, j int) bool { return ls[i].dev.id < ls[j].dev.id })
	var asked []*lease
	for _, l := range ls {
		if v := videos[l]; v != nil && v.recorded() {
			asked = append(asked, l)
		}
	}
	// Each export with a recording asks the person, before the task ends:
	// an answer still pending leaves it running for the agent to repeat
	// the call with approval_id.
	var answer approval.Outcome
	if len(asked) > 0 {
		out, res := g.askRecordings(ctx, s, asked, req, outcome)
		if res != nil {
			return res
		}
		answer = out
	}
	give := answer.Decision.Allows()
	g.mu.Lock()
	var names []string
	var exports []taskExport
	var released []*lease
	for _, l := range ls {
		if l.ended {
			continue
		}
		released = append(released, l)
		names = append(names, l.dev.name())
		if req.Export {
			e := taskExport{task: l.task.id, device: l.dev.name(), dir: g.FilesDir(l.dev.id), video: give && videos[l] != nil}
			text := exportStepText
			if e.video {
				text = exportVideoStepText
			}
			g.addStepLocked(l.task, StepView{Tool: "phone_release", Args: argsJSON(map[string]any{"export": true}),
				Text: text, Status: string(StatusSettled), At: time.Now()}, "", "")
			exports = append(exports, e)
		}
		g.addStepLocked(l.task, StepView{Tool: "phone_release", Args: argsJSON(map[string]any{"outcome": outcome, "summary": req.Summary}),
			Text: "Finished: " + outcome, Status: string(StatusSettled), At: time.Now()}, "", "")
		g.endLeaseLocked(l, outcome, req.Summary)
	}
	g.mu.Unlock()
	if len(released) == 0 {
		return &Result{Status: StatusSettled, Message: "Nothing to release."}
	}
	sort.Strings(names)
	res := &Result{Status: StatusSettled, Message: fmt.Sprintf("Released %s.", strings.Join(names, ", "))}
	if len(released) == 1 {
		res.Device, res.DeviceName = released[0].dev.id, released[0].dev.name()
	}
	if len(exports) > 0 {
		g.exportForAgent(ctx, exports, res)
	}
	if len(asked) > 0 && !give {
		res.Message += recordingsWithheld(answer, len(asked))
		res.Note = answer.Note
	}
	return res
}

// Observe implements phone_observe.
func (s *Session) Observe(ctx context.Context, req ObserveRequest) *Result {
	if req.WaitMS < 0 || time.Duration(req.WaitMS)*time.Millisecond > MaxObserveWait {
		return errResult(ErrInvalidArgument, "wait_ms must be between 0 and 30000.")
	}
	var save string
	if strings.TrimSpace(req.Save) != "" {
		name, why := shotName(req.Save)
		if why != "" {
			return errResult(ErrInvalidArgument, "%q cannot be a file name in the phone's folder on Pinky's computer: %s.", req.Save, why)
		}
		if s.g.opt.DataDir == "" {
			return errResult(ErrUnsupported, "This Pinky keeps no data folder, so it cannot save files.")
		}
		save = name
	}
	l, res := s.acquire(req.Device)
	if res != nil {
		return res
	}
	defer s.done(l)
	g := s.g
	ctx, cancel := g.callCtx(ctx, l)
	defer cancel()
	if res := g.gate(l, req.ApprovalID, false); res != nil {
		return g.finish(l, res)
	}
	g.readyScreen(ctx, l)
	if req.WaitMS > 0 {
		g.waitChange(ctx, l, time.Duration(req.WaitMS)*time.Millisecond, req.Progress)
	}
	mode := ocrAuto
	if req.OCR {
		mode = ocrAll
	}
	cur, err := g.observeOCR(ctx, l.dev, mode)
	if err != nil && errors.Is(err, driver.ErrOffline) && g.expectedBack(l.dev) {
		// The phone dropped as it moves between its cable and Wi-Fi, or is
		// back and being opened: read it again once it is (wifi.go).
		if res := g.gate(l, req.ApprovalID, false); res != nil {
			return g.finish(l, res)
		}
		cur, err = g.observeOCR(ctx, l.dev, mode)
	}
	if err != nil {
		return g.finish(l, g.readError(l, err))
	}
	res = g.screenResult(l, cur, true)
	res.Status = StatusSettled
	if cur.Locked {
		res.Status = StatusLocked
		res.Message = "The phone is locked; call phone_handoff so the person can unlock it."
	}
	if _, onBG := g.screenOf(l.dev); onBG && noApp(cur) {
		res.Message = strings.TrimSpace(res.Message + " " + emptyBackgroundMessage)
	}
	if req.OCR && g.noOCR(l.dev) {
		res.Message = strings.TrimSpace(res.Message + " " + noOCRMessage)
	}
	_, blocked := g.policy.Blocked(cur.App)
	if !req.Screenshot && save == "" {
		g.startShot(l.dev, cur)
	} else {
		tried := false // to save, so the message says how that went
		switch {
		case res.Rule != "":
			res.Message = strings.TrimSpace(res.Message + " No screenshot: the app is blocked.")
		case blocked:
			res.Message = strings.TrimSpace(res.Message + " " + blockedShotMessage(g.readsText(l.dev)))
		default:
			img, cov, err := g.screenshot(ctx, l.dev, cur)
			switch {
			case err == nil:
				if req.Screenshot {
					res.Screenshot = img
				}
				if save != "" {
					f, msg, err := g.saveShot(l, save, img)
					if err != nil {
						msg = "Saving the screenshot failed: " + err.Error() + "."
					} else {
						res.Saved = append(res.Saved, f)
					}
					res.Message, tried = strings.TrimSpace(res.Message+" "+msg), true
				}
				if cov.hidden > 0 {
					res.Message = strings.TrimSpace(res.Message + " Notifications and cards of blocked apps are covered in the screenshot.")
				}
				if cov.codes > 0 {
					res.Message = strings.TrimSpace(res.Message + " One-time codes on the screen are covered in the screenshot.")
				}
				// Checked against reads on both sides: steps may reuse it.
				g.keepImage(l.dev, cur, img, true)
			case errors.Is(err, driver.ErrUnsupported):
				res.Message = strings.TrimSpace(res.Message + " This phone cannot take screenshots.")
			case errors.Is(err, errLockedShot), errors.Is(err, errCodeShot), errors.Is(err, errHiddenShot), errors.Is(err, errNoAppShot):
				res.Message = strings.TrimSpace(res.Message + " No screenshot: " + err.Error() + ".")
			case errors.Is(err, errScreenChanged):
				res.Message = strings.TrimSpace(res.Message + " No screenshot: the screen changed while it was taken; observe again.")
			default:
				res.Message = strings.TrimSpace(res.Message + " The screenshot failed: " + err.Error())
			}
		}
		if save != "" && !tried {
			res.Message = strings.TrimSpace(res.Message + " Nothing was saved.")
		}
	}
	return g.finish(l, res)
}

// waitChange waits until the screen differs from what the agent last saw
// and settles, or until wait passes.
func (g *Gateway) waitChange(ctx context.Context, l *lease, wait time.Duration, progress Progress) {
	g.mu.Lock()
	base := l.baseline
	wake := l.dev.wake
	g.mu.Unlock()
	start := time.Now()
	deadline := start.Add(wait)
	lastProgress := start
	for time.Now().Before(deadline) {
		if !sleepCtx(ctx, g.opt.PollInterval, wake) {
			return
		}
		cur, err := g.observe(ctx, l.dev, false)
		if err != nil {
			return
		}
		view, _ := g.agentView(l, cur)
		if base == nil || body(view) != body(base) {
			g.settle(ctx, l.dev, g.opt.SettleMax)
			return
		}
		if progress != nil && time.Since(lastProgress) >= 5*time.Second {
			lastProgress = time.Now()
			progress(time.Since(start).Seconds(), wait.Seconds(), "Waiting for the screen to change")
		}
	}
}

// WaitFor implements phone_wait_for.
func (s *Session) WaitFor(ctx context.Context, req WaitForRequest) *Result {
	texts := waitTexts(req)
	switch {
	case len(texts) == 0:
		return errResult(ErrInvalidArgument, "text is required, or any with at least one text.")
	case len(texts) > MaxWaitTexts:
		return errResult(ErrInvalidArgument, "phone_wait_for takes at most %d texts, text and any together.", MaxWaitTexts)
	}
	area := req.Area
	if area != nil && (area.X < 0 || area.Y < 0 || area.Empty()) {
		return errResult(ErrInvalidArgument, "area needs x and y of 0 or more, and a width and height above 0.")
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	switch {
	case req.TimeoutMS < 0 || timeout > MaxWaitFor:
		return errResult(ErrInvalidArgument, "timeout_ms must be between 0 and 45000.")
	case timeout == 0:
		timeout = 10 * time.Second
	}
	l, res := s.acquire(req.Device)
	if res != nil {
		return res
	}
	defer s.done(l)
	g := s.g
	ctx, cancel := g.callCtx(ctx, l)
	defer cancel()
	if res := g.gate(l, req.ApprovalID, false); res != nil {
		return g.finish(l, res)
	}
	g.readyScreen(ctx, l)
	start := time.Now()
	// A wait ends with the call's budget too (budget.go), with what the
	// last read showed.
	deadline := budgetBefore(ctx, start.Add(timeout), 0)
	lastProgress := start
	var cur *screen.Screen
	matched := false
	// Text that only the pixels show (a web view, a canvas) is in OCR
	// elements, which tree reads leave out: read with OCR as well, at most
	// once a second, and count its answer until the next such read.
	var lastOCR time.Time
	ocrShown := ""
	// shown is the text the last reads found, the first in the order given:
	// the one that appeared, or the one that keeps a Gone wait waiting.
	shown := ""
	for first := true; ; first = false {
		read, err := g.observe(ctx, l.dev, false)
		if err != nil && cur != nil && errors.Is(err, errOutOfTime) {
			break
		}
		if err != nil {
			return g.finish(l, g.readError(l, err))
		}
		cur = read
		if first && area != nil && cur.Width > 0 && cur.Height > 0 && (area.X >= cur.Width || area.Y >= cur.Height) {
			return g.finish(l, errResult(ErrInvalidArgument, "area starts outside the screen, which is %dx%d.", cur.Width, cur.Height))
		}
		view, _ := g.agentView(l, cur)
		g.mu.Lock()
		evs := append([]screen.Event(nil), l.dev.events...)
		g.mu.Unlock()
		shown = screenShows(view, evs, texts, area)
		if shown == "" && time.Since(lastOCR) >= waitForOCREvery {
			if o, err := g.observeOCR(ctx, l.dev, ocrAll); err == nil {
				lastOCR = time.Now()
				ov, _ := g.agentView(l, o)
				ocrShown = screenShows(ov, evs, texts, area)
				if ocrShown != "" || req.Gone {
					cur = o
				}
			}
		}
		if shown == "" {
			shown = ocrShown
		}
		if (shown != "") != req.Gone {
			matched = true
			break
		}
		if !time.Now().Before(deadline) {
			break
		}
		g.mu.Lock()
		wake := l.dev.wake
		g.mu.Unlock()
		if !sleepCtx(ctx, g.opt.PollInterval, wake) {
			if ctx.Err() != nil {
				return g.finish(l, errResult(ErrInternal, "The call was cancelled."))
			}
		}
		if req.Progress != nil && time.Since(lastProgress) >= 5*time.Second {
			lastProgress = time.Now()
			req.Progress(time.Since(start).Seconds(), timeout.Seconds(), "Waiting for "+quoteList(texts, "or"))
		}
	}
	res = g.screenResult(l, cur, false)
	g.startShot(l.dev, cur)
	res.Status = StatusSettled
	res.Matched = &matched
	took := timeout
	if matched || time.Since(start) < timeout {
		took = time.Since(start)
	}
	res.Message = waitMessage(texts, shown, req.Gone, matched, area != nil, took)
	args := map[string]any{"gone": req.Gone, "timeout_ms": req.TimeoutMS}
	rest := texts
	if strings.TrimSpace(req.Text) != "" {
		args["text"], rest = texts[0], texts[1:]
	}
	if len(rest) > 0 {
		args["any"] = rest
	}
	if area != nil {
		args["area"] = map[string]any{"x": area.X, "y": area.Y, "width": area.W, "height": area.H}
	}
	// One pair of quotes around all the texts: the client reads the step's
	// text back by its shape (ui/gwtext.go).
	g.addStep(l, StepView{Tool: "phone_wait_for", Args: argsJSON(args),
		Text: fmt.Sprintf("Waited for “%s”", strings.Join(texts, " / ")), Status: string(StatusSettled), At: start, Elapsed: time.Since(start)})
	return g.finish(l, res)
}

// waitTexts is what a phone_wait_for waits for: text, then any, in the
// order given, without blanks and repeats.
func waitTexts(req WaitForRequest) []string {
	var texts []string
	seen := map[string]bool{}
	for _, t := range append([]string{req.Text}, req.Any...) {
		t = strings.TrimSpace(t)
		if key := strings.ToLower(t); t != "" && !seen[key] {
			seen[key] = true
			texts = append(texts, t)
		}
	}
	return texts
}

// waitMessage says how a phone_wait_for ended. shown is the text the last
// reads found: the one that appeared, or the one still there when a gone
// wait gave up.
func waitMessage(texts []string, shown string, gone, matched, inArea bool, took time.Duration) string {
	where := ""
	if inArea {
		where = " in the area"
	}
	after := approval.Human(took)
	switch {
	case !gone && matched:
		return fmt.Sprintf("“%s” appeared%s after %s.", shown, where, after)
	case !gone && len(texts) == 1:
		return fmt.Sprintf("“%s” has not appeared%s after %s.", texts[0], where, after)
	case !gone:
		return fmt.Sprintf("None of %s has appeared%s after %s.", quoteList(texts, "or"), where, after)
	case matched:
		return fmt.Sprintf("%s disappeared%s after %s.", quoteList(texts, "and"), where, after)
	}
	if shown == "" {
		shown = texts[0]
	}
	return fmt.Sprintf("“%s” has not disappeared%s after %s.", shown, where, after)
}

// quoteList writes texts as “a”, “b” or “c”, with or or and before the last.
func quoteList(texts []string, last string) string {
	var b strings.Builder
	for i, t := range texts {
		switch {
		case i == 0:
		case i == len(texts)-1:
			b.WriteString(" " + last + " ")
		default:
			b.WriteString(", ")
		}
		b.WriteString("“" + t + "”")
	}
	return b.String()
}

// screenShows returns the first of texts, in the order given, that an
// element's label or an event shows (containsText), or "" when none does.
// With an area, an element counts only when its centre, the @x,y on its
// line, lies inside; events have no place on the screen and do not count.
func screenShows(s *screen.Screen, evs []screen.Event, texts []string, area *screen.Rect) string {
	for _, t := range texts {
		for _, e := range s.Elements {
			if area != nil && (e.Bounds.Empty() || !area.Contains(e.Bounds.Center())) {
				continue
			}
			if containsText(e.Label, t) {
				return t
			}
		}
		if area != nil {
			continue
		}
		for _, ev := range evs {
			if containsText(ev.Text, t) {
				return t
			}
		}
	}
	return ""
}

// containsText reports whether label contains text, ignoring case. Where a
// script puts spaces between words, the text must match whole words, so
// waiting for "Stop" is not answered by the "Stopwatch" tab already on the
// screen; Chinese, Japanese, Korean and Thai text matches anywhere, as
// their words run together.
func containsText(label, text string) bool {
	l, t := strings.ToLower(label), strings.ToLower(text)
	if t == "" {
		return false
	}
	first, _ := utf8.DecodeRuneInString(t)
	last, _ := utf8.DecodeLastRuneInString(t)
	for i := 0; i <= len(l); {
		j := strings.Index(l[i:], t)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(t)
		before, _ := utf8.DecodeLastRuneInString(l[:start])
		after, _ := utf8.DecodeRuneInString(l[end:])
		if !(wordRune(before) && wordRune(first)) && !(wordRune(last) && wordRune(after)) {
			return true
		}
		_, size := utf8.DecodeRuneInString(l[start:])
		i = start + size
	}
	return false
}

// wordRune reports whether r is a letter or digit of a script that
// separates its words with spaces.
func wordRune(r rune) bool {
	if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
		return false
	}
	return !unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul,
		unicode.Thai, unicode.Lao, unicode.Khmer, unicode.Myanmar)
}

// Apps implements phone_apps: installed apps inside the agent's scope.
// Blocked apps are not listed.
func (s *Session) Apps(ctx context.Context, req AppsRequest) *Result {
	l, res := s.acquire(req.Device)
	if res != nil {
		return res
	}
	defer s.done(l)
	g := s.g
	ctx, cancel := g.callCtx(ctx, l)
	defer cancel()
	if res := g.gate(l, req.ApprovalID, false); res != nil {
		return g.finish(l, res)
	}
	dev := g.driverOf(l.dev)
	if dev == nil {
		return g.finish(l, errResult(ErrDeviceOffline, "%s is offline.", l.dev.name()))
	}
	apps, err := dev.Apps(ctx)
	if err != nil {
		return g.finish(l, g.readError(l, err))
	}
	scope := g.scopeOf(s)
	q := strings.ToLower(strings.TrimSpace(req.Query))
	var out []driver.App
	for _, a := range apps {
		orig := a.Original() // a clone is judged as the app it copies
		app := screen.App{ID: orig.ID, Name: orig.Name}
		if _, blocked := g.policy.Blocked(app); blocked {
			continue
		}
		if scope.Mode == ScopeApps && !g.policy.IsSystem(app) && !appListed(app, scope.Apps) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(a.Name), q) && !strings.Contains(strings.ToLower(a.ID), q) {
			continue
		}
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].System != out[j].System {
			return !out[i].System
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	res = &Result{Status: StatusSettled, Apps: out}
	if len(out) == 0 {
		res.Message = "No apps match."
	}
	return g.finish(l, res)
}

func appListed(app screen.App, list []string) bool {
	for _, x := range list {
		if x == app.ID || (app.Name != "" && strings.EqualFold(x, app.Name)) {
			return true
		}
	}
	return false
}

// RequestApproval implements phone_request_approval: it asks the person a
// question and records the answer. It grants nothing: a later step the
// rules ask about still asks, and so does the next question, since a yes answers this one only
// (canAllowTask).
func (s *Session) RequestApproval(ctx context.Context, req ApprovalRequest) *Result {
	summary := strings.TrimSpace(req.Summary)
	if summary == "" {
		return errResult(ErrInvalidArgument, "summary is required.")
	}
	l, res := s.acquire(req.Device)
	if res != nil {
		return res
	}
	defer s.done(l)
	g := s.g
	ctx, cancel := g.callCtx(ctx, l)
	defer cancel()
	if res := g.gate(l, req.ApprovalID, true); res != nil {
		return g.finish(l, res)
	}
	b := approval.Binding{Session: s.id, Device: l.dev.id, Lease: l.id, Tool: "phone_request_approval",
		Args: digest(summary + "\x00" + req.Details)}
	id := req.ApprovalID
	if id != "" {
		if res := g.checkApproval(id, b); res != nil {
			if res.Error != "" {
				return g.finish(l, res)
			}
			id = ""
		}
	}
	var info approval.Info
	if id == "" {
		var shot image.Image
		g.mu.Lock()
		last := l.dev.last
		g.mu.Unlock()
		if last != nil && time.Since(last.CapturedAt) < 10*time.Second {
			shot = g.rawShot(ctx, l.dev, last)
		}
		detail := s.agent.Name
		if d := strings.TrimSpace(req.Details); d != "" {
			detail += " · " + d
		}
		info = g.approvals.Open(approval.Request{Kind: approval.KindRequest, Binding: b, Rule: "Agent request",
			Title: summary, Detail: detail, Agent: s.agent.Name, TaskID: l.task.id, DeviceName: l.dev.name(),
			App: g.appName(l.dev), Screenshot: shot})
		if res := g.afterOpen(l, info); res != nil {
			return g.finish(l, res)
		}
		g.openStep(l, info, "phone_request_approval", argsJSON(map[string]any{"summary": summary, "details": req.Details}),
			fmt.Sprintf("Asked you: “%s”", summary), screen.Rect{}, screen.Point{}, "")
	} else {
		info, _ = g.approvals.Get(id)
	}
	out, err := g.waitApproval(ctx, l, info.ID, info, req.Progress)
	if errors.Is(err, errTaken) {
		return g.finish(l, takenResult(info.ID))
	}
	if err != nil {
		return g.finish(l, g.cancelled(l, info.ID))
	}
	switch {
	case out.State == approval.Open:
		return g.finish(l, g.pendingResult(l, info))
	case out.State == approval.Withdrawn:
		g.closeStep(info.ID, string(StatusDenied), 0)
		return g.finish(l, g.withdrawnResult(l, out))
	}
	g.recordDecision(info, out)
	if out.Decision.Allows() {
		g.closeStep(info.ID, string(StatusSettled), out.Waited)
		res := &Result{Status: StatusSettled, Decision: "allow", Note: out.Note,
			Message: "The person said yes. This grants nothing by itself: a later step their rules ask about, such as a payment, still asks."}
		return g.finish(l, res)
	}
	g.closeStep(info.ID, string(StatusDenied), out.Waited)
	return g.finish(l, &Result{Status: StatusDenied, Decision: "deny", Note: denyNote(out), Message: "The person said no."})
}

// Handoff implements phone_handoff: the agent waits until the person hands
// control back, with their note.
func (s *Session) Handoff(ctx context.Context, req HandoffRequest) *Result {
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		return errResult(ErrInvalidArgument, "reason is required: tell the person what you need.")
	}
	l, res := s.acquire(req.Device)
	if res != nil {
		return res
	}
	defer s.done(l)
	g := s.g
	ctx, cancel := g.callCtx(ctx, l)
	defer cancel()
	// A phone that waits for a step is what a handoff is for: it passes
	// the gate that holds other calls (device_needs_setup).
	g.mu.Lock()
	waiting := waitingLocked(l.dev) && !l.ended
	g.mu.Unlock()
	if !waiting {
		res := g.gate(l, req.ApprovalID, false)
		if res != nil && res.Error == ErrDeviceOffline && g.waitBack(ctx, l) {
			// It dropped for a moment, as a phone on Wi-Fi may while it is
			// unlocked, and is back.
			res = g.gate(l, req.ApprovalID, false)
		}
		if res != nil {
			return g.finish(l, res)
		}
	}
	b := approval.Binding{Session: s.id, Device: l.dev.id, Lease: l.id, Tool: "phone_handoff"}
	g.mu.Lock()
	if l.handedBack && req.ApprovalID == "" {
		note := l.handBackNote
		l.handedBack, l.handBackNote = false, ""
		g.mu.Unlock()
		return g.handedBackResult(ctx, l, note)
	}
	g.mu.Unlock()

	id := req.ApprovalID
	if id != "" {
		if res := g.checkApproval(id, b); res != nil {
			if res.Error != "" {
				return g.finish(l, res)
			}
			id = ""
		}
	}
	if id == "" {
		for _, info := range g.approvals.List(true) {
			if info.Binding == b {
				id = info.ID
			}
		}
	}
	var info approval.Info
	if id == "" {
		// Unlocking from the computer answers a handoff opened on a locked
		// phone only when unlocking is all it asks for.
		g.mu.Lock()
		endsOnUnlock := l.dev.locked && unlockOnly(reason)
		g.mu.Unlock()
		info = g.approvals.Open(approval.Request{Kind: approval.KindHandoff, Binding: b, Rule: "Handoff",
			Title: reason, Detail: fmt.Sprintf("%s · %s", s.agent.Name, l.task.title), Agent: s.agent.Name,
			TaskID: l.task.id, DeviceName: l.dev.name(), App: g.appName(l.dev), Timeout: g.opt.HandoffTimeout})
		g.mu.Lock()
		if endsOnUnlock {
			l.unlockHandoffs = append(l.unlockHandoffs, info.ID)
		}
		if waiting {
			// It asks for the step the phone waits for: it ends once the
			// gateway opens the phone (readyNow).
			l.readyHandoffs = append(l.readyHandoffs, info.ID)
		}
		g.mu.Unlock()
		if endsOnUnlock {
			// An unlock from the computer as soon as the card showed may
			// have finished before the line above and found nothing to
			// answer: answer now if the phone is unlocked.
			g.answerUnlockHandoffs(l.dev)
		}
		if waiting {
			g.readyNow(l.dev) // it may have opened meanwhile
		}
		if res := g.afterOpen(l, info); res != nil {
			return g.finish(l, res)
		}
		g.openStep(l, info, "phone_handoff", argsJSON(map[string]any{"reason": reason}),
			fmt.Sprintf("Asked you to take over: %s", reason), screen.Rect{}, screen.Point{}, "")
	} else {
		info, _ = g.approvals.Get(id)
	}
	out, err := g.waitApproval(ctx, l, info.ID, info, req.Progress)
	if errors.Is(err, errTaken) {
		return g.finish(l, takenResult(info.ID))
	}
	if err != nil {
		return g.finish(l, g.cancelled(l, info.ID))
	}
	switch {
	case out.State == approval.Open:
		return g.finish(l, g.pendingResult(l, info))
	case out.State == approval.Withdrawn:
		g.closeStep(info.ID, string(StatusDenied), 0)
		return g.finish(l, g.withdrawnResult(l, out))
	}
	g.recordDecision(info, out)
	if out.Decision == approval.Deny {
		g.closeStep(info.ID, string(StatusDenied), out.Waited)
		return g.finish(l, &Result{Status: StatusDenied, Note: denyNote(out), Message: "The person declined the handoff."})
	}
	g.closeStep(info.ID, string(StatusSettled), out.Waited)
	g.mu.Lock()
	l.handedBack, l.handBackNote = false, ""
	g.mu.Unlock()
	return g.handedBackResult(ctx, l, out.Note)
}

func (g *Gateway) handedBackResult(ctx context.Context, l *lease, note string) *Result {
	g.mu.Lock()
	if waitingLocked(l.dev) {
		res := setupResultLocked(l.dev, fmt.Sprintf("The person handed control back, but %s still waits for a step before Pinky can use it.", l.dev.name()))
		res.Note = note
		g.mu.Unlock()
		return g.finish(l, res)
	}
	g.mu.Unlock()
	g.readyScreen(ctx, l)
	cur, err := g.observe(ctx, l.dev, true)
	if errors.Is(err, driver.ErrOffline) {
		// A phone on Wi-Fi may drop for a moment as it is unlocked: the
		// unlock that ended an unlock handoff, or the person's own. Wait
		// for it to come back, then read it.
		g.setOffline(l.dev, "")
		if g.waitBack(ctx, l) {
			g.readyScreen(ctx, l)
			cur, err = g.observe(ctx, l.dev, true)
		}
	}
	if err != nil {
		return g.finish(l, g.readError(l, err))
	}
	res := g.screenResult(l, cur, true)
	g.startShot(l.dev, cur)
	res.Status = StatusSettled
	res.Note = note
	res.Message = "The person handed control back."
	if cur.Locked {
		res.Status = StatusLocked
		res.Message = "The person handed control back, but the phone is locked."
	}
	return g.finish(l, res)
}

// Batch implements phone_batch: steps run in order and stop at the first
// one that does not settle.
func (s *Session) Batch(ctx context.Context, req BatchRequest) *Result {
	if len(req.Steps) == 0 {
		return errResult(ErrInvalidArgument, "steps is empty.")
	}
	if len(req.Steps) > MaxBatchSteps {
		return errResult(ErrInvalidArgument, "phone_batch takes at most %d steps.", MaxBatchSteps)
	}
	for i, a := range req.Steps {
		if res := a.check(); res != nil {
			res.Message = fmt.Sprintf("step %d: %s", i+1, res.Message)
			return res
		}
	}
	l, res := s.acquire(req.Device)
	if res != nil {
		return res
	}
	defer s.done(l)
	g := s.g
	ctx, cancel := g.callCtx(ctx, l)
	defer cancel()
	out := &Result{Status: StatusSettled}
	// The longest step so far: the next step starts only while the call's
	// budget has room for one like it (budget.go).
	var longest time.Duration
	outOfTime := false
	for i, a := range req.Steps {
		if left, ok := budgetLeft(ctx); i > 0 && ok && left < max(longest, minBatchStep) {
			outOfTime = true
			break
		}
		c := Call{Device: req.Device, Intent: req.Intent, ApprovalID: a.ApprovalID, Progress: req.Progress, reuse: i > 0}
		if a.Intent != "" {
			c.Intent = a.Intent
		}
		start := time.Now()
		r := g.finish(l, g.act(ctx, l, c, a))
		longest = max(longest, time.Since(start))
		out.Steps = append(out.Steps, r)
		if r.IsError() || r.Status != StatusSettled {
			break
		}
	}
	last := out.Steps[len(out.Steps)-1]
	out.Status, out.Error, out.ApprovalID, out.Rule, out.Note = last.Status, last.Error, last.ApprovalID, last.Rule, last.Note
	out.Screen = last.Screen
	n, total := len(out.Steps), len(req.Steps)
	switch {
	case outOfTime:
		out.Message = fmt.Sprintf("Ran %s of %d, then stopped: the phone is answering slowly and the call's %s were running out. Not done: %s; continue with it in a new call.",
			stepSpan(1, n), total, human(g.opt.CallBudget), stepSpan(n+1, total))
	case n < total:
		out.Message = fmt.Sprintf("Stopped at step %d of %d.", n, total)
	}
	return g.finish(l, out)
}

// stepSpan names steps from to to: "step 3" or "steps 1-2".
func stepSpan(from, to int) string {
	if from == to {
		return fmt.Sprintf("step %d", from)
	}
	return fmt.Sprintf("steps %d-%d", from, to)
}

// minBatchStep is the least of the call's budget a phone_batch step starts
// with.
const minBatchStep = 5 * time.Second

func sleepCtx(ctx context.Context, d time.Duration, wake <-chan struct{}) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	case <-wake:
		return false
	}
}

// Package gateway is the core of Pinky. It holds every device connection,
// runs the action pipeline (lease, policy, approval, execute, settle,
// record) and tells clients what changed.
package gateway

import (
	"context"
	"image"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// API is what the desktop client and remote consoles use. Agents never get
// it: the MCP server talks to the gateway's agent-facing methods, so an
// agent can never approve, stop or take over.
type API interface {
	Devices() []DeviceView
	Tasks() []TaskView
	Needs() []NeedView
	History(ctx context.Context, q HistoryQuery) ([]TaskView, error)

	// Subscribe delivers change notifications until ctx ends. Clients read
	// the views again when an event arrives; events carry no state.
	Subscribe(ctx context.Context) <-chan Event

	// Decide answers a NeedView of kind NeedApproval.
	Decide(needID string, d Decision, note string) error
	// Unlock answers a NeedView of kind NeedUnlock with the saved PIN.
	Unlock(deviceID string) error
	TakeOver(deviceID string) error
	HandBack(deviceID, note string) error
	Stop(deviceID string) error
	StopAll() error

	// Stream returns the device's live video for the mirror.
	Stream(ctx context.Context, deviceID string, opt driver.StreamOptions) (driver.Stream, error)
	// Person input on the mirror. Any of these pauses an agent task on that
	// device until HandBack.
	Touch(deviceID string, a driver.TouchAction, p screen.Point) error
	Key(deviceID string, k driver.Key) error
	Text(deviceID string, text string) error
}

// DeviceStatus is what the sidebar dot and status line show.
type DeviceStatus string

// Device statuses, in the order the UI ranks them.
const (
	DeviceNeedsYou DeviceStatus = "needs_you"
	// DeviceNeedsSetup: the phone is connected, but Pinky cannot open it
	// until the person does a step, on the phone or on the computer: its
	// driver lists it as StateUnauthorized or StateNeedsSetup, and
	// Info.Setup and Info.Detail say which step.
	DeviceNeedsSetup DeviceStatus = "needs_setup"
	DeviceLocked     DeviceStatus = "locked"
	DeviceInControl  DeviceStatus = "in_control" // the person took over
	DeviceWorking    DeviceStatus = "working"
	DeviceIdle       DeviceStatus = "idle"
	DeviceOffline    DeviceStatus = "offline"
	// DeviceConnecting: Pinky is opening the phone, which is listed and
	// ready, or opening it again over its other connection as it moves
	// between its cable and Wi-Fi; it is online in a moment. Info.State
	// reads offline until then, as for a phone that is not open.
	DeviceConnecting DeviceStatus = "connecting"
)

// DeviceView is one row of the sidebar.
type DeviceView struct {
	Info       driver.Info
	Status     DeviceStatus
	StatusLine string // plain words: "Claude Code · Reorder lunch", "Idle", "Locked", "Needs setup", "Camera or microphone in use", "Offline"
	TaskID     string // the running task, if any
	// Features says what the phone's driver offers, so the client shows
	// only the controls that work. All false while the phone is offline.
	Features Features
	// Paused is set while the person paused Pinky's connection to the
	// phone (Gateway.PauseDevice): it shows offline, with StatusLine
	// "Paused", and Pinky does not open it until they resume it or plug
	// it in again.
	Paused bool
	// Streaming is set while the phone sends its screen to Pinky right
	// now: a mirror or a recording watches it, or the driver keeps
	// capturing it (an iPhone's display stream, which iOS shows as screen
	// sharing, stops a while after the last look or input).
	Streaming bool
}

// Features are the optional driver capabilities the client has controls for.
type Features struct {
	Unlock     bool // driver.Unlocker: unlock from the computer
	Background bool // driver.Backgrounder: agents can work on a background display
	Files      bool // driver.Files: the phone's Pinky folder
	Companion  bool // driver.CompanionInstaller: an Android companion app to install
	Pinch      bool // driver.MultiToucher: the person pinches on the mirror (Touch2)
	Rotate     bool // driver.Switcher, or the helper to move to (helperneed.go): the person turns the screen from the ⋯ menu (SetDeviceSwitch)
}

// TaskState is the state of an agent task.
type TaskState string

// Task states.
const (
	TaskWorking TaskState = "working"
	TaskWaiting TaskState = "waiting" // waiting for the person
	TaskPaused  TaskState = "paused"  // the person took over
	TaskDone    TaskState = "done"
	TaskStopped TaskState = "stopped"
	TaskFailed  TaskState = "failed"
)

// TaskView is a task card, or a row in Activity.
type TaskView struct {
	ID         string
	DeviceID   string
	DeviceName string
	Agent      string // "Claude Code"
	Title      string // the task name the agent gave
	State      TaskState
	Started    time.Time
	Ended      time.Time // zero while running
	Steps      []StepView
	// ReadOnly is set when the agent asked to run the task read-only
	// (phone_connect read_only), whatever its scope.
	ReadOnly bool
}

// StepView is one step of a task.
type StepView struct {
	Index   int
	Text    string // plain language: "Opened Dinely", "Waiting for you: pay $12.80"
	Tool    string // Developer mode only: "phone_tap"
	Args    string // Developer mode only: the arguments as JSON
	Status  string // settled, uncertain, blocked_by_policy, approval_pending, ...
	At      time.Time
	Elapsed time.Duration
	Before  string // media id of the screenshot before the step, may be empty
	After   string // media id of the screenshot after the step, may be empty
	// Target is the element the step acted on and Point where it touched,
	// in the device's screen pixels; the mirror draws its overlay from them.
	// Both are zero for steps without a position.
	Target screen.Rect
	Point  screen.Point
}

// NeedKind is what a Needs you card asks for.
type NeedKind string

// Need kinds.
const (
	NeedApproval NeedKind = "approval"
	NeedUnlock   NeedKind = "unlock"
	// NeedHandoff: the agent called phone_handoff and waits for the person.
	// Answer it with TakeOver and HandBack, or Decide (allow = hand back
	// with the note, Deny = decline, TakeOver = take over).
	NeedHandoff NeedKind = "handoff"
	// NeedSetup: a task waits for the person to do a step on the phone
	// itself before Pinky can go on, such as entering the iPhone's
	// passcode before the WebDriverAgent helper starts. Pinky shows it by
	// itself, whether or not the agent calls phone_handoff, and it goes
	// once the phone is ready; there is nothing to answer.
	NeedSetup NeedKind = "setup"
)

// NeedView is a Needs you card.
type NeedView struct {
	ID         string
	Kind       NeedKind
	DeviceID   string
	DeviceName string
	App        string // the app's display name
	Agent      string
	TaskID     string
	Title      string // "Pay $12.80?"
	Detail     string // "Claude Code · 1 chicken rice bowl, no cilantro"
	Rule       string // the rule that asked, such as "Payments"
	Created    time.Time
	Deadline   time.Time // when no answer means Deny
	Screenshot image.Image
	Target     screen.Rect // the element the action targets, in Screenshot pixels
	// Point is where the finger goes down and, for a swipe, To where it
	// lifts, in Screenshot pixels; both zero when the action has no
	// position (a key, opening an app). Point2 and To2 are the second
	// finger's of a pinch, zero for other actions. Path is every point a
	// drag's finger presses, rests on or lets go at, in order, from Point
	// to To; nil for other actions.
	Point, To   screen.Point
	Point2, To2 screen.Point
	Path        []screen.Point `json:",omitempty"`
	// AlwaysAllow is set when the approval can be answered with
	// AllowAlways: from then on, Rule (and the rules that asked with it)
	// no longer asks about such steps in App, until the person takes it
	// back in Settings; for changing the phone's settings or reading an
	// app's logs, for this agent on this phone (Exemption.Agent). Unset for
	// the agent's own questions, copying files, screen recordings, opening
	// apps and links, and while every action asks; there AllowAlways
	// counts as AllowTask.
	AlwaysAllow bool
	// AllowTask is set when the approval can be answered with AllowTask:
	// for the rest of the task, Rule (and the rules that asked with it)
	// no longer asks in App, or copying files to the phone no longer
	// asks. Unset for the agent's own questions, where a yes answers that
	// question only; there AllowTask and AllowAlways count as AllowOnce.
	AllowTask bool
	// EndsOnUnlock is set on a handoff that unlocking the phone from the
	// computer answers: the phone was locked when the agent asked, and
	// unlocking is all it asked for. Other handoffs stay open until the
	// person hands the phone back.
	EndsOnUnlock bool
	// Unblock is set on an approval to open an app that a rule blocks (a
	// finance app, or one the person blocked): any Allow lets the agent
	// open and use that app until the task ends, while Settings stay as
	// they are, and Deny keeps it blocked for the rest of the task. Rule
	// names the rule that blocks it.
	Unblock bool
	// ReadOnly is set on an agent's first-connection card when the agent
	// asked to only browse and read (phone_connect read_only): the client
	// offers that scope first.
	ReadOnly bool
}

// Decision answers an approval.
type Decision string

// Decisions.
const (
	AllowOnce   Decision = "allow_once"
	AllowTask   Decision = "allow_task"
	AllowAlways Decision = "allow_always"
	Deny        Decision = "deny"
	TakeOver    Decision = "take_over"
)

// EventKind says which views changed.
type EventKind string

// Event kinds.
const (
	EventDevices EventKind = "devices"
	EventTasks   EventKind = "tasks"
	EventNeeds   EventKind = "needs"
)

// Event is a change notification.
type Event struct {
	Kind     EventKind
	DeviceID string
	TaskID   string
	NeedID   string
}

// HistoryQuery filters Activity.
type HistoryQuery struct {
	Search string
	Before time.Time // zero = now
	Limit  int
}

package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// cowork is Claude Cowork as Pinky sees it: an agent on this computer
// whose client runs several conversations over one MCP session, and moves
// them all to a new session now and then.
var cowork = AgentInfo{Name: "Claude Cowork", Client: "local-agent-mode-pinky 1.0.0", Reconnect: true}

// The two conversations of the field report (2026-11-11).
const (
	taskA = "Set up Henge JIT and install macOS on the iPad"
	taskB = "把低电量快捷指令自动化改成低于 10%"
)

// coworkPhones is a gateway with an iPad and an iPhone, both online.
func coworkPhones(t *testing.T, opts ...func(*Options)) (*Gateway, *demo.Device, *demo.Device) {
	t.Helper()
	ipad := demo.NewDevice("ipad", "DIYgod's iPad", driver.IOS)
	iphone := demo.NewDevice("iphone", "DIYgod's iPhone", driver.IOS)
	o := Options{
		Providers: []driver.Provider{demo.NewProvider(driver.IOS, ipad, iphone)}, Policy: testPolicy(t), TrustNewAgents: true,
		ApprovalTimeout: 3 * time.Second, MaxBlock: 2 * time.Second, SettleMax: 200 * time.Millisecond,
		PollInterval: 5 * time.Millisecond, DiscoverEvery: 20 * time.Millisecond, NoScreenshots: true,
		LocalResumeGrace: time.Minute,
	}
	for _, f := range opts {
		f(&o)
	}
	g, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	waitOpen(t, g, "ipad")
	waitOpen(t, g, "iphone")
	return g, ipad, iphone
}

// taskOn returns the device's running task id and title.
func taskOn(t *testing.T, g *Gateway, id string) (string, string) {
	t.Helper()
	for _, d := range g.Devices() {
		if d.Info.ID == id {
			for _, tv := range g.Tasks() {
				if tv.ID == d.TaskID {
					return tv.ID, tv.Title
				}
			}
			return d.TaskID, ""
		}
	}
	t.Fatalf("no device %s", id)
	return "", ""
}

func launch(s *Session, device, app string) *Result {
	return s.Act(context.Background(), Call{Device: device}, Action{Tool: "launch", App: app})
}

func mustError(t *testing.T, res *Result, code ErrorCode, says ...string) {
	t.Helper()
	if res.Error != code {
		t.Fatalf("want %s, got status %q error %q: %s", code, res.Status, res.Error, res.Message)
	}
	for _, s := range says {
		if !strings.Contains(res.Message, s) {
			t.Fatalf("message misses %q: %s", s, res.Message)
		}
	}
}

// The field report: two Cowork conversations on one MCP session. B's
// phone_connect without device no longer ends A's task on the iPad, and
// A's step without device no longer lands on B's iPhone.
func TestSharedSessionIncident(t *testing.T) {
	ctx := context.Background()
	g, ipad, iphone := coworkPhones(t)

	// 10:09 A works on the iPad in the first session.
	s1 := call(t, g, cowork)
	mustStatus(t, s1.Connect(ctx, ConnectRequest{Device: "ipad", Task: taskA}), StatusSettled)
	mustStatus(t, launch(s1, "", "Dinely"), StatusSettled)
	idA, _ := taskOn(t, g, "ipad")

	// 10:30 Cowork opens a new session and closes the first: the lease is
	// kept, and A's next call (without device) continues it.
	s2 := call(t, g, cowork)
	s1.Close()
	mustStatus(t, s2.Act(ctx, Call{}, Action{Tool: "key", Key: "home"}), StatusSettled)
	if id, _ := taskOn(t, g, "ipad"); id != idA {
		t.Fatalf("A's next call runs task %s, want %s", id, idA)
	}

	// 10:33 B connects without device: not to the session's phone, which
	// runs A's task. Two phones are online, so B must pick one.
	res := s2.Connect(ctx, ConnectRequest{Task: taskB})
	mustError(t, res, ErrInvalidArgument, "Several phones are connected", "Pass device", "DIYgod's iPad (“"+taskA+"”)")
	if id, title := taskOn(t, g, "ipad"); id != idA || title != taskA {
		t.Fatalf("B's connect touched the iPad: task %s %q", id, title)
	}

	// B names its phone, the iPhone: the session now runs two tasks and
	// says so.
	res = s2.Connect(ctx, ConnectRequest{Device: "iphone", Task: taskB})
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Message, "pass device on every call") {
		t.Fatalf("B is not told the session is shared: %s", res.Message)
	}
	idB, _ := taskOn(t, g, "iphone")

	// 10:34 A's tap without device: refused, not sent to B's iPhone.
	iphone.Show("home")
	res = s2.Act(ctx, Call{Intent: "Turn on JIT automatically whenever Henge opens"}, Action{Tool: "tap", Point: &screen.Point{X: 530, Y: 827}})
	mustError(t, res, ErrInvalidArgument, "pass device on every call", `DIYgod's iPad (device "ipad", task “`+taskA+`”)`, `DIYgod's iPhone (device "iphone", task “`+taskB+`”)`)
	if iphone.Current() != "home" {
		t.Fatalf("A's step reached B's iPhone: %s", iphone.Current())
	}

	// With device, each conversation reaches its own phone and task.
	mustStatus(t, launch(s2, "ipad", "Dinely"), StatusSettled)
	if ipad.Current() != "dinely" || iphone.Current() != "home" {
		t.Fatalf("screens: iPad %s, iPhone %s", ipad.Current(), iphone.Current())
	}
	mustStatus(t, launch(s2, "iphone", "Dinely"), StatusSettled)
	if id, _ := taskOn(t, g, "ipad"); id != idA {
		t.Fatalf("iPad task %s, want %s", id, idA)
	}
	if id, _ := taskOn(t, g, "iphone"); id != idB {
		t.Fatalf("iPhone task %s, want %s", id, idB)
	}

	// phone_release without device would end both tasks: refused.
	mustError(t, s2.Release(ctx, ReleaseRequest{}), ErrInvalidArgument, "pass device")
	mustStatus(t, s2.Release(ctx, ReleaseRequest{Device: "iphone"}), StatusSettled)

	// The session stays shared after B's task ends: A's step without
	// device still names nothing, whatever phones are left.
	mustError(t, s2.Act(ctx, Call{}, Action{Tool: "key", Key: "home"}), ErrInvalidArgument, "pass device")
	// A's phone_connect for its own task needs no device: the task names
	// the phone.
	mustStatus(t, s2.Connect(ctx, ConnectRequest{Task: taskA}), StatusSettled)
	if id, _ := taskOn(t, g, "ipad"); id != idA {
		t.Fatalf("A's connect started task %s, want %s", id, idA)
	}
}

// phone_connect for another task on the session's phone is refused and
// ends nothing; the session is shared from then on, so the refused
// conversation's later steps without device do not reach the other's
// phone.
func TestConnectAnotherTaskRefused(t *testing.T) {
	ctx := context.Background()
	g, ipad, _ := coworkPhones(t)
	s := call(t, g, cowork)
	mustStatus(t, s.Connect(ctx, ConnectRequest{Device: "ipad", Task: taskA}), StatusSettled)
	idA, _ := taskOn(t, g, "ipad")
	mustStatus(t, launch(s, "", "Dinely"), StatusSettled)

	res := s.Connect(ctx, ConnectRequest{Device: "DIYgod's iPad", Task: taskB})
	mustError(t, res, ErrLeaseHeld, "another task of this session: “"+taskA+"”", "phone_release", "pass device")
	if res.Holder != "Claude Cowork · "+taskA {
		t.Fatalf("holder %q", res.Holder)
	}
	if id, title := taskOn(t, g, "ipad"); id != idA || title != taskA {
		t.Fatalf("the refused connect touched the iPad: task %s %q", id, title)
	}
	for _, tv := range g.Tasks() {
		if tv.ID == idA && tv.State != TaskWorking {
			t.Fatalf("A's task %+v", tv)
		}
	}

	// B's step without device: refused, the iPad untouched.
	mustError(t, s.Act(ctx, Call{}, Action{Tool: "key", Key: "home"}), ErrInvalidArgument, "pass device")
	mustError(t, s.Release(ctx, ReleaseRequest{}), ErrInvalidArgument, "pass device")
	if ipad.Current() != "dinely" {
		t.Fatalf("a step without device reached the iPad: %s", ipad.Current())
	}
	// A goes on with device.
	mustStatus(t, s.Act(ctx, Call{Device: "ipad"}, Action{Tool: "key", Key: "home"}), StatusSettled)

	// The same task again still continues it.
	mustStatus(t, s.Connect(ctx, ConnectRequest{Device: "ipad", Task: taskA}), StatusSettled)
	if id, _ := taskOn(t, g, "ipad"); id != idA {
		t.Fatalf("the same task started %s, want %s", id, idA)
	}
}

// B connects before A's first call on the new session: the iPad's lease,
// kept for the agent's next session, is not B's to take, and once B has
// the iPhone the session shows both tasks.
func TestConnectBeforeKeptLeaseContinues(t *testing.T) {
	ctx := context.Background()
	g, _, iphone := coworkPhones(t)
	s1 := call(t, g, cowork)
	mustStatus(t, s1.Connect(ctx, ConnectRequest{Device: "ipad", Task: taskA}), StatusSettled)
	idA, _ := taskOn(t, g, "ipad")
	s2 := call(t, g, cowork)
	s1.Close()

	// Without device: not the kept phone, which runs another task.
	mustError(t, s2.Connect(ctx, ConnectRequest{Task: taskB}), ErrInvalidArgument, "Several phones are connected", "“"+taskA+"”")
	// Naming it: refused, and the kept task goes on.
	res := s2.Connect(ctx, ConnectRequest{Device: "ipad", Task: taskB})
	mustError(t, res, ErrLeaseHeld, "another task of an earlier session of Claude Cowork: “"+taskA+"”")
	if id, _ := taskOn(t, g, "ipad"); id != idA {
		t.Fatalf("the kept task ended: iPad runs %s, want %s", id, idA)
	}

	// B on the iPhone; A's step without device is refused rather than
	// sent to the session's one phone, B's.
	mustStatus(t, s2.Connect(ctx, ConnectRequest{Device: "iphone", Task: taskB}), StatusSettled)
	iphone.Show("home")
	mustError(t, s2.Act(ctx, Call{}, Action{Tool: "key", Key: "back"}), ErrInvalidArgument, "pass device", "“"+taskA+"”", "“"+taskB+"”")
	if iphone.Current() != "home" {
		t.Fatalf("A's step reached the iPhone: %s", iphone.Current())
	}
	// A names the iPad and continues its task there.
	mustStatus(t, s2.Act(ctx, Call{Device: "ipad"}, Action{Tool: "key", Key: "home"}), StatusSettled)
	if id, _ := taskOn(t, g, "ipad"); id != idA {
		t.Fatalf("iPad runs %s, want %s", id, idA)
	}
}

// The new session comes up and B connects on it before the client closes
// the old one: closing it shows the new session both tasks, and that
// holds after A's kept lease runs out.
func TestCloseAfterNewSessionConnected(t *testing.T) {
	ctx := context.Background()
	g, _, iphone := coworkPhones(t, func(o *Options) { o.LocalResumeGrace = 100 * time.Millisecond })
	s1 := call(t, g, cowork)
	mustStatus(t, s1.Connect(ctx, ConnectRequest{Device: "ipad", Task: taskA}), StatusSettled)
	s2 := call(t, g, cowork)
	res := s2.Connect(ctx, ConnectRequest{Device: "iphone", Task: taskB})
	mustStatus(t, res, StatusSettled)
	if strings.Contains(res.Message, "pass device") {
		t.Fatalf("B told to pass device while A's session is open: %s", res.Message)
	}
	s1.Close()

	// The kept lease runs out before A comes back.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if id, _ := taskOn(t, g, "ipad"); id == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the kept lease never ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
	iphone.Show("home")
	mustError(t, s2.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}), ErrInvalidArgument, "pass device")
	if iphone.Current() != "home" {
		t.Fatalf("A's step reached B's iPhone: %s", iphone.Current())
	}
}

// A single agent that ends each task before the next, or works on two
// phones for one task, notices nothing.
func TestOneAgentNoticesNothing(t *testing.T) {
	ctx := context.Background()
	g, _, _ := coworkPhones(t)
	for _, a := range []AgentInfo{agent, cowork} {
		s := call(t, g, a)
		for _, task := range []string{"First", "Second"} {
			res := s.Connect(ctx, ConnectRequest{Device: "ipad", Task: task})
			mustStatus(t, res, StatusSettled)
			if strings.Contains(res.Message, "pass device") {
				t.Fatalf("%s: %s", a.Name, res.Message)
			}
			mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "key", Key: "home"}), StatusSettled)
			mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)
		}
		// Two phones, one task: the phone it holds is the default again
		// once the other is released.
		mustStatus(t, s.Connect(ctx, ConnectRequest{Device: "ipad", Task: "Copy a photo"}), StatusSettled)
		mustStatus(t, s.Connect(ctx, ConnectRequest{Device: "iphone", Task: "Copy a photo"}), StatusSettled)
		mustStatus(t, s.Release(ctx, ReleaseRequest{Device: "ipad"}), StatusSettled)
		mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "key", Key: "home"}), StatusSettled)
		mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)
		s.CloseNow()
	}
}

// A client that opens a session for every call (PR #81): another task's
// phone_connect no longer ends the running one; releasing it first works.
func TestResumeAnotherTaskRefused(t *testing.T) {
	ctx := context.Background()
	g, _ := newTestGateway(t)
	mustStatus(t, call(t, g, grok).Connect(ctx, ConnectRequest{Task: "Lunch"}), StatusSettled)
	taskID := g.Devices()[0].TaskID
	res := call(t, g, grok).Connect(ctx, ConnectRequest{Task: "Dinner"})
	mustError(t, res, ErrLeaseHeld, "another task of an earlier session of Grok Bot (remote): “Lunch”")
	if d := g.Devices()[0]; d.TaskID != taskID {
		t.Fatalf("the refused connect started task %s", d.TaskID)
	}
	mustStatus(t, call(t, g, grok).Act(ctx, Call{}, Action{Tool: "key", Key: "home"}), StatusSettled)
	mustStatus(t, call(t, g, grok).Release(ctx, ReleaseRequest{}), StatusSettled)
	mustStatus(t, call(t, g, grok).Connect(ctx, ConnectRequest{Task: "Dinner"}), StatusSettled)
	if d := g.Devices()[0]; d.TaskID == taskID {
		t.Fatal("Dinner did not start a task")
	}
}

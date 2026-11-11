package gateway

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// loadPolicy loads the rules and apps files from dir, as the app does, with
// the demo apps' categories.
func loadPolicy(t *testing.T, dir string) *policy.Engine {
	t.Helper()
	pol, err := policy.Load(filepath.Join(dir, "rules.yaml"), filepath.Join(dir, "apps.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// Preset 1, as testPolicy: these tests check that Commits asks too.
	if err := pol.SetPreset(policy.PresetCommits); err != nil {
		t.Fatal(err)
	}
	for cat, e := range demo.PolicyApps() {
		pol.AddApps(cat, e)
	}
	return pol
}

// payInDinely walks a task to Dinely's cart and taps Place order.
func payInDinely(t *testing.T, s *Session) *Result {
	t.Helper()
	cart := toCart(t, s)
	return s.Act(context.Background(), Call{Intent: "Pay $12.80"}, Action{Tool: "tap", Ref: ref(t, cart, "Place order · $12.80")})
}

// answer answers the next need with d, after checking it.
func answer(t *testing.T, g *Gateway, d Decision, check func(NeedView)) {
	t.Helper()
	go func() {
		n := waitNeed(t, g)
		if check != nil {
			check(n)
		}
		if err := g.Decide(n.ID, d, ""); err != nil {
			t.Error(err)
		}
	}()
}

// TestAlwaysAllowLastsAcrossTasks answers a payment in Dinely with Always
// allow: the next task's payment there runs without asking, also after a
// restart, while a commit there that is not a payment, and a payment in
// another app, still ask.
func TestAlwaysAllowLastsAcrossTasks(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	var otherApp, deleteButton atomic.Bool
	g, hd := newHookGateway(t, func(o *Options) { o.Policy = loadPolicy(t, dir) })
	hd.setObserve(func(sc *screen.Screen) {
		if sc.App.ID != demo.Dinely {
			return
		}
		if otherApp.Load() {
			sc.App = screen.App{Name: "Other Eats", ID: "com.othereats.app"}
		}
		if deleteButton.Load() {
			sc.Elements = append(sc.Elements, screen.Element{Role: screen.RoleButton, Label: "Delete account",
				ID: "dinely:delete", Path: "account/delete", Bounds: screen.Rect{X: 100, Y: 1800, W: 880, H: 140}, Source: screen.SourceTree})
		}
	})

	// The first task asks, and the person answers Always allow.
	s, _ := connect(t, g, "Lunch")
	answer(t, g, AllowAlways, func(n NeedView) {
		if n.Rule != "Payments" || n.App != "Dinely" || !n.AlwaysAllow {
			t.Errorf("need %+v", n)
		}
	})
	res := payInDinely(t, s)
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Message, `from now on "Payments" no longer asks in Dinely`) {
		t.Fatalf("message: %s", res.Message)
	}
	want := []Exemption{{Rule: "Payments", With: []string{"Commits"}, AppID: demo.Dinely, AppName: "Dinely"}}
	if got := g.Exemptions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("exemptions %+v, want %+v", got, want)
	}
	s.Release(ctx, ReleaseRequest{Outcome: "done"})

	// The next task pays in Dinely without asking.
	g.opt.MaxBlock = 50 * time.Millisecond
	s, _ = connect(t, g, "Lunch again")
	res = payInDinely(t, s)
	mustStatus(t, res, StatusSettled)
	if hd.Current() != "done" || len(g.Needs()) != 0 {
		t.Fatalf("payment did not go through: %s, needs %+v", hd.Current(), g.Needs())
	}
	if !strings.Contains(res.Message, `No approval needed: the person always allows "Payments" in Dinely.`) {
		t.Fatalf("message: %s", res.Message)
	}

	// Another rule in Dinely still asks: Delete account is a commit, not a
	// payment.
	deleteButton.Store(true)
	res = s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"})
	res = tap(s, ref(t, res, "Account"))
	del := ref(t, res, "Delete account")
	res = tap(s, del)
	mustStatus(t, res, StatusPending)
	if n := g.Needs()[0]; n.Rule != "Commits" || n.App != "Dinely" || !n.AlwaysAllow {
		t.Fatalf("delete in Dinely: %+v", n)
	}
	g.Decide(res.ApprovalID, Deny, "")
	mustStatus(t, s.Act(ctx, Call{ApprovalID: res.ApprovalID}, Action{Tool: "tap", Ref: del}), StatusDenied)
	deleteButton.Store(false)

	// So does the same rule in another app.
	otherApp.Store(true)
	cart := toCart(t, s)
	place := ref(t, cart, "Place order · $12.80")
	res = tap(s, place)
	mustStatus(t, res, StatusPending)
	if n := g.Needs()[0]; n.Rule != "Payments" || n.App != "Other Eats" || !n.AlwaysAllow {
		t.Fatalf("payment in another app: %+v", n)
	}
	g.Decide(res.ApprovalID, Deny, "")
	mustStatus(t, s.Act(ctx, Call{ApprovalID: res.ApprovalID}, Action{Tool: "tap", Ref: place}), StatusDenied)
	otherApp.Store(false)
	s.Release(ctx, ReleaseRequest{Outcome: "done"})
	g.Close()

	// After a restart, from the rules file.
	g2, hd2 := newHookGateway(t, func(o *Options) { o.Policy = loadPolicy(t, dir); o.MaxBlock = 50 * time.Millisecond })
	s, _ = connect(t, g2, "Lunch after a restart")
	mustStatus(t, payInDinely(t, s), StatusSettled)
	if hd2.Current() != "done" {
		t.Fatalf("payment after a restart: %s", hd2.Current())
	}

	// Taken back in Settings, it asks again.
	if err := g2.RemoveExemption(g2.Exemptions()[0]); err != nil {
		t.Fatal(err)
	}
	if got := g2.Exemptions(); len(got) != 0 {
		t.Fatalf("exemptions after taking it back: %+v", got)
	}
	mustStatus(t, payInDinely(t, s), StatusPending)
	data, err := os.ReadFile(filepath.Join(dir, "rules.yaml"))
	if err != nil || strings.Contains(string(data), "\nalways_allow:") {
		t.Fatalf("rules.yaml after taking it back (%v):\n%s", err, data)
	}
}

// TestAlwaysAllowedStep: a payment in Dinely that runs without asking
// because of the person's Always allow says so on its step, which history
// keeps and the report shows; the payment they answered Always allow just
// then, and the steps around both, do not.
func TestAlwaysAllowedStep(t *testing.T) {
	ctx := context.Background()
	g, _ := newTestGateway(t, func(o *Options) { o.DataDir = t.TempDir() })
	s, _ := connect(t, g, "Lunch")
	answer(t, g, AllowAlways, nil)
	mustStatus(t, payInDinely(t, s), StatusSettled)
	s.Release(ctx, ReleaseRequest{Outcome: "done"})
	s, _ = connect(t, g, "Lunch again")
	mustStatus(t, payInDinely(t, s), StatusSettled)
	s.Release(ctx, ReleaseRequest{Outcome: "done"})

	const place, mark = "Tapped “Place order · $12.80”", "Always allowed: “Payments” in Dinely"
	stored, err := g.History(ctx, HistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tasks := range [][]TaskView{g.Tasks(), stored} {
		if len(tasks) != 2 {
			t.Fatalf("%d tasks", len(tasks))
		}
		for _, task := range tasks {
			want := place
			if task.Title == "Lunch again" {
				want += "\n" + mark
			}
			paid := false
			for _, st := range task.Steps {
				first, _, _ := strings.Cut(st.Text, "\n")
				switch {
				case first == place:
					paid = true
					if st.Text != want || st.Status != string(StatusSettled) {
						t.Errorf("%s: the payment's step is %q (%s), want %q", task.Title, st.Text, st.Status, want)
					}
				case first != st.Text:
					t.Errorf("%s: step %q has a second line", task.Title, st.Text)
				}
			}
			if !paid {
				t.Errorf("%s: no payment among %+v", task.Title, task.Steps)
			}
		}
	}

	// The report shows the mark under the step, and a recording names the
	// step by its first line.
	path, err := g.ExportHistory(ctx, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for want, n := range map[string]int{
		`<span class="text">` + place + `</span>`: 2, // both lunches
		`<div class="detail">` + mark + `</div>`:  1, // the one paid on Always allow
	} {
		if got := strings.Count(string(b), want); got != n {
			t.Errorf("the report has %q %d times, want %d", want, got, n)
		}
	}
	start := time.Now()
	marks := marksFor(RecordingView{Start: start, Length: time.Minute},
		[]StepView{{Index: 6, Text: place + "\n" + mark, At: start.Add(time.Second)}})
	if len(marks) != 1 || marks[0].Text != place {
		t.Errorf("marks %+v", marks)
	}
}

func waitGone(t *testing.T, g *Gateway) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if len(g.Needs()) == 0 {
			return
		}
	}
	t.Fatalf("needs left: %+v", g.Needs())
}

// TestAlwaysAllowKeptWithoutAWaitingCall answers Always allow after the
// agent's call returned approval_pending: the answer is kept at once, and
// the call that comes back with the id runs.
func TestAlwaysAllowKeptWithoutAWaitingCall(t *testing.T) {
	g, dev := newTestGateway(t, func(o *Options) { o.MaxBlock = 20 * time.Millisecond })
	s, _ := connect(t, g, "Lunch")
	cart := toCart(t, s)
	place := ref(t, cart, "Place order · $12.80")
	res := tap(s, place)
	mustStatus(t, res, StatusPending)
	if err := g.Decide(res.ApprovalID, AllowAlways, ""); err != nil {
		t.Fatal(err)
	}
	if got := g.Exemptions(); len(got) != 1 || got[0].Rule != "Payments" {
		t.Fatalf("exemptions before the call came back: %+v", got)
	}
	res = s.Act(context.Background(), Call{ApprovalID: res.ApprovalID}, Action{Tool: "tap", Ref: place})
	mustStatus(t, res, StatusSettled)
	if dev.Current() != "done" || !strings.Contains(res.Message, "no longer asks in Dinely") {
		t.Fatalf("screen %s, message %s", dev.Current(), res.Message)
	}
}

// TestAlwaysAllowNotOffered covers approvals whose Always allow cannot be
// kept: there it counts as Allow for this task, and nothing is saved.
func TestAlwaysAllowNotOffered(t *testing.T) {
	ctx := context.Background()

	t.Run("every action asks", func(t *testing.T) {
		g, dev := newTestGateway(t)
		if err := g.Policy().SetPreset(policy.PresetEvery); err != nil {
			t.Fatal(err)
		}
		s, _ := connect(t, g, "Calculator")
		dev.Show("calc")
		obs := s.Observe(ctx, ObserveRequest{})
		answer(t, g, AllowAlways, func(n NeedView) {
			if n.Rule != policy.RuleEvery || n.AlwaysAllow {
				t.Errorf("need %+v", n)
			}
		})
		res := tap(s, ref(t, obs, "7"))
		mustStatus(t, res, StatusSettled)
		if !strings.Contains(res.Message, "for the rest of the task") || len(g.Exemptions()) != 0 {
			t.Fatalf("message %q, exemptions %+v", res.Message, g.Exemptions())
		}
		// Allowed for the task: the next tap does not ask.
		mustStatus(t, tap(s, ref(t, res, "8")), StatusSettled)
	})

	t.Run("task apps", func(t *testing.T) {
		g, dev := newTestGateway(t)
		s, _ := connect(t, g, "Chat")
		dev.Show("chats")
		res := tap(s, ref(t, s.Observe(ctx, ObserveRequest{}), "Sam"))
		input := ref(t, res, "Message")
		if err := g.SetTaskApps("pixel", []string{"Dinely"}); err != nil {
			t.Fatal(err)
		}
		answer(t, g, AllowAlways, func(n NeedView) {
			if n.Rule != policy.RuleTaskApps || n.AlwaysAllow {
				t.Errorf("need %+v", n)
			}
		})
		mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "type", Ref: input, Text: "draft"}), StatusSettled)
		if len(g.Exemptions()) != 0 {
			t.Fatalf("exemptions %+v", g.Exemptions())
		}
	})

	t.Run("message outside the task's apps", func(t *testing.T) {
		// Messages can be always allowed; the task's app list cannot, and
		// still asks in the next task with the same list.
		g, dev := newTestGateway(t, func(o *Options) { o.MaxBlock = 50 * time.Millisecond })
		s, _ := connect(t, g, "Chat")
		dev.Show("chats")
		res := tap(s, ref(t, s.Observe(ctx, ObserveRequest{}), "Sam"))
		input := ref(t, res, "Message")
		if err := g.SetTaskApps("pixel", []string{"Dinely"}); err != nil {
			t.Fatal(err)
		}
		res = s.Act(ctx, Call{}, Action{Tool: "type", Ref: input, Text: "one", Submit: true})
		mustStatus(t, res, StatusPending)
		n := g.Needs()[0]
		if n.Rule != "Messages" || !n.AlwaysAllow {
			t.Fatalf("need %+v", n)
		}
		g.Decide(n.ID, AllowAlways, "")
		mustStatus(t, s.Act(ctx, Call{ApprovalID: n.ID}, Action{Tool: "type", Ref: input, Text: "one", Submit: true}), StatusSettled)
		if got := g.Exemptions(); len(got) != 1 || got[0].Rule != "Messages" || len(got[0].With) != 0 || got[0].AppName != "Chatter" {
			t.Fatalf("exemptions %+v", got)
		}
		// The rest of the task: allowed.
		mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "type", Ref: input, Text: "two", Submit: true}), StatusSettled)
		// The next task with the same list asks about the list only.
		mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)
		mustStatus(t, s.Connect(ctx, ConnectRequest{Task: "Chat again", Apps: []string{"Dinely"}}), StatusSettled)
		res = s.Act(ctx, Call{}, Action{Tool: "type", Ref: input, Text: "three", Submit: true})
		mustStatus(t, res, StatusPending)
		if n := g.Needs()[0]; n.Rule != policy.RuleTaskApps || n.AlwaysAllow {
			t.Fatalf("next task: %+v", n)
		}
		// Without a list, messages in Chatter no longer ask.
		g.Decide(res.ApprovalID, Deny, "")
		mustStatus(t, s.Act(ctx, Call{ApprovalID: res.ApprovalID}, Action{Tool: "type", Ref: input, Text: "three", Submit: true}), StatusDenied)
		mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)
		mustStatus(t, s.Connect(ctx, ConnectRequest{Task: "Chat without a list"}), StatusSettled)
		mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "type", Ref: input, Text: "four", Submit: true}), StatusSettled)
		if chat := dev.Chat(); chat[len(chat)-1] != "Me: four" {
			t.Fatalf("chat: %v", chat)
		}
	})

	t.Run("agent request", func(t *testing.T) {
		g, _ := newTestGateway(t)
		s, _ := connect(t, g, "Ask")
		answer(t, g, AllowAlways, func(n NeedView) {
			if n.Rule != "Agent request" || n.AlwaysAllow {
				t.Errorf("need %+v", n)
			}
		})
		res := s.RequestApproval(ctx, ApprovalRequest{Summary: "May I order lunch?"})
		if res.Decision != "allow" || len(g.Exemptions()) != 0 {
			t.Fatalf("result %+v, exemptions %+v", res, g.Exemptions())
		}
	})

	t.Run("link", func(t *testing.T) {
		g, _ := newTestGateway(t)
		s, _ := connect(t, g, "Link")
		answer(t, g, Deny, func(n NeedView) {
			if n.Rule != "Installs and links" || n.AlwaysAllow {
				t.Errorf("need %+v", n)
			}
		})
		res := s.Act(ctx, Call{}, Action{Tool: "launch", App: "https://dinely.app/offer"})
		mustStatus(t, res, StatusDenied)
	})
}

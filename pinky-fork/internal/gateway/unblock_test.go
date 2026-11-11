package gateway

import (
	"context"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

const wallet = "com.google.android.apps.walletnfcrel"

// Opening a blocked finance app asks the person, and Allow for this task
// lets the agent open it and read its screen until the task ends (field
// test #18). Payments in it still ask, Pinky keeps no pictures or screen
// text of it, and the next task finds it blocked again.
func TestAllowABlockedAppForTheTask(t *testing.T) {
	ctx := context.Background()
	g, dev := newTestGateway(t, func(o *Options) { o.DataDir = t.TempDir(); o.NoScreenshots = false })
	s, _ := connect(t, g, "Bookkeeping")

	go func() {
		n := waitNeed(t, g)
		if !n.Unblock || n.Rule != "Finance apps" || n.AlwaysAllow || n.Title != "Open Google Wallet?" {
			t.Errorf("need: %+v", n)
		}
		g.Decide(n.ID, AllowTask, "")
	}()
	res := s.Act(ctx, Call{}, Action{Tool: "launch", App: "Google Wallet"})
	mustStatus(t, res, StatusSettled)
	if dev.Current() != "wallet" || res.Rule != "" || !strings.Contains(res.Screen.Render(), "Visa •••• 4242") ||
		!strings.Contains(res.Message, "allowed Google Wallet for this task") {
		t.Fatalf("after Allow: screen %s, rule %q, %s\n%s", dev.Current(), res.Rule, res.Message, res.Screen.Render())
	}
	// The person opens it again and again: no more questions.
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "key", Key: "home"}), StatusSettled)
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Google Wallet"}), StatusSettled)

	// What the rules ask about still asks.
	go func() {
		n := waitNeed(t, g)
		if n.Rule != "Payments" || n.Unblock || n.Screenshot != nil {
			t.Errorf("payment need: %+v", n)
		}
		g.Decide(n.ID, Deny, "")
	}()
	mustStatus(t, tap(s, ref(t, res, "Pay $25.00")), StatusDenied)
	obs := s.Observe(ctx, ObserveRequest{Screenshot: true})
	if obs.Screenshot != nil || !strings.Contains(obs.Message, "no pictures of a blocked app") {
		t.Fatalf("observe with a screenshot: %v %s", obs.Screenshot != nil, obs.Message)
	}

	// History keeps the steps, without pictures or screen text of it.
	s.Release(ctx, ReleaseRequest{})
	g.Flush()
	tasks, err := g.History(ctx, HistoryQuery{})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("history: %v %v", tasks, err)
	}
	opened := 0
	for _, st := range tasks[0].Steps {
		if st.Tool == "phone_launch" {
			opened++
			if st.After != "" {
				t.Errorf("a picture of the blocked app: %+v", st)
			}
		}
	}
	if opened != 2 {
		t.Fatalf("launch steps: %+v", tasks[0].Steps)
	}
	if out, err := g.store.Steps(ctx, tasks[0].ID); err == nil {
		for _, st := range out {
			if strings.Contains(st.ScreenText, "4242") {
				t.Errorf("the blocked app's screen is in history: %+v", st)
			}
		}
	}

	// The next task asks again.
	s2, _ := connect(t, g, "Bookkeeping again")
	go func() {
		n := waitNeed(t, g)
		g.Decide(n.ID, Deny, "")
	}()
	mustStatus(t, s2.Act(ctx, Call{}, Action{Tool: "launch", App: "Google Wallet"}), StatusDenied)
}

// Password managers are never offered: the launch is refused at once.
func TestPasswordManagersStayBlocked(t *testing.T) {
	g, dev := newTestGateway(t)
	g.Policy().AddApps(policy.CatPasswords, policy.CategoryEntries{IDs: []string{demo.Calc}})
	s, _ := connect(t, g, "Look around")
	res := s.Act(context.Background(), Call{}, Action{Tool: "launch", App: "Calculator"})
	mustStatus(t, res, StatusBlocked)
	if len(g.Needs()) != 0 || dev.Current() != "home" {
		t.Fatalf("needs %+v, screen %s", g.Needs(), dev.Current())
	}
}

// An agent can ask for a read-only task (field test #19): whatever its
// scope, the task refuses what would ask, and says so; the person can still
// allow a blocked app for it.
func TestReadOnlyTask(t *testing.T) {
	ctx := context.Background()
	g, dev := newTestGateway(t)
	s := g.OpenSession(agent)
	t.Cleanup(s.Close)
	res := s.Connect(ctx, ConnectRequest{Task: "Read the balances", ReadOnly: true})
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Message, "This task is read-only") {
		t.Fatalf("connect: %s", res.Message)
	}
	if ts := g.Tasks(); len(ts) != 1 || !ts[0].ReadOnly {
		t.Fatalf("tasks: %+v", ts)
	}
	go func() {
		n := waitNeed(t, g)
		g.Decide(n.ID, AllowOnce, "")
	}()
	res = s.Act(ctx, Call{}, Action{Tool: "launch", App: "Google Wallet"})
	mustStatus(t, res, StatusSettled)
	res = tap(s, ref(t, res, "Pay $25.00"))
	mustStatus(t, res, StatusBlocked)
	if res.Rule != policy.RuleReadOnly || len(dev.Inputs()) != 1 {
		t.Fatalf("pay in a read-only task: %+v, inputs %+v", res, dev.Inputs())
	}
	// Connecting again for the same task never widens it.
	mustStatus(t, s.Connect(ctx, ConnectRequest{Task: "Read the balances"}), StatusSettled)
	if ts := g.Tasks(); len(ts) != 1 || !ts[0].ReadOnly {
		t.Fatalf("tasks after connecting again: %+v", ts)
	}
}

// An agent that asks for read-only on its first connection gets a card
// that says so, and the scope offered with it.
func TestNewAgentAsksForReadOnly(t *testing.T) {
	g, _ := newTestGateway(t, func(o *Options) { o.TrustNewAgents = false })
	s := g.OpenSession(AgentInfo{Name: "Bookkeeper", Client: "test"})
	t.Cleanup(s.Close)
	done := make(chan *Result, 1)
	go func() {
		done <- s.Connect(context.Background(), ConnectRequest{Task: "Read the balances", ReadOnly: true})
	}()
	n := waitNeedKind(t, g, NeedApproval)
	if !newAgentNeedView(n) || !n.ReadOnly || !strings.Contains(n.Detail, "read_only access") {
		t.Fatalf("first connection: %+v", n)
	}
	g.Decide(n.ID, Deny, "")
	<-done

	s2 := g.OpenSession(AgentInfo{Name: "Shopper", Client: "test"})
	t.Cleanup(s2.Close)
	go func() { done <- s2.Connect(context.Background(), ConnectRequest{Task: "Lunch"}) }()
	if n := waitNeedKind(t, g, NeedApproval); n.ReadOnly {
		t.Fatalf("an agent that did not ask: %+v", n)
	}
}

func newAgentNeedView(n NeedView) bool { return n.Kind == NeedApproval && n.Rule == "New agent" }

// sameApp matches by id, or by name when one side has none.
func TestSameApp(t *testing.T) {
	wise := screen.App{ID: "com.transferwise.Transferwise", Name: "Wise"}
	for _, c := range []struct {
		b    screen.App
		want bool
	}{
		{screen.App{ID: "com.transferwise.Transferwise"}, true},
		{screen.App{Name: "wise"}, true},
		{screen.App{ID: "com.other", Name: "Wise"}, false},
		{screen.App{}, false},
	} {
		if got := sameApp(wise, c.b); got != c.want {
			t.Errorf("sameApp(%v, %v) = %v", wise, c.b, got)
		}
	}
}

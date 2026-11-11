package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// TestAllowTaskOnlyWhereItLasts: Allow for this task is offered where the
// rest of the task skips the question (a payment, a file copy), and not for
// the agent's own questions, where a yes answers that question only.
func TestAllowTaskOnlyWhereItLasts(t *testing.T) {
	ctx := context.Background()

	t.Run("agent question", func(t *testing.T) {
		g, _ := newTestGateway(t, func(o *Options) { o.DataDir = t.TempDir() })
		s, conn := connect(t, g, "Lunch")
		// An older client offers Allow for this task and Always allow on
		// every approval: each counts as a yes to this question, and the
		// next question asks again.
		answers := []Decision{AllowTask, AllowAlways}
		for i, d := range answers {
			answer(t, g, d, func(n NeedView) {
				if n.Rule != "Agent request" || n.AllowTask || n.AlwaysAllow {
					t.Errorf("need %+v", n)
				}
			})
			res := s.RequestApproval(ctx, ApprovalRequest{Summary: []string{"May I order lunch?", "May I pay $12.80?"}[i]})
			mustStatus(t, res, StatusSettled)
			if res.Decision != "allow" || !strings.Contains(res.Message, "grants nothing") {
				t.Fatalf("answer %s: %+v", d, res)
			}
		}
		if len(g.Exemptions()) != 0 {
			t.Fatalf("exemptions %+v", g.Exemptions())
		}
		g.rec.flush()
		aps, err := g.store.Approvals(ctx, conn.Task)
		if err != nil {
			t.Fatal(err)
		}
		if len(aps) != len(answers) {
			t.Fatalf("approvals %+v", aps)
		}
		for _, a := range aps {
			if a.Kind != "request" || a.Decision != "allow_once" {
				t.Errorf("recorded %+v", a)
			}
		}
	})

	t.Run("payment", func(t *testing.T) {
		g, dev := newTestGateway(t)
		s, _ := connect(t, g, "Lunch")
		answer(t, g, AllowTask, func(n NeedView) {
			if n.Rule != "Payments" || n.App != "Dinely" || !n.AllowTask {
				t.Errorf("need %+v", n)
			}
		})
		res := payInDinely(t, s)
		mustStatus(t, res, StatusSettled)
		if !strings.Contains(res.Message, `"Payments" for the rest of the task`) {
			t.Fatalf("message: %s", res.Message)
		}
		// The next payment in the task does not ask.
		res = payInDinely(t, s)
		mustStatus(t, res, StatusSettled)
		if dev.Current() != "done" || len(g.Needs()) != 0 {
			t.Fatalf("second payment: %s, needs %+v", dev.Current(), g.Needs())
		}
	})

	t.Run("file copy", func(t *testing.T) {
		x := newExtraDev("pixel", "Pixel 9")
		g := newExtraGateway(t, x)
		s, _ := connect(t, g, "Send a plan")
		dir := g.FilesDir("pixel")
		writeLocal(t, dir, "plan.md", "# plan", time.Now())
		answer(t, g, AllowTask, func(n NeedView) {
			if n.Rule != filesRule || !n.AllowTask || n.AlwaysAllow {
				t.Errorf("need %+v", n)
			}
		})
		mustStatus(t, s.Files(ctx, FilesRequest{Action: "push"}), StatusSettled)
		// The next copy in the task does not ask.
		writeLocal(t, dir, "plan.md", "# plan, v2", time.Now().Add(time.Minute))
		mustStatus(t, s.Files(ctx, FilesRequest{Action: "push"}), StatusSettled)
		if string(x.files["plan.md"].data) != "# plan, v2" {
			t.Fatalf("second copy %q", x.files["plan.md"].data)
		}
	})
}

// TestAlwaysAllowNotKeptIsForTheTask: Always allow from an older client,
// on an approval where it cannot be kept, counts as Allow for this task,
// and the agent hears so. Here the task's app list asks while messages in
// Chatter are always allowed: the list asks again in the next task.
func TestAlwaysAllowNotKeptIsForTheTask(t *testing.T) {
	ctx := context.Background()
	g, dev := newTestGateway(t)
	if _, err := g.Policy().Exempt([]string{"Messages"}, screen.App{Name: "Chatter", ID: demo.Chatter}); err != nil {
		t.Fatal(err)
	}
	s, _ := connect(t, g, "Chat")
	dev.Show("chats")
	res := tap(s, ref(t, s.Observe(ctx, ObserveRequest{}), "Sam"))
	input := ref(t, res, "Message")
	if err := g.SetTaskApps("pixel", []string{"Dinely"}); err != nil {
		t.Fatal(err)
	}
	answer(t, g, AllowAlways, func(n NeedView) {
		if n.Rule != policy.RuleTaskApps || n.AlwaysAllow || !n.AllowTask {
			t.Errorf("need %+v", n)
		}
	})
	res = s.Act(ctx, Call{}, Action{Tool: "type", Ref: input, Text: "one", Submit: true})
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Message, `"Task apps" for the rest of the task`) || strings.Contains(res.Message, "from now on") {
		t.Fatalf("message: %s", res.Message)
	}
	if got := g.Exemptions(); len(got) != 1 || got[0].Rule != "Messages" {
		t.Fatalf("exemptions %+v", got)
	}
}

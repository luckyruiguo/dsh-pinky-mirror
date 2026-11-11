package ui

import (
	"context"
	"slices"
	"strings"
	"testing"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/ui/demogw"
)

// TestGatewayAgentQuestionMenu: on the agent's own questions, More options
// offers no Allow for this task, on the card nor in the floating panel, and
// the next question asks again. The payment after them still offers it,
// and the panel then says payments in Dinely are allowed until the task
// ends.
func TestGatewayAgentQuestionMenu(t *testing.T) {
	ga := newGatewayApp(t, true)
	panel := mg.NewTester(ga.panelView, panelW, panelH)
	views := []struct {
		name string
		tt   *mg.Tester
	}{
		{"card", mg.NewTester(ga.view, 1280, 820)},
		{"floating panel", panel},
	}
	questions := []string{"May I reorder last week's lunch?", "May I pay $12.80 for it?"}
	done := ga.agent(t, "Claude Code", pixel, func(ag *demogw.Agent, ctx context.Context) error {
		if err := ag.Connect(ctx, "Reorder last week's lunch"); err != nil {
			return err
		}
		for _, q := range questions {
			if err := ag.Ask(ctx, q); err != nil {
				return err
			}
		}
		return ag.ReorderLunch(ctx) // the same task: it keeps the lease
	})
	// moreOptions opens More options, returns its items and closes it.
	moreOptions := func(name string, tt *mg.Tester) []string {
		t.Helper()
		if tt == panel {
			tt.SetSize(ga.panelSize()) // as fitPanel sizes the window
		}
		tt.Frame()
		if err := tt.Click("More options"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		texts := tt.Texts()
		if err := tt.Click("More options"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return texts
	}

	for _, q := range questions {
		var n gateway.NeedView
		ga.waitFor(t, q, func() (ok bool) {
			n, ok = ga.need(func(n gateway.NeedView) bool { return n.Title == q })
			return
		})
		if n.Rule != "Agent request" || n.AllowTask || n.AlwaysAllow {
			t.Fatalf("question %+v", n)
		}
		for _, v := range views {
			texts := moreOptions(v.name, v.tt)
			if !slices.Contains(texts, "Take over") || !slices.Contains(texts, "Deny with a note") {
				t.Fatalf("%s: More options: %q", v.name, texts)
			}
			for _, s := range texts {
				if s == "Allow for this task" || strings.HasPrefix(s, "Always allow") {
					t.Errorf("%s: More options offers %q for the agent's question", v.name, s)
				}
			}
		}
		if err := ga.w.Gateway.Decide(n.ID, gateway.AllowOnce, ""); err != nil {
			t.Fatal(err)
		}
	}

	var pay gateway.NeedView
	ga.waitFor(t, "the payment approval", func() (ok bool) {
		pay, ok = ga.need(func(n gateway.NeedView) bool { return isPayment(n) && n.Rule == "Payments" })
		return
	})
	if !pay.AllowTask {
		t.Fatalf("payment approval %+v", pay)
	}
	for _, v := range views {
		if texts := moreOptions(v.name, v.tt); !slices.Contains(texts, "Allow for this task") {
			t.Fatalf("%s: More options on the payment: %q", v.name, texts)
		}
	}
	panel.Frame()
	if err := panel.Click("More options"); err != nil {
		t.Fatal(err)
	}
	if err := panel.Click("Allow for this task"); err != nil {
		t.Fatal(err)
	}
	panel.Frame()
	for _, want := range []string{"Allowed for this task", "Payments in Dinely are allowed until this task ends."} {
		if !hasText(panel, want) {
			t.Errorf("no %q in the floating panel: %q", want, panel.Texts())
		}
	}
	if err := wait(t, done, "the agent"); err != nil {
		t.Fatalf("agent: %v", err)
	}
}

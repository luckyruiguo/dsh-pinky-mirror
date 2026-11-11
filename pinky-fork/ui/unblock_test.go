package ui

import (
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// TestUnblockCard: the card that asks whether an agent may open a blocked
// app says what allowing does and answers with Allow for this task or
// Deny, nothing else (field test #18).
func TestUnblockCard(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.waitFor(t, "the phones", func() bool { return len(ta.devices) > 0 })
	d := ta.devices[0]
	n := gateway.NeedView{ID: "ap-unblock", Kind: gateway.NeedApproval, DeviceID: d.Info.ID, DeviceName: d.Info.Name,
		App: "Wise", Agent: "Claude Code", Title: "Open Wise?", Detail: "Claude Code · Bookkeeping", Rule: "Finance apps",
		Unblock: true, AllowTask: true, Created: time.Now(), Deadline: time.Now().Add(time.Minute)}
	tt := mg.NewTester(func(c *mg.Context) { ta.needCard(c, n, false) }, 420, 640)
	tt.Frame()
	for _, want := range []string{"Open Wise?",
		"Wise is blocked by “Finance apps”. Allowing lets Claude Code open it and read its screen until this task ends.",
		"Deny", "Allow for this task"} {
		if !hasText(tt, want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	if hasText(tt, "More options") {
		t.Errorf("the card offers more: %q", tt.Texts())
	}
	if err := tt.Click("Allow for this task"); err != nil {
		t.Fatal(err)
	}
	if len(ta.answered) != 1 || ta.answered[0].d != gateway.AllowTask {
		t.Fatalf("answered %+v", ta.answered)
	}
	tt = mg.NewTester(func(c *mg.Context) { ta.panelAnswered(c, ta.answered[0]) }, 420, 400)
	tt.Frame()
	if !hasText(tt, "Allowed for this task") || !hasText(tt, "Claude Code can use Wise until this task ends.") {
		t.Errorf("the panel's answer: %q", tt.Texts())
	}
}

// TestNewAgentCardScopes: the first-connection card offers read-only first
// to an agent that asked for it, and says under the menu what the chosen
// scope lets the agent do (field test #19, #28).
func TestNewAgentCardScopes(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	asked := gateway.NeedView{ID: "ap-agent-1", Kind: gateway.NeedApproval, Rule: "New agent", Agent: "Bookkeeper",
		Title: "Let Bookkeeper use your phones?", ReadOnly: true,
		Detail: "Bookkeeper wants to connect for “Read the balances”. Allowing gives it read_only access."}
	tt := mg.NewTester(func(c *mg.Context) { ta.needCard(c, asked, false) }, 420, 640)
	tt.Frame()
	for _, want := range []string{"Bookkeeper asked to only browse and read.", "Read only: browse, never commit",
		"It can open apps, look around and search. Anything that would ask you is refused, and it types only into search fields."} {
		if !hasText(tt, want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	plain := gateway.NeedView{ID: "ap-agent-2", Kind: gateway.NeedApproval, Rule: "New agent", Agent: "Shopper",
		Title: "Let Shopper use your phones?", Detail: "Shopper wants to connect for “Lunch”. Allowing gives it everything access."}
	tt = mg.NewTester(func(c *mg.Context) { ta.needCard(c, plain, false) }, 420, 640)
	tt.Frame()
	if hasText(tt, "asked to only browse") || !hasText(tt, "All apps except blocked ones") ||
		!hasText(tt, "It can open any app except blocked ones. What Settings › Safety asks about still asks you.") {
		t.Errorf("an agent that did not ask: %q", tt.Texts())
	}
	for _, c := range scopeChoices {
		if scopeHint(c.mode) == "" {
			t.Errorf("no hint for %s", c.mode)
		}
	}
	inLanguage(t, "zh-Hans")
	if got := scopeLabel(gateway.ScopeReadOnly); got != "只读：可以浏览，不能提交" {
		t.Errorf("zh-Hans read-only label %q", got)
	}
}

// TestReadOnlyTaskCard says on the task card that the agent asked to run
// it read-only.
func TestReadOnlyTaskCard(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	task := gateway.TaskView{ID: "t1", DeviceID: "pixel-9", DeviceName: "Pixel 9", Agent: "Claude Code",
		Title: "Read the balances", State: gateway.TaskWorking, Started: time.Now(), ReadOnly: true}
	tt := mg.NewTester(func(c *mg.Context) { ta.taskCard(c, task) }, 420, 400)
	tt.Frame()
	if !hasText(tt, "Claude Code · Read only · Pixel 9") {
		t.Errorf("the task card reads %q", tt.Texts())
	}
}

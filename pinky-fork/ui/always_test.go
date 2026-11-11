package ui

import (
	"context"
	"slices"
	"strings"
	"testing"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
	"github.com/DIYgod/pinky/ui/demogw"
)

// TestGatewayAlwaysAllow answers the lunch payment with More options ›
// Always allow payments in Dinely: Settings › Safety lists it, the next
// lunch is paid without asking, and the list's × makes payments ask again.
func TestGatewayAlwaysAllow(t *testing.T) {
	ga := newGatewayApp(t, true)
	tt := mg.NewTester(ga.view, 1280, 820)
	done := ga.agent(t, "Claude Code", pixel, (*demogw.Agent).ReorderLunch)

	var pay gateway.NeedView
	ga.waitFor(t, "the payment approval", func() (ok bool) { pay, ok = ga.need(isPayment); return })
	if !pay.AlwaysAllow || pay.Rule != "Payments" || pay.App != "Dinely" {
		t.Fatalf("payment approval %+v", pay)
	}
	tt.Frame()
	if err := tt.Click("More options"); err != nil {
		t.Fatal(err)
	}
	if texts := tt.Texts(); !slices.Contains(texts, "Allow for this task") || !slices.Contains(texts, "Always allow payments in Dinely") {
		t.Fatalf("More options: %q", texts)
	}
	if err := tt.Click("Always allow payments in Dinely"); err != nil {
		t.Fatal(err)
	}
	if err := wait(t, done, "the first lunch"); err != nil {
		t.Fatalf("agent: %v", err)
	}
	if got := ga.w.Gateway.Exemptions(); len(got) != 1 || got[0].Rule != "Payments" || got[0].AppName != "Dinely" {
		t.Fatalf("exemptions %+v", got)
	}

	// Settings › Safety lists it.
	ga.page, ga.set.sec = pageSettings, secSafety
	ga.waitFor(t, "Always allowed", func() bool { tt.Frame(); return ga.always.loaded && len(ga.always.list) == 1 })
	tt.Frame()
	for _, want := range []string{"Always allowed", "Dinely", "Payments"} {
		if !hasText(tt, want) {
			t.Errorf("no %q in Settings › Safety: %q", want, tt.Texts())
		}
	}
	if _, ok := tt.Find("Ask again about payments in Dinely"); !ok {
		t.Fatalf("no button to take it back: %q", tt.Texts())
	}

	// The next lunch is paid without asking. (The agent goes on from where
	// the phone is: on the screen of the order placed, it would just stop.)
	ga.w.Pixel.Show("home")
	done = ga.agent(t, "Claude Code", pixel, (*demogw.Agent).ReorderLunch)
	ga.waitFor(t, "the second lunch", func() bool {
		if n, ok := ga.need(isPayment); ok {
			t.Fatalf("the second payment asked: %+v", n)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("agent: %v", err)
			}
			return true
		default:
			return false
		}
	})
	if !paid(ga.w.Gateway.Tasks()) {
		t.Fatalf("the second lunch was not paid: %+v", ga.w.Gateway.Tasks())
	}

	// Taken back, payments in Dinely ask again.
	tt.Frame()
	if err := tt.Click("Ask again about payments in Dinely"); err != nil {
		t.Fatal(err)
	}
	ga.waitFor(t, "the answer taken back", func() bool { return len(ga.w.Gateway.Exemptions()) == 0 })
	ga.waitFor(t, "the list to empty", func() bool { tt.Frame(); return len(ga.always.list) == 0 })
	tt.Frame()
	if !hasText(tt, "None yet.") || hasText(tt, "Ask again about") {
		t.Fatalf("Settings › Safety after taking it back: %q", tt.Texts())
	}
	ga.w.Pixel.Show("home")
	done = ga.agent(t, "Claude Code", pixel, (*demogw.Agent).ReorderLunch)
	ga.waitFor(t, "the third payment to ask", func() (ok bool) { pay, ok = ga.need(isPayment); return })
	ga.w.Gateway.Decide(pay.ID, gateway.AllowOnce, "")
	if err := wait(t, done, "the third lunch"); err != nil {
		t.Fatalf("agent: %v", err)
	}
}

// paid reports whether the last task tapped Place order, and it went
// through without waiting for the person.
func paid(tasks []gateway.TaskView) bool {
	if len(tasks) == 0 {
		return false
	}
	for _, st := range tasks[len(tasks)-1].Steps {
		if first, _ := splitStep(st.Text); first == "Tapped “Place order · $12.80”" {
			return st.Status == "settled"
		}
		if strings.HasPrefix(st.Text, "Waiting for you") {
			return false
		}
	}
	return false
}

// TestGatewayAlwaysAllowedStep: a lunch paid on an earlier Always allow
// says so under the payment's step, on the task card and in Activity, in
// English and in Chinese.
func TestGatewayAlwaysAllowedStep(t *testing.T) {
	ga := newGatewayApp(t, true)
	if _, err := ga.w.Gateway.Policy().Exempt([]string{"Payments", "Commits"}, screen.App{Name: "Dinely", ID: demo.Dinely}); err != nil {
		t.Fatal(err)
	}
	tt := mg.NewTester(ga.view, 1280, 820)
	done := ga.agent(t, "Claude Code", pixel, (*demogw.Agent).ReorderLunch)
	if err := wait(t, done, "the lunch"); err != nil {
		t.Fatalf("agent: %v", err)
	}
	if !paid(ga.w.Gateway.Tasks()) {
		t.Fatalf("the lunch was not paid without asking: %+v", ga.w.Gateway.Tasks())
	}
	zh := locales.In("zh-Hans")
	en := []string{"Tapped “Place order · $12.80”", "Always allowed: “Payments” in Dinely"}
	cn := []string{zh.GwDidTap("Place order · $12.80"), zh.GwStepAlwaysAllowed(zh.RulePayments(), "Dinely")}
	check := func(where string, want []string, not string) {
		t.Helper()
		for _, s := range want {
			if !hasText(tt, s) {
				t.Errorf("%s: no %q in %q", where, s, tt.Texts())
			}
		}
		if not != "" && hasText(tt, not) {
			t.Errorf("%s: %q shows: %q", where, not, tt.Texts())
		}
	}

	ga.page, ga.sel = pagePhones, pixel
	ga.waitFor(t, "the finished task", func() bool {
		task := ga.taskOf(pixel)
		return task != nil && !running(task.State)
	})
	tt.Frame()
	check("the task card", en, "")
	ga.page = pageActivity
	ga.waitFor(t, "Activity", func() bool { tt.Frame(); return ga.act.loaded && len(ga.act.tasks) > 0 })
	ga.act.sel = ga.act.tasks[0].ID
	tt.Frame()
	check("Activity", en, "")

	inLanguage(t, "zh-Hans", tt)
	tt.Frame()
	check("Activity in Chinese", cn, "Always allowed")
	ga.page = pagePhones
	tt.Frame()
	check("the task card in Chinese", cn, "Always allowed")
}

// TestGatewayAlwaysAllowNotOffered: while every action asks, More options
// has no Always allow, and Settings says the answers wait.
func TestGatewayAlwaysAllowNotOffered(t *testing.T) {
	ga := newGatewayApp(t, true)
	g := ga.w.Gateway
	// An answer from before, in another app.
	if _, err := g.Policy().Exempt([]string{"Messages"}, screen.App{Name: "Chatter", ID: demo.Chatter}); err != nil {
		t.Fatal(err)
	}
	if err := g.Policy().SetPreset(policy.PresetEvery); err != nil {
		t.Fatal(err)
	}
	ga.settings.Preset = presetEvery
	tt := mg.NewTester(ga.view, 1280, 820)
	done := ga.agent(t, "Claude Code", pixel, func(ag *demogw.Agent, ctx context.Context) error {
		if err := ag.Connect(ctx, "Open Dinely"); err != nil {
			return err
		}
		return ag.Act(ctx, "Open Dinely", gateway.Action{Tool: "launch", App: "Dinely"})
	})
	var n gateway.NeedView
	ga.waitFor(t, "the launch approval", func() (ok bool) { n, ok = ga.need(isPayment); return })
	if n.AlwaysAllow || n.Rule != policy.RuleEvery {
		t.Fatalf("approval %+v", n)
	}
	tt.Frame()
	if err := tt.Click("More options"); err != nil {
		t.Fatal(err)
	}
	texts := tt.Texts()
	if !slices.Contains(texts, "Allow for this task") {
		t.Fatalf("More options: %q", texts)
	}
	for _, s := range texts {
		if strings.HasPrefix(s, "Always allow") {
			t.Fatalf("More options offers %q while every action asks", s)
		}
	}
	if err := tt.Click("Allow for this task"); err != nil {
		t.Fatal(err)
	}
	if err := wait(t, done, "the agent"); err != nil {
		t.Fatalf("agent: %v", err)
	}

	ga.page, ga.set.sec = pageSettings, secSafety
	ga.waitFor(t, "Always allowed", func() bool { tt.Frame(); return ga.always.loaded })
	tt.Frame()
	for _, want := range []string{"Chatter", "Messages", "While agents ask before every action, these ask too."} {
		if !hasText(tt, want) {
			t.Errorf("no %q in Settings › Safety: %q", want, tt.Texts())
		}
	}
}

// Always allow on changing the phone's settings, reading an app's logs or
// installing an app is for one agent on one phone: the menu item, the panel's confirmation
// and Settings › Safety say whom and where.
func TestAlwaysAllowPerAgentAndPhone(t *testing.T) {
	settings := gateway.NeedView{Rule: "Phone settings", Agent: "Claude Code", DeviceName: "Pixel 9"}
	logs := gateway.NeedView{Rule: "App logs", Agent: "Claude Code", DeviceName: "Pixel 9", App: "Crash Test"}
	install := gateway.NeedView{Rule: "Installing from the computer", Agent: "Claude Code", DeviceName: "Pixel 9", App: "Tool"}
	for n, want := range map[*gateway.NeedView][2]string{
		&settings: {"Always allow Claude Code to change settings on Pixel 9",
			"Claude Code no longer asks to change settings on Pixel 9. Pinky still puts them back when each task ends. Change it in Settings."},
		&logs: {"Always allow Claude Code to read the logs of Crash Test on Pixel 9",
			"Claude Code no longer asks to read the logs of Crash Test on Pixel 9. Change it in Settings."},
		&install: {"Always allow Claude Code to install Tool from this signer on Pixel 9",
			"Claude Code no longer asks to install builds of Tool from the same signer on Pixel 9. Another signer or another app still asks. Change it in Settings."},
	} {
		if got := alwaysAllowLabel(*n); got != want[0] {
			t.Errorf("label %q, want %q", got, want[0])
		}
		if got := alwaysAllowedDetail(*n); got != want[1] {
			t.Errorf("detail %q, want %q", got, want[1])
		}
	}
	if got := alwaysAllowedDetail(gateway.NeedView{Rule: "Payments", App: "Dinely"}); got != "" {
		t.Errorf("payments detail %q", got)
	}

	for _, c := range []struct {
		x                 gateway.Exemption
		name, sub, remove string
	}{
		{gateway.Exemption{Rule: "Payments", AppName: "Dinely"}, "Dinely", "", "Ask again about payments in Dinely"},
		{gateway.Exemption{Rule: "Phone settings", Agent: "Claude Code", DeviceID: "pixel", DeviceName: "Pixel 9"},
			"Pixel 9", "Claude Code", "Ask again about changing phone settings by Claude Code on Pixel 9"},
		{gateway.Exemption{Rule: "App logs", AppID: "dev.pinky.crashtest", AppName: "Crash Test", Agent: "Claude Code", DeviceID: "pixel"},
			"Crash Test", "Claude Code on pixel", "Ask again about reading the logs of Crash Test by Claude Code on pixel"},
		{gateway.Exemption{Rule: "Installing from the computer", AppID: "com.example.tool", AppName: "Tool", Agent: "Claude Code",
			DeviceID: "pixel", DeviceName: "Pixel 9", Signer: "ab12cd34ef", SignerName: "Example Dev (ab12cd34)"},
			"Tool", "Claude Code on Pixel 9 · signed by Example Dev (ab12cd34)", "Ask again about installing Tool by Claude Code on Pixel 9"},
	} {
		name, sub, remove := alwaysRow(c.x)
		if name != c.name || sub != c.sub || remove != c.remove {
			t.Errorf("%+v: %q %q %q", c.x, name, sub, remove)
		}
	}
}

package policy

import (
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

// TestLogsRefused: the logs of blocked apps, finance apps, password
// managers and the phone's own system apps never reach agents, even with
// the rules that block opening them turned off; an ordinary app's do.
func TestLogsRefused(t *testing.T) {
	e := Default()
	e.AddApps(CatBlocked, CategoryEntries{IDs: []string{"com.example.diary"}})
	cases := []struct {
		app  screen.App
		rule string
	}{
		{screen.App{ID: "com.example.mybank", Name: "Example Bank"}, "Finance apps"},
		{screen.App{ID: "com.onepassword.android", Name: "1Password"}, "Password managers"},
		{screen.App{ID: "com.example.diary", Name: "Diary"}, RuleBlocked},
		{screen.App{ID: "com.android.settings", Name: "Settings"}, RuleAppLogs},
		{screen.App{ID: "com.android.systemui", Name: "System UI"}, RuleAppLogs},
		{screen.App{ID: "com.google.android.dialer", Name: "Phone"}, RuleAppLogs},
		{screen.App{ID: "dev.pinky.crashtest", Name: "Crash Test"}, ""},
	}
	for _, c := range cases {
		rule, refused := e.LogsRefused(c.app)
		if refused != (c.rule != "") || rule != c.rule {
			t.Errorf("%s: refused %v by %q, want %q", c.app.ID, refused, rule, c.rule)
		}
	}

	// Without any rules, money and passwords stay out of reach.
	bare := New(File{}, nil)
	for _, c := range cases[:2] {
		if rule, refused := bare.LogsRefused(c.app); !refused || rule != c.rule {
			t.Errorf("no rules, %s: refused %v by %q", c.app.ID, refused, rule)
		}
	}
}

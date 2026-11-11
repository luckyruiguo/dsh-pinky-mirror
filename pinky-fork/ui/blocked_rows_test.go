package ui

import (
	"testing"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// Two blocked apps share a name, "Trust": Trust Bank's iPhone app and
// Trust Wallet's Android app. Their rows in Settings › Safety have keys of
// their own (with one key the Tester panics, and the app's two remove
// buttons shared one state: one click removed both rows, the second past
// the end of the list, and Pinky quit), and removing one unblocks that app
// alone.
func TestBlockedAppsShareAName(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	var reported [][2]Settings
	ta.opt.OnSettings = func(old, s Settings) { reported = append(reported, [2]Settings{old, s}) }
	rows := []BlockedApp{
		{Name: "Trust", ID: "com.sixdays.trust", Kind: "Finance"},
		{Name: "Trust", ID: "sg.trust", Kind: "Finance"},
	}
	ta.settings.BlockedApps = append([]BlockedApp(nil), rows...)
	ta.reported.BlockedApps = append([]BlockedApp(nil), rows...)
	ta.page, ta.set.sec = pageSettings, secSafety
	tt := mg.NewTester(ta.view, 1280, 2600)
	tt.Frame()

	// Click finds the first remove button of that label: the first row's.
	if err := tt.Click(locales.SettingsSafetyUnblockApp("Trust")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := ta.settings.BlockedApps; len(got) != 1 || got[0].ID != "sg.trust" {
		t.Fatalf("after removing the first Trust: %+v", got)
	}
	if len(reported) == 0 {
		t.Fatal("the change was not reported")
	}
	if last := reported[len(reported)-1][1].BlockedApps; len(last) != 1 || last[0].ID != "sg.trust" {
		t.Fatalf("reported %+v", last)
	}
}

func TestWithoutBlocked(t *testing.T) {
	list := []BlockedApp{{Name: "Trust", ID: "a"}, {Name: "Trust", ID: "b"}, {Name: "Trust"}}
	got := withoutBlocked(list, BlockedApp{Name: "Trust", ID: "b"})
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "" {
		t.Fatalf("%+v", got)
	}
}

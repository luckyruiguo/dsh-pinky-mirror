package gateway

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
)

// TestMatchAppByID finds apps by English names on a phone in another
// language, through their package or bundle ids.
func TestMatchAppByID(t *testing.T) {
	apps := []driver.App{
		{ID: "com.google.android.calculator", Name: "计算器", System: true},
		{ID: "com.google.android.deskclock", Name: "时钟", System: true},
		{ID: "com.android.settings", Name: "设置", System: true},
		{ID: "com.apple.mobilecal", Name: "日历", System: true},
		{ID: "com.example.cal", Name: "Cal"},
	}
	for q, want := range map[string]string{
		"Calculator": "com.google.android.calculator",
		"Clock":      "com.google.android.deskclock",
		"Settings":   "com.android.settings",
		"Cal":        "com.example.cal", // a name that matches wins
		"设置":         "com.android.settings",
	} {
		got, ok := matchApp(apps, q)
		if !ok || got.ID != want {
			t.Errorf("matchApp(%q) = %q, %v; want %q", q, got.ID, ok, want)
		}
	}
	if _, ok := matchApp(apps, "Maps"); ok {
		t.Error("matched an app the phone lacks")
	}
}

// A task app list with a name no installed app has (天气 for an English
// Weather app) is pointed out on connect, and connecting again with the
// same task takes the corrected list.
func TestConnectPointsOutUnknownTaskApps(t *testing.T) {
	g, _ := newTestGateway(t)
	s := g.OpenSession(agent)
	t.Cleanup(s.Close)
	ctx := context.Background()
	res := s.Connect(ctx, ConnectRequest{Task: "Lunch", Apps: []string{"天气", "Dinely"}})
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Message, "“天气”") || strings.Contains(res.Message, "“Dinely”") {
		t.Fatalf("connect message: %s", res.Message)
	}
	res = s.Connect(ctx, ConnectRequest{Task: "Lunch", Apps: []string{"Dinely"}})
	mustStatus(t, res, StatusSettled)
	if strings.Contains(res.Message, "No installed app matches") {
		t.Fatalf("still warned: %s", res.Message)
	}
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "com.dinely.app"}), StatusSettled)
}

// GitHub issue #7: on an iPhone WeChat's bundle is named WeChat, and 微信
// is part of 微信读书's name. A name finds an app only for sure: its id, its
// name or an alias (the home screen's name), or a well-known name; an app
// whose name merely holds it is offered as similar, never picked.
func TestFindAppDoesNotGuess(t *testing.T) {
	wechat := driver.App{ID: "com.tencent.xin", Name: "WeChat", Aliases: []string{"微信"}}
	weread := driver.App{ID: "com.tencent.weread", Name: "微信读书"}
	maps := driver.App{ID: "com.google.Maps", Name: "Google Maps"}
	for _, c := range []struct {
		apps    []driver.App
		q, want string
		similar []string
	}{
		{apps: []driver.App{weread, wechat}, q: "微信", want: "com.tencent.xin"},
		{apps: []driver.App{weread, wechat}, q: "WeChat", want: "com.tencent.xin"},
		{apps: []driver.App{weread, wechat}, q: "微信读书", want: "com.tencent.weread"},
		// Without the home screen's name: the well-known name.
		{apps: []driver.App{weread, {ID: "com.tencent.xin", Name: "WeChat"}}, q: "微信", want: "com.tencent.xin"},
		// Android: the label is 微信 already.
		{apps: []driver.App{{ID: "com.tencent.mm", Name: "微信"}, {ID: "com.tencent.weread", Name: "微信读书"}}, q: "微信", want: "com.tencent.mm"},
		// No WeChat: WeRead is only similar.
		{apps: []driver.App{weread}, q: "微信", similar: []string{"com.tencent.weread"}},
		{apps: []driver.App{weread, maps}, q: "Maps", similar: []string{"com.google.Maps"}},
		{apps: []driver.App{weread, maps}, q: "com.google.Maps", want: "com.google.Maps"},
		// Two apps of one name: the user's, else both are offered.
		{apps: []driver.App{{ID: "a.notes", Name: "Notes", System: true}, {ID: "b.notes", Name: "Notes"}}, q: "notes", want: "b.notes"},
		{apps: []driver.App{{ID: "a.notes", Name: "Notes"}, {ID: "b.notes", Name: "Notes"}}, q: "Notes", similar: []string{"a.notes", "b.notes"}},
	} {
		m := findApp(c.apps, c.q)
		var similar []string
		for _, a := range m.similar {
			similar = append(similar, a.ID)
		}
		if m.app.ID != c.want || m.ok != (c.want != "") || strings.Join(similar, ",") != strings.Join(c.similar, ",") {
			t.Errorf("findApp(%q) = %q %v, similar %v; want %q, similar %v", c.q, m.app.ID, m.ok, similar, c.want, c.similar)
		}
	}
	if got := appList([]driver.App{wechat, weread}); got != "WeChat · 微信 (com.tencent.xin), 微信读书 (com.tencent.weread)" {
		t.Errorf("appList = %q", got)
	}
}

// appsDev is a demo phone with another list of apps.
type appsDev struct {
	*demo.Device
	apps []driver.App
}

func (d *appsDev) Apps(context.Context) ([]driver.App, error) { return d.apps, nil }

// The task's app list takes WeChat for 微信, and says which apps are only
// similar when it cannot tell; phone_launch does the same.
func TestTaskAppsDoNotGuess(t *testing.T) {
	ctx := context.Background()
	base := demo.NewDevice("iphone", "iPhone 16 Pro", driver.IOS)
	weread := driver.App{ID: "com.tencent.weread", Name: "微信读书"}
	wechat := driver.App{ID: "com.tencent.xin", Name: "WeChat", Aliases: []string{"微信"}}
	g := newDevGateway(t, base, &appsDev{base, []driver.App{weread, wechat}})
	s := g.OpenSession(agent)
	t.Cleanup(s.Close)
	res := s.Connect(ctx, ConnectRequest{Task: "Send a message", Apps: []string{"微信"}})
	mustStatus(t, res, StatusSettled)
	if strings.Contains(res.Message, "No installed app") {
		t.Fatalf("connect: %s", res.Message)
	}
	g.mu.Lock()
	var apps []string
	for _, l := range s.leases {
		apps = l.apps
	}
	g.mu.Unlock()
	if !slices.Contains(apps, "com.tencent.xin") || slices.Contains(apps, "com.tencent.weread") {
		t.Fatalf("task apps = %v", apps)
	}
	s.Release(ctx, ReleaseRequest{})

	// A phone without WeChat: WeRead is only offered.
	base2 := demo.NewDevice("iphone", "iPhone 16 Pro", driver.IOS)
	g2 := newDevGateway(t, base2, &appsDev{base2, []driver.App{weread}})
	s2 := g2.OpenSession(agent)
	t.Cleanup(s2.Close)
	res = s2.Connect(ctx, ConnectRequest{Task: "Send a message", Apps: []string{"微信"}})
	if !strings.Contains(res.Message, "No installed app matches “微信”") || !strings.Contains(res.Message, "Similar names for “微信”: 微信读书 (com.tencent.weread)") {
		t.Fatalf("connect: %s", res.Message)
	}
	res = s2.Act(ctx, Call{}, Action{Tool: "launch", App: "微信"})
	if res.Error != ErrNotFound || !strings.Contains(res.Message, "Similar: 微信读书 (com.tencent.weread)") {
		t.Fatalf("launch: %s %s", res.Error, res.Message)
	}
}

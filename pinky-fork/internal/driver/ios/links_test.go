package ios

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
	goios "github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/installationproxy"
)

func TestLinkScheme(t *testing.T) {
	for link, want := range map[string]string{
		"https://example.com/a?b=c": "https",
		"  HTTP://example.com  ":    "http",
		"weixin://dl/scan":          "weixin",
		"tel:+15551234":             "tel",
		"MyApp://item/42":           "myapp",
	} {
		if got, err := linkScheme(link); got != want || err != nil {
			t.Errorf("linkScheme(%q) = %q, %v; want %q", link, got, err, want)
		}
	}
	for _, bad := range []string{"", "https://", "https:///path", "Settings", "://x", "https://a b", "https://a\nb", "myapp://a\tb"} {
		if _, err := linkScheme(bad); !errors.Is(err, driver.ErrUnsupported) {
			t.Errorf("linkScheme(%q): %v", bad, err)
		}
	}
}

// appInfo is an app as the installation proxy lists it.
func appInfo(id, name, typ string, schemes ...string) installationproxy.AppInfo {
	a := installationproxy.AppInfo{"CFBundleIdentifier": id, "CFBundleDisplayName": name, "ApplicationType": typ, "Path": "/var/containers/Bundle/Application/X/" + id + ".app"}
	if len(schemes) > 0 {
		list := make([]any, len(schemes))
		for i, s := range schemes {
			list[i] = s
		}
		a["CFBundleURLTypes"] = []any{map[string]any{"CFBundleURLName": id, "CFBundleURLSchemes": list}}
	}
	return a
}

func testAppList() appList {
	hidden := appInfo("com.apple.helper", "Helper", "System", "helper")
	hidden["SBAppTags"] = []any{"hidden"}
	return readAppInfos([]installationproxy.AppInfo{
		appInfo(safariID, "Safari", "System", "http", "https", "x-web-search"),
		appInfo("com.apple.MobileSMS", "Messages", "System", "sms", "imessage"),
		appInfo("com.tencent.xin", "WeChat", "User", "weixin", "wechat", "WeiXin"),
		appInfo("com.example.dinely", "Dinely", "User", "dinely"),
		appInfo("com.example.copy", "Copycat", "User", "dinely", "sms"),
		appInfo("com.example.other", "Other", "User", "shared"),
		appInfo("com.example.another", "Another", "User", "shared"),
		hidden,
	}, map[string]string{"com.tencent.xin": "微信"})
}

func TestReadAppInfos(t *testing.T) {
	l := testAppList()
	if len(l.apps) != 7 {
		t.Errorf("apps %+v", l.apps)
	}
	if got := l.schemes["weixin"]; len(got) != 1 || got[0].ID != "com.tencent.xin" || len(got[0].Aliases) != 1 || got[0].Aliases[0] != "微信" {
		t.Errorf("weixin: %+v", got)
	}
	if got := l.schemes["sms"]; len(got) != 2 || got[0].ID != "com.apple.MobileSMS" || got[1].ID != "com.example.copy" {
		t.Errorf("sms: %+v", got)
	}
	if got := l.schemes["helper"]; len(got) != 1 || got[0].ID != "com.apple.helper" {
		t.Errorf("a hidden app's scheme: %+v", got)
	}
}

func TestPickSchemeApp(t *testing.T) {
	l := testAppList()
	for _, c := range []struct {
		link, want string
		err        error
		choice     int
	}{
		{"weixin://dl/scan", "com.tencent.xin", nil, 0},
		{"dinely://order/42", "", nil, 2}, // two apps declare it: iOS picks either
		{"sms:+15551234", "com.apple.MobileSMS", nil, 0},
		{"shared://x", "", nil, 2},
		{"nothing://x", "", driver.ErrNotFound, 0},
	} {
		scheme, _ := linkScheme(c.link)
		app, err := pickSchemeApp(c.link, l.schemes[scheme])
		var choice *driver.LinkChoiceError
		switch {
		case c.choice > 0:
			if !errors.As(err, &choice) || len(choice.Apps) != c.choice || choice.Link != c.link {
				t.Errorf("%s: %+v %v, want a choice of %d", c.link, app, err, c.choice)
			}
		case c.err != nil:
			if !errors.Is(err, c.err) {
				t.Errorf("%s: %+v %v, want %v", c.link, app, err, c.err)
			}
		case err != nil || app.ID != c.want:
			t.Errorf("%s: %+v %v, want %s", c.link, app, err, c.want)
		}
	}
}

func TestWDALinks(t *testing.T) {
	f := &fakeWDA{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	ctx := context.Background()
	d := &WDADevice{c: newWDAURL(srv.URL, ""), entry: goios.DeviceEntry{DeviceID: 7}}
	d.apps = &appCatalog{}
	d.apps.set(testAppList())

	app, err := d.LinkApp(ctx, "https://example.com/a")
	if err != nil || app.ID != safariID {
		t.Fatalf("https: %+v %v", app, err)
	}
	app, err = d.LinkApp(ctx, "weixin://dl/scan")
	if err != nil || app.ID != "com.tencent.xin" {
		t.Fatalf("weixin: %+v %v", app, err)
	}
	if err := d.OpenLink(ctx, " weixin://dl/scan ", app); err != nil {
		t.Fatal(err)
	}
	if err := d.OpenLink(ctx, "https://example.com/a", safari); err != nil {
		t.Fatal(err)
	}
	var sent []map[string]string
	for _, b := range f.bodies["POST /url"] {
		var m map[string]string
		json.Unmarshal([]byte(b), &m)
		sent = append(sent, m)
	}
	if len(sent) != 2 || sent[0]["url"] != "weixin://dl/scan" || sent[0]["bundleId"] != "com.tencent.xin" || sent[1]["bundleId"] != safariID {
		t.Fatalf("sent %v", sent)
	}
	// Not the app that opens the link: nothing is sent.
	if err := d.OpenLink(ctx, "weixin://dl/scan", safari); !errors.Is(err, driver.ErrNotFound) {
		t.Errorf("wrong app: %v", err)
	}
	if err := d.OpenLink(ctx, "dinely://order/42", driver.App{ID: "com.example.dinely"}); err == nil {
		t.Error("a link two apps declare opened")
	}
	// A WebDriverAgent that would ignore the app opens nothing.
	for _, v := range []string{"4.10.24", "none"} {
		f.mu.Lock()
		f.version = v
		f.mu.Unlock()
		if err := d.OpenLink(ctx, "https://example.com/a", safari); !errors.Is(err, driver.ErrUnsupported) {
			t.Errorf("WebDriverAgent %q: %v", v, err)
		}
	}
	if n := len(f.bodies["POST /url"]); n != 2 {
		t.Errorf("%d links sent, want 2", n)
	}

	// A simulator opens web links only, unless told which apps declare which
	// schemes.
	sim := &WDADevice{c: newWDAURL(srv.URL, "")}
	if _, err := sim.LinkApp(ctx, "maps://?q=Paris"); !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("simulator: %v", err)
	}
	maps := driver.App{ID: "com.apple.Maps", Name: "Maps", System: true}
	sim.SetLinkSchemes(func(context.Context) (map[string][]driver.App, error) {
		return map[string][]driver.App{"maps": {maps}}, nil
	})
	if app, err := sim.LinkApp(ctx, "maps://?q=Paris"); err != nil || app.ID != maps.ID {
		t.Errorf("simulator with schemes: %+v %v", app, err)
	}
}

// TestDeviceLinkApp checks the CoreDevice path's choice of app from its
// catalog.
func TestDeviceLinkApp(t *testing.T) {
	d := &Device{apps: &appCatalog{}}
	d.apps.set(testAppList())
	ctx := context.Background()
	if app, err := d.LinkApp(ctx, "HTTPS://example.com"); err != nil || app.ID != safariID {
		t.Errorf("https: %+v %v", app, err)
	}
	if app, err := d.LinkApp(ctx, "weixin://dl/scan"); err != nil || app.Name != "WeChat" {
		t.Errorf("weixin: %+v %v", app, err)
	}
	if _, err := d.LinkApp(ctx, "javascript alert"); !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("not a link: %v", err)
	}
	// Opening checks the app before anything is sent to the phone.
	if err := d.OpenLink(ctx, "weixin://dl/scan", safari); !errors.Is(err, driver.ErrNotFound) {
		t.Errorf("wrong app: %v", err)
	}
}

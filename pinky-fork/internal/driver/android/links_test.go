package android

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

const (
	chromeView  = "com.android.chrome/com.google.android.apps.chrome.IntentDispatcher"
	firefoxView = "org.mozilla.firefox/org.mozilla.fenix.IntentReceiverActivity"
	wechatView  = "com.tencent.mm/com.tencent.mm.plugin.base.stub.WXCustomSchemeEntryActivity"
	resolver    = "android/com.android.internal.app.ResolverActivity"
)

// briefResolve and briefQuery are what `cmd package resolve-activity
// --brief` and `query-activities --brief` print.
func briefResolve(c string) string {
	if c == "" {
		return "No activity found\n"
	}
	return "priority=0 preferredOrder=0 match=0x208000 specificIndex=-1 isDefault=true\n" + c + "\n"
}

func briefQuery(cs ...string) string {
	if len(cs) == 0 {
		return "No activities found\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d activities found:\n", len(cs))
	for i, c := range cs {
		fmt.Fprintf(&b, "  Activity #%d:\n    priority=0 preferredOrder=0 match=0x208000 specificIndex=-1 isDefault=false\n    %s\n", i, c)
	}
	return b.String()
}

func TestParseLinkQuery(t *testing.T) {
	out := briefResolve(resolver) + candidatesSection + "\n" + briefQuery(chromeView, "org.mozilla.firefox/.App", chromeView)
	resolved, cands, err := parseLinkQuery(out)
	if err != nil || resolved != resolver || strings.Join(cands, " ") != chromeView+" org.mozilla.firefox/org.mozilla.firefox.App" {
		t.Errorf("got %q %q %v", resolved, cands, err)
	}
	resolved, cands, err = parseLinkQuery(briefResolve("") + candidatesSection + "\n" + briefQuery())
	if err != nil || resolved != "" || len(cands) != 0 {
		t.Errorf("none: %q %q %v", resolved, cands, err)
	}
	for _, bad := range []string{
		"/system/bin/sh: cmd: not found\n" + candidatesSection + "\n" + briefQuery(),
		briefResolve(chromeView) + candidatesSection + "\nError: unknown command 'query-activities'\n",
		"cmd: Can't find service: package\n",
	} {
		if _, _, err := parseLinkQuery(bad); !errors.Is(err, driver.ErrUnsupported) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestPickLinkHandler(t *testing.T) {
	for _, c := range []struct {
		name       string
		resolved   string
		candidates []string
		pkg        string
		offered    string
	}{
		{"the person's default browser", chromeView, []string{firefoxView, chromeView}, "com.android.chrome", ""},
		{"the only app", resolver, []string{chromeView}, "com.android.chrome", ""},
		{"one app with two activities", resolver, []string{chromeView, "com.android.chrome/.Other"}, "com.android.chrome", ""},
		{"the phone would ask", resolver, []string{chromeView, firefoxView}, "", "com.android.chrome org.mozilla.firefox"},
		{"a maker's resolver", "com.huawei.android.internal.app/.HwResolverActivity", []string{chromeView, firefoxView}, "", "com.android.chrome org.mozilla.firefox"},
		{"resolved to an app that cannot open it", "com.evil/.Thief", []string{chromeView, firefoxView}, "", "com.android.chrome org.mozilla.firefox"},
		{"no app", "", nil, "", ""},
		{"resolved to nothing that can open it", chromeView, nil, "", ""},
	} {
		pkg, offered := pickLinkHandler(c.resolved, c.candidates)
		if pkg != c.pkg || strings.Join(offered, " ") != c.offered {
			t.Errorf("%s: got %q %q, want %q %q", c.name, pkg, offered, c.pkg, c.offered)
		}
	}
}

// linkPhone is clonePhone with apps that open links: Chrome and Firefox
// for web links (Chrome the default), WeChat and its clone for weixin://.
type linkPhone struct {
	*clonePhone
	defaultBrowser string
}

var linkQueryRE = regexp.MustCompile(`^cmd package resolve-activity --brief --user (\S+) .* -d (.+?) 2>&1; echo`)

func (p *linkPhone) run(serial, service string) (string, bool) {
	cmd, _ := strings.CutPrefix(service, "shell:")
	m := linkQueryRE.FindStringSubmatch(cmd)
	if m == nil {
		return p.clonePhone.run(serial, service)
	}
	p.mu.Lock()
	p.cmds = append(p.cmds, cmd)
	browser := p.defaultBrowser
	p.mu.Unlock()
	user, link := m[1], strings.ReplaceAll(strings.Trim(m[2], "'"), `'\''`, "'")
	switch {
	case strings.HasPrefix(link, "https://"):
		if browser == "" {
			browser = resolver
		}
		return briefResolve(browser) + candidatesSection + "\n" + briefQuery(chromeView, firefoxView), true
	case strings.HasPrefix(link, "weixin://"):
		return briefResolve(wechatView) + candidatesSection + "\n" + briefQuery(wechatView), true
	case strings.HasPrefix(link, "only999://") && user == "999":
		return briefResolve(wechatView) + candidatesSection + "\n" + briefQuery(wechatView), true
	}
	return briefResolve("") + candidatesSection + "\n" + briefQuery(), true
}

func newLinkADB(t *testing.T, p *linkPhone) *adb.Client {
	t.Helper()
	f := startFakeADB(t)
	f.set(func(f *fakeADB) {
		f.props = map[string]string{"phone": ""}
		f.run = p.run
	})
	return f.config().Client
}

func TestLinks(t *testing.T) {
	ctx := context.Background()
	p := &linkPhone{clonePhone: newClonePhone(), defaultBrowser: chromeView}
	d := newCloneDevice(t, p.clonePhone)
	d.client = newLinkADB(t, p)

	app, err := d.LinkApp(ctx, "https://example.com/a?b=c&d='e'")
	if err != nil || app.ID != "com.android.chrome" {
		t.Fatalf("https: %+v %v", app, err)
	}
	app, err = d.LinkApp(ctx, "weixin://dl/scan")
	if err != nil || app.ID != "com.tencent.mm" || app.Name != "微信" {
		t.Fatalf("weixin: %+v %v", app, err)
	}
	if _, err := d.LinkApp(ctx, "nothing://x"); !errors.Is(err, driver.ErrNotFound) {
		t.Errorf("no app: %v", err)
	}
	p.mu.Lock()
	p.defaultBrowser = ""
	p.mu.Unlock()
	var choice *driver.LinkChoiceError
	if _, err := d.LinkApp(ctx, "https://example.com"); !errors.As(err, &choice) || len(choice.Apps) != 2 || choice.Apps[0].ID != "com.android.chrome" {
		t.Errorf("choice: %v", err)
	}
	if _, err := d.LinkApp(ctx, "https://a\nb"); !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("a newline: %v", err)
	}

	// Opening starts the app's own activity, by name, in the app's user.
	if err := d.OpenLink(ctx, "https://example.com/a?b=c&d='e'", driver.App{ID: "com.android.chrome"}); err != nil {
		t.Fatal(err)
	}
	if err := d.OpenLink(ctx, "weixin://dl/scan", driver.App{ID: "com.tencent.mm@999"}); err != nil {
		t.Fatal(err)
	}
	b := &Background{d: d, display: 7, closed: make(chan struct{})}
	if err := b.OpenLink(ctx, "weixin://dl/scan", driver.App{ID: "com.tencent.mm"}); err != nil {
		t.Fatal(err)
	}
	if b.launch.pkg != "com.tencent.mm" || !b.started.Load() {
		t.Errorf("background display's launch = %+v", b.launch)
	}
	// An app that does not open the link: nothing starts.
	if err := d.OpenLink(ctx, "weixin://dl/scan", driver.App{ID: "com.android.chrome"}); !errors.Is(err, driver.ErrNotFound) {
		t.Errorf("wrong app: %v", err)
	}
	if err := d.OpenLink(ctx, "only999://x", driver.App{ID: "com.tencent.mm"}); !errors.Is(err, driver.ErrNotFound) {
		t.Errorf("not in the person's user: %v", err)
	}
	var starts []string
	for _, c := range p.commands() {
		if strings.HasPrefix(c, "am start ") {
			starts = append(starts, c)
		}
	}
	want := []string{
		`am start --user current --display 0 ` + viewIntent + ` -f 0x10000000 -d 'https://example.com/a?b=c&d='\''e'\''' -n '` + chromeView + `' 2>&1`,
		`am start --user 999 --display 0 ` + viewIntent + ` -f 0x10000000 -d weixin://dl/scan -n '` + wechatView + `' 2>&1`,
		`am start --user current --display 7 ` + viewIntent + ` -f 0x10000000 -d weixin://dl/scan -n '` + wechatView + `' 2>&1`,
	}
	if strings.Join(starts, "\n") != strings.Join(want, "\n") {
		t.Errorf("starts:\n%s\nwant:\n%s", strings.Join(starts, "\n"), strings.Join(want, "\n"))
	}
}

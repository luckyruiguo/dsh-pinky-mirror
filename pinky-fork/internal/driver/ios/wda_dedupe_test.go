package ios

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	goios "github.com/danielpaulus/go-ios/ios"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

func wnode(typ, label string, x, y, w, h float64, accessible bool, kids ...wdaNode) wdaNode {
	n := wdaNode{Type: typ, Rect: wdaRect{X: x, Y: y, Width: w, Height: h}, Visible: true, Accessible: wdaBool(accessible), Children: kids}
	if label != "" {
		n.Label = &label
	}
	return n
}

// GitHub issue #7: WeChat's chat list on the WDA path listed a row's
// summary and date again next to the row, and rows scrolled under the
// navigation bar. A text its element already says is left out, and so is
// what a bar covers; the bars' own items stay.
func TestWDARowsAndBars(t *testing.T) {
	const w, h = 393, 852 // points; scale 1 keeps them as pixels
	root := wnode("Application", "WeChat", 0, 0, w, h, false,
		wnode("Window", "", 0, 0, w, h, false,
			wnode("Table", "", 0, 0, w, h, false,
				// Scrolled under the navigation bar.
				wnode("Cell", "Alice, See you tomorrow, Yesterday", 0, 40, w, 60, true,
					wnode("StaticText", "Alice", 70, 45, 200, 20, true)),
				// A row that is one element, with its texts listed again.
				wnode("Cell", "Bob, Lunch?, 10:24", 0, 120, w, 70, true,
					wnode("StaticText", "Bob", 70, 125, 200, 20, true),
					wnode("StaticText", "Lunch?", 70, 150, 200, 20, true),
					wnode("StaticText", "10:24", 330, 125, 50, 20, true),
					wnode("Button", "Mute", 340, 150, 40, 30, true)),
				// A row that is not: its button carries the label.
				wnode("Cell", "", 0, 200, w, 60, false,
					wnode("Button", "Carol", 0, 200, w, 60, true)),
				// Under the tab bar.
				wnode("Cell", "Dave, Bye, Monday", 0, 790, w, 60, true)),
			wnode("NavigationBar", "Chats", 0, 47, w, 44, true,
				wnode("Button", "Back", 0, 47, 60, 44, true),
				wnode("StaticText", "Chats", 150, 55, 100, 30, true)),
			wnode("TabBar", "Tab Bar", 0, 769, w, 83, false,
				wnode("Button", "Chats", 0, 769, 98, 49, true),
				wnode("Button", "Me", 295, 769, 98, 49, true))))
	els, _, _ := wdaScreenElements(&root, 1, w, h, "com.tencent.xin")
	var got []string
	for _, e := range els {
		got = append(got, fmt.Sprintf("%s %q", e.Role, e.Label))
	}
	want := []string{
		`button "Bob, Lunch?, 10:24"`, `button "Mute"`, `button "Carol"`,
		`other "Chats"`, `button "Back"`, // the bar's title repeats its label
		`tab "Chats"`, `tab "Me"`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("elements:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// A row mostly under the bar keeps the part that shows below it.
	root2 := wnode("Application", "WeChat", 0, 0, w, h, false,
		wnode("Cell", "Erin, Hi, Sunday", 0, 0, w, 140, true),
		wnode("NavigationBar", "Chats", 0, 47, w, 44, true))
	els, _, _ = wdaScreenElements(&root2, 1, w, h, "com.tencent.xin")
	if len(els) != 2 || els[0].Bounds != (screen.Rect{X: 0, Y: 91, W: w, H: 49}) {
		t.Fatalf("peeking row: %+v", els)
	}
}

// GitHub issue #7: a first read said WeChat, with only the lock screen's
// charging line from OCR. A read whose tree shows nothing of the app asks
// WDA whether the lock screen is in front, and lockdown failing does not
// read as unlocked.
func TestWDALockScreenBehindAnApp(t *testing.T) {
	for _, c := range []struct {
		name     string
		lockdown func(context.Context) (bool, error)
	}{
		{"lockdown says unlocked", func(context.Context) (bool, error) { return false, nil }},
		{"lockdown fails", func(context.Context) (bool, error) { return false, errors.New("no answer over Wi-Fi") }},
	} {
		f := &fakeLockedWDA{locked: true}
		srv := httptest.NewServer(f)
		d := &WDADevice{c: newWDAURL(srv.URL, ""), entry: goios.DeviceEntry{DeviceID: 7}, scale: 3, width: 1125, height: 2436,
			lockdownFn: c.lockdown}
		s, err := d.Observe(context.Background(), driver.ObserveOptions{})
		srv.Close()
		if err != nil || !s.Locked || len(s.Elements) != 0 {
			t.Errorf("%s: %+v, %v", c.name, s, err)
		}
	}
	// Unlocked with an empty tree: still unlocked.
	f := &fakeLockedWDA{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	d := &WDADevice{c: newWDAURL(srv.URL, ""), entry: goios.DeviceEntry{DeviceID: 7}, scale: 3, width: 1125, height: 2436,
		lockdownFn: func(context.Context) (bool, error) { return false, nil }}
	if s, err := d.Observe(context.Background(), driver.ObserveOptions{}); err != nil || s.Locked {
		t.Fatalf("unlocked: %+v, %v", s, err)
	}
}

// A web page on the WDA path (Safari): its own header, links and texts stay;
// only what Safari's bottom bar covers goes, and a link's text is not
// listed twice.
func TestWDAWebPageKeepsItsElements(t *testing.T) {
	const w, h = 393, 852
	root := wnode("Application", "Safari", 0, 0, w, h, false,
		wnode("WebView", "", 0, 0, w, h, false,
			// The page's own sticky header is no bar of iOS: nothing goes
			// under it.
			wnode("Other", "Site header", 0, 50, w, 60, false,
				wnode("Link", "Home", 10, 60, 80, 40, true, wnode("StaticText", "Home", 10, 60, 80, 40, true))),
			wnode("StaticText", "Order 20261108 shipped", 10, 120, 300, 30, true),
			wnode("Link", "Track your parcel", 10, 160, 300, 30, true),
			wnode("Button", "Pay ¥128.00", 10, 700, 300, 50, true),
			// Under Safari's address bar.
			wnode("Link", "Footer link", 10, 790, 300, 30, true)),
		wnode("Toolbar", "", 0, 760, w, 92, false,
			wnode("Button", "Address", 20, 770, 353, 44, true)))
	els, _, _ := wdaScreenElements(&root, 1, w, h, "com.apple.mobilesafari")
	var got []string
	for _, e := range els {
		got = append(got, fmt.Sprintf("%s %q", e.Role, e.Label))
	}
	want := []string{`link "Home"`, `text "Order 20261108 shipped"`, `link "Track your parcel"`, `button "Pay ¥128.00"`, `button "Address"`}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("elements:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

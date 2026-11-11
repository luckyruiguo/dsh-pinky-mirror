//go:build device

package android

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// TestDeviceLinks opens a web link and an app's own link, on the main
// display and on a background display, in the apps LinkApp names. Set
// PINKY_DEEP_LINK to a link an app on the phone opens (a small test app
// built for it, not one with the owner's data); without it only the web
// link is opened. Emulators only: the web link opens the browser. With
// PINKY_CHOICE_LINK, a link two apps open, it checks only that LinkApp
// refuses to pick one.
//
//	go test -c -tags device -o /tmp/pinky-and/android.test ./internal/driver/android
//	scripts/devlock.sh emulator-5720 env PINKY_DEVICE=emulator-5720 PINKY_TEST_PIN=1234 PINKY_DEEP_LINK=pinkytest://order/42 /tmp/pinky-and/android.test -test.run TestDeviceLinks -test.v
func TestDeviceLinks(t *testing.T) {
	d, ctx, _ := openEmulator(t)
	links := []string{"https://example.com/?from=pinky"}
	if l := os.Getenv("PINKY_DEEP_LINK"); l != "" {
		links = append(links, l)
	}
	if _, err := d.LinkApp(ctx, "nothing-opens-this://x"); !errors.Is(err, driver.ErrNotFound) {
		t.Errorf("a link no app opens: %v", err)
	}
	// PINKY_CHOICE_LINK: a link two apps open and neither is the default,
	// so the phone would ask which app to use.
	if l := os.Getenv("PINKY_CHOICE_LINK"); l != "" {
		var choice *driver.LinkChoiceError
		_, err := d.LinkApp(ctx, l)
		if !errors.As(err, &choice) || len(choice.Apps) < 2 {
			t.Fatalf("%s: %v, want a choice", l, err)
		}
		t.Logf("%s: the phone would ask: %v", l, err)
		return
	}
	focused := func(display int) string {
		out, err := d.client.Shell(ctx, d.info.ID, "dumpsys window displays | grep -E 'Display: mDisplayId|mFocusedApp='")
		if err != nil {
			t.Fatal(err)
		}
		s := string(out)
		if display > 0 {
			i := displaySection(s, display)
			if i < 0 {
				return ""
			}
			s = s[i:]
		}
		return parseFocusedApp(s)
	}
	waitFocused := func(display int, pkg string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			got := focused(display)
			if got == pkg {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("display %d shows %q, want %q", display, got, pkg)
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
	home := func() {
		if err := d.Key(ctx, driver.KeyHome); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
	}
	for _, link := range links {
		app, err := d.LinkApp(ctx, link)
		if err != nil {
			t.Fatalf("%s: %v", link, err)
		}
		t.Logf("%s opens in %s (%s)", link, app.ID, app.Name)
		if err := d.OpenLink(ctx, link, driver.App{ID: "com.android.settings"}); !errors.Is(err, driver.ErrNotFound) {
			t.Errorf("%s in Settings: %v", link, err)
		}

		// The main display.
		home()
		if err := d.OpenLink(ctx, link, app); err != nil {
			t.Fatalf("%s: %v", link, err)
		}
		waitFocused(0, app.ID)
		if strings.HasPrefix(app.ID, "cc.pinky.") {
			logLink(t, ctx, d, link)
		}

		// A background display: the link opens there, and the main display
		// keeps what it shows.
		if _, err := d.client.Shell(ctx, d.info.ID, "am force-stop "+app.ID); err != nil {
			t.Fatal(err)
		}
		home()
		before := focused(0)
		b, err := d.OpenDisplay(ctx, DisplayOptions{})
		if errors.Is(err, driver.ErrUnsupported) {
			t.Skipf("no background display: %v", err)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := b.OpenLink(ctx, link, app); err != nil {
			b.Close()
			t.Fatalf("%s on display %d: %v", link, b.DisplayID(), err)
		}
		waitFocused(b.DisplayID(), app.ID)
		if now := focused(0); now != before {
			t.Errorf("the main display changed from %q to %q", before, now)
		}
		if strings.HasPrefix(app.ID, "cc.pinky.") {
			logLink(t, ctx, d, link)
		}
		t.Logf("%s: opened on display %d", link, b.DisplayID())
		b.Close()
	}
	home()
}

// logLink checks that the test app logged the link it was opened with.
func logLink(t *testing.T, ctx context.Context, d *Device, link string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, err := d.client.Shell(ctx, d.info.ID, "logcat -d -s PinkyLinkTest:I | tail -1")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "Opened: "+link) {
			t.Logf("the app says: %s", strings.TrimSpace(string(out)))
			_, _ = d.client.Shell(ctx, d.info.ID, "logcat -c")
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the app did not log %s: %q", link, out)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

//go:build device

package ios

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/ios/cryptex"
	goios "github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/imagemounter"
)

// TestDiskImageStatus reads, and only reads, how the phone's developer disk
// image is mounted: Developer Mode, the cryptexes cryptexd lists, the
// personalized images the image mounter lists, and whether the image this
// computer would mount is the one on the phone. Nothing is mounted,
// unmounted or revealed.
func TestDiskImageStatus(t *testing.T) {
	udid := os.Getenv("PINKY_DEVICE")
	if udid == "" {
		t.Skip("PINKY_DEVICE not set")
	}
	img, err := findDiskImage("")
	t.Logf("image on this computer: %+v err=%v", img, err)

	base, err := findEntry(udid, false)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := imagemounter.IsDevModeEnabled(base)
	t.Logf("developer mode: %v err=%v", dev, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	l, err := dialLinkEntry(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	t.Logf("services: cryptexd %v, display %v, hid %v, instruments %v", l.has(cryptexService), l.has(displayService), l.has(universalHIDService), l.has(instrumentsService))

	if l.has(cryptexService) {
		cm, err := cryptex.NewCryptexDeveloperDiskImageMounter(l.entry)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		list, err := cm.ListCryptexes()
		t.Logf("cryptexes (%v): %+v err=%v", time.Since(start), list, err)
		if img.Dir != "" {
			ok, err := cm.IsImageMounted(img.Dir)
			t.Logf("this computer's image installed as a cryptex: %v err=%v", ok, err)
		}
	}

	v, err := goios.GetProductVersion(base)
	if err != nil {
		t.Fatal(err)
	}
	pm, err := imagemounter.NewPersonalizedDeveloperDiskImageMounter(base, v)
	if err != nil {
		t.Logf("personalized mounter: %v", err)
		return
	}
	defer pm.Close()
	start := time.Now()
	imgs, err := pm.ListImages()
	t.Logf("personalized images (%v): %d err=%v", time.Since(start), len(imgs), err)
}

// TestDiskImageMountKeeps runs Pinky's mount on a phone that already has
// this computer's image: it must find it there and change nothing (no
// signing request, no upload). Skipped when the phone's image differs.
func TestDiskImageMountKeeps(t *testing.T) {
	udid := os.Getenv("PINKY_DEVICE")
	if udid == "" {
		t.Skip("PINKY_DEVICE not set")
	}
	img, err := findDiskImage("")
	if err != nil {
		t.Skip(err)
	}
	base, err := findEntry(udid, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	l, err := dialLinkEntry(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	m, err := cryptex.NewCryptexDeveloperDiskImageMounter(l.entry)
	if err != nil {
		t.Skip(err)
	}
	if ok, err := m.IsImageMounted(img.Dir); err != nil || !ok {
		t.Skipf("the phone does not have this computer's image (%v, %v); this test only checks that a mounted one is kept", ok, err)
	}
	st, err := phoneStateOf(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	r, err := mountDiskImage(ctx, l, img, st.major)
	t.Logf("mountDiskImage (%v): %+v err=%v", time.Since(start), r, err)
	if err != nil || !r.Already || r.How != mountCryptex || r.Replaced != "" {
		t.Fatalf("mountDiskImage = %+v, %v; want the image kept as it is", r, err)
	}
	list, err := m.ListCryptexes()
	t.Logf("cryptexes afterwards: %+v err=%v", list, err)
}

// TestDiskImageRemount takes the developer disk image off the phone and
// lets the provider put it back, as after a restart: List must report the
// phone as being prepared, then online, with the same image as before. It
// changes the phone for a few seconds, so it runs only with
// PINKY_REMOUNT=1, with the owner's leave, and only when the phone has
// this computer's image (the one it puts back).
func TestDiskImageRemount(t *testing.T) {
	udid := os.Getenv("PINKY_DEVICE")
	remount := os.Getenv("PINKY_REMOUNT")
	if udid == "" || (remount != "1" && remount != "any") {
		t.Skip("set PINKY_DEVICE and PINKY_REMOUNT=1 to take the image off and let Pinky mount it again (PINKY_REMOUNT=any also takes off another version of it, which this computer's then replaces)")
	}
	img, err := findDiskImage("")
	if err != nil {
		t.Skip(err)
	}
	base, err := findEntry(udid, false)
	if err != nil {
		t.Fatal(err)
	}
	cryptexes := func(what string) []cryptex.RemoteCryptex {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		l, err := dialLinkEntry(ctx, base)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		defer l.Close()
		m, err := cryptex.NewCryptexDeveloperDiskImageMounter(l.entry)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		list, err := m.ListCryptexes()
		t.Logf("%s: cryptexes %+v err=%v; display %v, hid %v", what, list, err, l.has(displayService), l.has(universalHIDService))
		return list
	}
	has := func(list []cryptex.RemoteCryptex) bool {
		for _, c := range list {
			if c.Identifier == ddiCryptex && c.Version == img.Version {
				return true
			}
		}
		return false
	}
	hasAny := func(list []cryptex.RemoteCryptex) bool {
		for _, c := range list {
			if c.Identifier == ddiCryptex {
				return true
			}
		}
		return false
	}
	before := cryptexes("before")
	switch {
	case has(before):
	case remount == "any" && hasAny(before):
		t.Logf("the phone has another version of the image; this computer's (%s) takes its place", img.Version)
	default:
		t.Skip("the phone does not have this computer's image as a cryptex")
	}
	// A locked phone mounts nothing until it is unlocked, so taking the
	// image off one would leave it without until then.
	lctx, lcancel := context.WithTimeout(context.Background(), 10*time.Second)
	locked, err := lockedEntry(lctx, base)
	lcancel()
	if err != nil || locked {
		t.Skipf("the phone is locked (%v, %v): unlock it first", locked, err)
	}
	// Whatever happens below, the phone ends with its image.
	defer func() {
		if has(cryptexes("at the end")) {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), mountTimeout)
		defer cancel()
		l, err := dialLinkEntry(ctx, base)
		if err != nil {
			t.Errorf("putting the image back: %v", err)
			return
		}
		defer l.Close()
		st, err := phoneStateOf(ctx, base)
		if err != nil {
			t.Errorf("putting the image back: %v", err)
			return
		}
		r, err := mountDiskImage(ctx, l, img, st.major)
		t.Logf("putting the image back directly: %+v err=%v", r, err)
		if err != nil {
			t.Errorf("THE PHONE HAS NO DEVELOPER DISK IMAGE: %v. Open Xcode with the iPhone connected (Window > Devices and Simulators) to mount it again.", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	l, err := dialLinkEntry(ctx, base)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	m, err := cryptex.NewCryptexDeveloperDiskImageMounter(l.entry)
	if err == nil {
		start := time.Now()
		err = m.UnmountImage()
		t.Logf("took the image off in %v: err=%v", time.Since(start), err)
	}
	l.Close()
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if has(cryptexes("after taking it off")) {
		t.Fatal("the image is still there")
	}

	// The provider, as the gateway runs it: List every 2 s. Its log lines
	// say which way the mount went and how long it took.
	p := deviceProvider(t, udid)
	p.Log = slog.New(slog.NewTextHandler(testLog{t}, nil))
	start := time.Now()
	last := ""
	for {
		lctx, lcancel := context.WithTimeout(context.Background(), 30*time.Second)
		infos, err := p.List(lctx)
		lcancel()
		var info driver.Info
		for _, i := range infos {
			if i.ID == udid {
				info = i
			}
		}
		line := fmt.Sprintf("%s / %s / %s (err %v)", info.State, info.Setup, info.Detail, err)
		if line != last {
			t.Logf("%5.1f s: %s", time.Since(start).Seconds(), line)
			last = line
		}
		if info.State == driver.StateOnline {
			break
		}
		if time.Since(start) > 4*time.Minute {
			t.Fatalf("not online after 4 minutes: %s", line)
		}
		time.Sleep(2 * time.Second)
	}
	t.Logf("online again %.1f s after the image was taken off", time.Since(start).Seconds())
	if !has(cryptexes("after Pinky mounted it")) {
		t.Fatal("the phone is online but this computer's image is not there")
	}
}

// TestCheckupOnPhone opens the phone as the gateway does and runs its
// checkup, which only reads: Developer Mode, the image, a screenshot and
// the HID service (no event sent).
func TestCheckupOnPhone(t *testing.T) {
	udid := os.Getenv("PINKY_DEVICE")
	if udid == "" {
		t.Skip("PINKY_DEVICE not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	p := deviceProvider(t, udid)
	if _, err := p.List(ctx); err != nil {
		t.Fatal(err)
	}
	dev, err := p.Open(ctx, udid)
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	ck, ok := dev.(driver.Checker)
	if !ok {
		t.Fatalf("%T runs no checkup", dev)
	}
	start := time.Now()
	cu, err := ck.Checkup(ctx)
	t.Logf("checkup (%v): system %q err=%v", time.Since(start), cu.System, err)
	for _, c := range cu.Checks {
		t.Logf("  %-15s %-5s fix=%q %s", c.ID, c.Status, c.Fix, c.Detail)
	}
	if err != nil || len(cu.Checks) == 0 {
		t.Fatal("no checkup")
	}
	for _, c := range cu.Checks {
		if c.Status == driver.CheckFail {
			t.Errorf("%s failed: %s", c.ID, c.Detail)
		}
	}
}

// deviceProvider is a provider for the phone under test only, that uses it
// over Wi-Fi when usbmuxd lists it only there, as the app does with a
// phone the person uses.
func deviceProvider(t *testing.T, udid string) *Provider {
	t.Helper()
	p := NewProvider()
	p.Only = map[string]bool{udid: true}
	if e, err := findEntry(udid, false); err == nil && e.Properties.ConnectionType == "Network" {
		t.Logf("%s is on Wi-Fi", udid)
		p.SetWiFi(true)
		p.UseOverWiFi(udid)
	}
	return p
}

// testLog writes a provider's log lines to the test's output.
type testLog struct{ t *testing.T }

func (w testLog) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

//go:build e2e && !windows

package main

import (
	"bufio"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/telemetry"
)

// TestCheckupInTheApp drives the e2e build of the desktop app over MyGo's
// automation socket, in English and in Simplified Chinese: the fake
// Pixel's ⋯ › Checkup shows its checkup, every item passed, and Back to
// the phone closes it; Add phone › Android shows each brand's steps when
// its choice is pressed. It opens windows, so it needs a display with the
// screen unlocked:
//
//	go test -tags e2e -run TestCheckupInTheApp ./cmd/pinky
//
// With PINKY_SCREENSHOTS set to a folder it captures the checkup and each
// brand's steps there.
func TestCheckupInTheApp(t *testing.T) {
	if testing.Short() {
		t.Skip("opens the app")
	}
	bin := filepath.Join(t.TempDir(), "pinky")
	build := exec.Command("go", "build", "-tags", "e2e", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, lang := range []string{"en", "zh-Hans"} {
		t.Run(lang, func(t *testing.T) {
			t.Setenv("MYGO_LOCALE", lang)
			checkupInTheApp(t, bin, lang)
		})
	}
}

func checkupInTheApp(t *testing.T, bin, lang string) {
	m := locales.In(lang)
	const phone = "Pixel 9 (fake)"
	home := pinkyHome(t)
	backend := newFakeBackend(t)
	app := startApp(t, bin, home, backend.URL, filepath.Join(home, "opened.txt"))
	win := app.window(t)
	app.waitText(t, win, phone)

	// The phone's checkup, from ⋯ under its mirror.
	app.pressNode(t, win, m.PhoneMoreControls())
	app.pressNode(t, win, m.PhoneCheckup())
	app.waitText(t, win, m.CheckupFor(phone))
	app.waitText(t, win, m.CheckupAllGood())
	for _, want := range []string{m.CheckADBTitle(), m.CheckAuthorizedTitle(), m.CheckCompanionTitle(), m.CheckInputPass(),
		m.CheckScreenTitle(), m.CheckBackgroundTitle()} {
		app.waitText(t, win, want)
	}
	app.capture(t, win, "app-checkup-"+lang+".png")
	app.pressNode(t, win, m.CheckupBack())
	for i := 0; app.hasText(t, win, m.CheckupFor(phone)); i++ {
		if i > 100 {
			t.Fatal("the checkup did not close")
		}
		app.waitText(t, win, phone)
	}

	// Add phone › Android: each brand's steps.
	app.pressNode(t, win, m.SidebarAddPhone())
	app.pressNode(t, win, "Android")
	app.waitText(t, win, m.AddAndroidTitle())
	for _, b := range []struct {
		id, name, tap string
	}{
		{"xiaomi", m.BrandXiaomi(), m.GuideXiaomiTap()},
		{"oppo", m.BrandOppo(), m.GuideOppoTap()},
		{"vivo", m.BrandVivo(), m.GuideVivoTap()},
		{"honor", m.BrandHonor(), m.GuideHonorTap()},
		{"huawei", m.BrandHuawei(), m.GuideHuaweiTap()},
		{"samsung", m.BrandSamsung(), m.GuideSamsungTap()},
		{"pixel", m.BrandPixel(), m.GuidePixelTap()},
		{"other", m.BrandOther(), m.GuideOtherTap()},
	} {
		app.pressNode(t, win, b.name)
		app.waitText(t, win, m.AddAndroidStepDevTap(b.tap))
		app.capture(t, win, "app-add-android-"+b.id+"-"+lang+".png")
	}
	run(t, bin, home, "quit")
	app.wait(t)
}

// TestCheckupOnPhoneInTheApp runs the e2e build of the app on a real phone
// or emulator (no fake phones) and opens its checkup from ⋯, so the
// driver's checks show through the real gateway. The phone's id comes
// from PINKY_E2E_ANDROID; the app uses no other phone (PINKY_DEVICES):
//
//	scripts/devlock.sh emulator-5574 env PINKY_E2E_ANDROID=emulator-5574 go test -tags e2e -run TestCheckupOnPhoneInTheApp ./cmd/pinky
func TestCheckupOnPhoneInTheApp(t *testing.T) {
	serial := os.Getenv("PINKY_E2E_ANDROID")
	if serial == "" || testing.Short() {
		t.Skip("set PINKY_E2E_ANDROID to a phone's adb serial")
	}
	out, err := exec.Command("adb", "-s", serial, "shell", "settings get global device_name; getprop ro.product.model").Output()
	if err != nil {
		t.Fatal(err)
	}
	name := ""
	for _, l := range strings.Split(strings.ReplaceAll(string(out), "\r", ""), "\n") {
		if l = strings.TrimSpace(l); l != "" && l != "null" {
			name = l
			break
		}
	}
	bin := filepath.Join(t.TempDir(), "pinky")
	build := exec.Command("go", "build", "-tags", "e2e", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, lang := range []string{"en", "zh-Hans"} {
		t.Run(lang, func(t *testing.T) {
			t.Setenv("MYGO_LOCALE", lang)
			t.Setenv("PINKY_DEVICES", serial)
			m := locales.In(lang)
			home := pinkyHome(t)
			backend := newFakeBackend(t)
			app := startRealApp(t, bin, home, backend.URL)
			win := app.window(t)
			app.waitText(t, win, name)
			app.pressNode(t, win, m.PhoneMoreControls())
			app.pressNode(t, win, m.PhoneCheckup())
			app.waitText(t, win, m.CheckupFor(name))
			for _, want := range []string{m.CheckADBPass(), m.CheckInputPass()} {
				app.waitText(t, win, want)
			}
			app.capture(t, win, "app-checkup-"+serial+"-"+lang+".png")
			run(t, bin, home, "quit")
			app.wait(t)
		})
	}
}

// startRealApp is startApp without fake phones: the app uses the phones
// connected to this computer (PINKY_DEVICES limits which).
func startRealApp(t *testing.T, bin, home, backendURL string) *uiApp {
	t.Helper()
	sock := filepath.Join(home, "ui.sock")
	os.Remove(sock)
	cmd := exec.Command(bin)
	cmd.Env = append(env(home), "MYGO_AUTOMATION="+sock, telemetry.EndpointEnv+"="+backendURL, "DO_NOT_TRACK=1")
	out := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	a := &uiApp{cmd: cmd, done: make(chan struct{}), out: out}
	go func() {
		cmd.Wait()
		close(a.done)
	}()
	t.Cleanup(func() {
		if a.conn != nil {
			a.conn.Close()
		}
		select {
		case <-a.done:
		default:
			cmd.Process.Kill()
			<-a.done
		}
	})
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		conn, err := net.Dial("unix", sock)
		if err == nil {
			a.conn, a.r = conn, bufio.NewReader(conn)
			return a
		}
		if time.Now().After(deadline) {
			t.Fatalf("no automation socket: %v\n%s", err, out.String())
		}
	}
}

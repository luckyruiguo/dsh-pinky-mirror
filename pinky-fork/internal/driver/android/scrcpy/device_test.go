//go:build device

package scrcpy

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

// Run with: scripts/devlock.sh <id> env PINKY_DEVICE=<id> go test -tags device ./internal/driver/android/scrcpy/

func deviceSerial(t *testing.T) string {
	t.Helper()
	s := os.Getenv("PINKY_DEVICE")
	if s == "" {
		t.Skip("PINKY_DEVICE not set")
	}
	return s
}

func TestDeviceSessionVideo(t *testing.T) {
	serial := deviceSerial(t)
	c := &adb.Client{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	s, err := Start(ctx, c, serial, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t.Logf("started in %v: device %q codec %s", time.Since(start).Round(time.Millisecond), s.DeviceName, CodecName(s.Codec))
	// Ask for a fresh key frame so the test sees one even on a still screen.
	if err := s.Send(ResetVideo()); err != nil {
		t.Fatal(err)
	}
	var sessions, configs, keys, frames, bytes int
	deadline := time.Now().Add(3 * time.Second)
	done := make(chan struct{})
	go func() {
		<-time.After(time.Until(deadline))
		close(done)
		s.Close()
	}()
	for {
		p, err := s.ReadPacket()
		if err != nil {
			select {
			case <-done:
			default:
				t.Fatal(err)
			}
			break
		}
		switch {
		case p.Session:
			sessions++
			t.Logf("session %dx%d", p.Width, p.Height)
		case p.Config:
			configs++
		default:
			frames++
			if p.KeyFrame {
				keys++
			}
		}
		bytes += len(p.Data)
	}
	t.Logf("sessions=%d configs=%d frames=%d keyframes=%d bytes=%d", sessions, configs, frames, keys, bytes)
	if sessions == 0 || configs == 0 || keys == 0 {
		t.Errorf("want a session header, a config packet and a key frame")
	}
	t.Logf("logs: %q", s.Logs())
}

func TestDeviceListApps(t *testing.T) {
	serial := deviceSerial(t)
	c := &adb.Client{}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	start := time.Now()
	apps, err := ListApps(ctx, c, serial)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d apps in %v", len(apps), time.Since(start).Round(time.Millisecond))
	var settings bool
	for _, a := range apps {
		if a.Package == "com.android.settings" {
			settings = true
			t.Logf("settings: %+v", a)
		}
	}
	if !settings {
		t.Error("com.android.settings not listed")
	}
	// The server deletes its jar in list mode.
	out, _ := c.Shell(ctx, serial, "ls /data/local/tmp/ | grep pinky-scrcpy || true")
	if len(out) > 0 {
		t.Errorf("leftover server jars: %s", out)
	}
}

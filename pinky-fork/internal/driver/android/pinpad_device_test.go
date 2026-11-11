//go:build device

package android

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// TestDeviceUnlockByTaps unlocks one of our emulators the way Pinky
// unlocks OEM lock screens: it reads the PIN pad from its keys' labels and
// layout, ignoring AOSP's ids, and taps its keys and its confirm key;
// once reading the screen with uiautomator, once through the companion
// (when it is installed), and once with a wrong PIN, which must end in
// driver.ErrLocked after one try. It locks the emulator before each.
// Emulators only, never a person's phone, with the emulator's PIN in
// PINKY_TEST_PIN:
//
//	go test -c -tags device -o /tmp/pinky-android.test ./internal/driver/android
//	scripts/devlock.sh emulator-5560 env PINKY_DEVICE=emulator-5560 PINKY_TEST_PIN=1234 /tmp/pinky-android.test -test.run TestDeviceUnlockByTaps -test.v
func TestDeviceUnlockByTaps(t *testing.T) {
	id, pin := os.Getenv("PINKY_DEVICE"), os.Getenv("PINKY_TEST_PIN")
	if !strings.HasPrefix(id, "emulator-") || pin == "" {
		t.Skip("set PINKY_DEVICE to one of our emulators (emulator-5560) and PINKY_TEST_PIN to its PIN")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var mu sync.Mutex
	var logs []string
	logf := func(format string, args ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		mu.Unlock()
		t.Logf(format, args...)
	}
	d, err := NewProvider(Config{Logf: logf}).OpenDevice(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	padOnlyDevice.Store(d)
	defer padOnlyDevice.Store(nil)
	for _, via := range []string{"uiautomator", "companion"} {
		t.Run(via, func(t *testing.T) {
			// Holding the companion off makes the driver read the screen
			// with uiautomator.
			d.comp.mu.Lock()
			if via == "uiautomator" {
				d.comp.retryAt = time.Now().Add(time.Hour)
			} else {
				d.comp.retryAt = time.Time{}
			}
			d.comp.mu.Unlock()
			lockEmulator(ctx, t, d)
			mu.Lock()
			logs = nil
			mu.Unlock()
			start := time.Now()
			err := d.Unlock(ctx, pin)
			t.Logf("Unlock: %v in %v", err, time.Since(start).Round(time.Millisecond))
			if err != nil {
				t.Fatal(err)
			}
			if locked, err := d.Locked(ctx); err != nil || locked {
				t.Fatalf("locked %v, %v after Unlock", locked, err)
			}
			mu.Lock()
			defer mu.Unlock()
			all := strings.Join(logs, "\n")
			if !strings.Contains(all, "read from its keys' labels (confirm key: true)") {
				t.Error("the PIN did not go in as taps on a pad read from its labels, with its confirm key")
			}
			if via == "companion" && !strings.Contains(all, "companion") {
				t.Log("the companion is not installed on this emulator; read with uiautomator again")
			}
		})
	}
	// A wrong PIN: one try, then ErrLocked and no retry. The emulator's
	// own PIN unlocks it afterwards, which resets its count of failures.
	t.Run("wrong PIN", func(t *testing.T) {
		wrong := []byte(pin)
		wrong[len(wrong)-1] = '0' + (wrong[len(wrong)-1]-'0'+1)%10
		lockEmulator(ctx, t, d)
		err := d.Unlock(ctx, string(wrong))
		t.Logf("Unlock with a wrong PIN: %v", err)
		if !errors.Is(err, driver.ErrLocked) {
			t.Errorf("want driver.ErrLocked, got %v", err)
		}
		if locked, err := d.Locked(ctx); err != nil || !locked {
			t.Fatalf("locked %v, %v after a wrong PIN", locked, err)
		}
		if err := d.Unlock(ctx, pin); err != nil {
			t.Fatalf("the right PIN after a wrong one: %v", err)
		}
	})
}

// lockEmulator turns an emulator's screen off and waits until it locks.
func lockEmulator(ctx context.Context, t *testing.T, d *Device) {
	t.Helper()
	if _, err := d.client.Shell(ctx, d.info.ID, "input keyevent KEYCODE_SLEEP"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(20 * time.Second); ; {
		time.Sleep(500 * time.Millisecond)
		on, _ := d.ScreenOn(ctx)
		locked, _ := d.Locked(ctx)
		if !on && locked {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the emulator did not lock (screen on %v, locked %v)", on, locked)
		}
	}
}

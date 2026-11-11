//go:build device

package android

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/companion"
	"github.com/DIYgod/pinky/internal/screen"
)

// GitHub issue #31: typing into fields that refuse ACTION_SET_TEXT on a
// background display, with a Chinese keyboard. The input test app
// (android/inputtest) has one field for each way a field takes text, and a
// line that echoes what each holds. Each field is tapped, typed into with
// the driver's Type, and read back from the echo.
//
//	android/companion/gradlew -p android/inputtest assembleDebug
//	android/companion/gradlew -p android/companion assembleDebug
//	adb -s emulator-5560 install -r android/companion/app/build/outputs/apk/debug/app-debug.apk
//	go test -c -tags device -o /tmp/pinky-and/android.test ./internal/driver/android
//	scripts/devlock.sh emulator-5560 env PINKY_DEVICE=emulator-5560 PINKY_TEST_PIN=1234 \
//	  PINKY_COMPANION_APK=$PWD/android/companion/app/build/outputs/apk/debug/app-debug.apk \
//	  /tmp/pinky-and/android.test -test.run TestEmulatorBackgroundTypeFallback -test.v
//
// Set the emulator's keyboard to Gboard's Chinese (Pinyin) first, as on the
// phone in the issue: it composes keys and swallows the paste key, so text
// that falls through to keys does not land.
func TestEmulatorBackgroundTypeFallback(t *testing.T) {
	d, ctx, _ := openEmulator(t)
	const pkg = "app.pinky.inputtest"
	apk := filepath.Join("..", "..", "..", "android", "inputtest", "build", "outputs", "apk", "debug", "pinky-inputtest-debug.apk")
	if _, err := os.Stat(apk); err != nil {
		t.Skipf("no input test app at %s; build it with android/companion/gradlew -p android/inputtest assembleDebug", apk)
	}
	if err := (&companion.ExecRunner{Serial: d.info.ID}).Install(ctx, apk); err != nil {
		t.Fatal(err)
	}
	if _, err := d.client.Shell(ctx, d.info.ID, "am force-stop "+pkg); err != nil {
		t.Fatal(err)
	}
	b, err := d.OpenDisplay(ctx, DisplayOptions{App: pkg})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.WaitSettled(ctx, 0)
	read := func() *screen.Screen {
		t.Helper()
		s, err := b.Observe(ctx, driver.ObserveOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := read()
	if !d.comp.has(companion.FeatureSetTextFallback) {
		t.Skip("the companion lacks set_text_fallback: install the one built from this tree")
	}
	at := map[string]screen.Point{}
	for _, e := range s.Elements {
		if _, id, ok := strings.Cut(e.ID, ":id/"); ok {
			at[id] = e.Bounds.Center()
		}
	}
	echo := func() string {
		t.Helper()
		for _, e := range read().Elements {
			if strings.HasSuffix(e.ID, ":id/echo") {
				return e.Label
			}
		}
		t.Fatal("no echo line")
		return ""
	}

	for _, tc := range []struct {
		field, text string
		submit      bool
		want        string // in the echo line
		wantErr     string
	}{
		{field: "plain", text: "hello", want: "plain=[hello]"},       // ACTION_SET_TEXT
		{field: "no_set_text", text: "你好", want: "no_set_text=[你好]"}, // the input connection
		{field: "no_commit", text: "粘贴", want: "no_commit=[粘贴]"},     // a paste
		{field: "chat", text: "微信", want: "chat=[微信]"},               // the input connection of a custom view
		{field: "chat", text: "发送", submit: true, want: "sent=[发送]"}, // and its editor action
		{field: "short_field", text: "abcdefg", want: "short=[abcd]", wantErr: "4 characters where 7 were typed"},
		{field: "dead", text: "dead", wantErr: "the text did not reach the field"},
	} {
		p, ok := at[tc.field]
		if !ok {
			t.Fatalf("no field %q on the screen:\n%s", tc.field, s.Render())
		}
		if err := b.Tap(ctx, p, driver.TapOptions{}); err != nil {
			t.Fatal(err)
		}
		b.WaitSettled(ctx, 0)
		start := time.Now()
		err := b.Type(ctx, tc.text, driver.TypeOptions{Submit: tc.submit})
		took := time.Since(start).Round(time.Millisecond)
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s: Type(%q): %v", tc.field, tc.text, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s: Type(%q) = %v, want %q", tc.field, tc.text, err, tc.wantErr)
		}
		b.WaitSettled(ctx, 0)
		line := echo()
		t.Logf("%s: %q in %v: %v; %s", tc.field, tc.text, took, err, line)
		if tc.want != "" && !strings.Contains(line, tc.want) {
			t.Errorf("%s: the echo reads %q, want %q", tc.field, line, tc.want)
		}
	}
}

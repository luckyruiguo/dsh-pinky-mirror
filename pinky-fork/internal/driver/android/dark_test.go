package android

import (
	"bytes"
	"testing"

	"github.com/DIYgod/pinky/internal/driver/android/scrcpy"
)

// powerCmd's output on the Mi 10 Pro (Android 17): asleep and locked, then
// awake with the panel off and a virtual display listed after the panel.
const (
	powerAsleep = `      showing=true
      occluded=false
      interactiveState=INTERACTIVE_STATE_SLEEP
    connectionType=Internal
    powerMode=Off
`
	powerDark = `      showing=true
      occluded=false
      interactiveState=INTERACTIVE_STATE_AWAKE
    connectionType=Internal
    powerMode=Off
    powerMode=On
`
)

func TestParsePhonePower(t *testing.T) {
	for _, c := range []struct {
		name string
		out  string
		want phonePower
	}{
		{"asleep", powerAsleep, phonePower{known: true, locked: true, panel: "Off"}},
		{"dark", powerDark, phonePower{known: true, interactive: true, locked: true, panel: "Off"}},
		{"waking, a call over the lock screen", `      showing=true
      occluded=true
      interactiveState=INTERACTIVE_STATE_WAKING
    connectionType=Internal
    powerMode=On
`, phonePower{known: true, interactive: true, locked: true, occluded: true, panel: "On"}},
		{"unlocked, always-on display", `      showing=false
      occluded=false
      interactiveState=INTERACTIVE_STATE_GOING_TO_SLEEP
    connectionType=Internal
    powerMode=Doze
`, phonePower{known: true, panel: "Doze"}},
		{"external display first", `      showing=true
      interactiveState=INTERACTIVE_STATE_AWAKE
    connectionType=External
    powerMode=On
    connectionType=Internal
    powerMode=Off
`, phonePower{known: true, interactive: true, locked: true, panel: "Off"}},
		{"a foldable with its outer panel on", `      showing=false
      interactiveState=INTERACTIVE_STATE_AWAKE
    connectionType=Internal
    powerMode=Off
    connectionType=Internal
    powerMode=On
`, phonePower{known: true, interactive: true, panel: "On"}},
		{"no keyguard state", "    powerMode=On\n", phonePower{}},
	} {
		if got := parsePhonePower(c.out); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestParseDisplayGroup(t *testing.T) {
	// `dumpsys display | grep -E 'displayId 214,'` on the Mi 10 Pro, and an
	// Android 16 display in a group of its own.
	out := `    mBaseDisplayInfo=DisplayInfo{"scrcpy", displayId 214, displayGroupId 0, FLAG_PRIVATE, FLAG_OWN_DISPLAY_GROUP, real 1080 x 2340
    mOverrideDisplayInfo=DisplayInfo{"scrcpy", displayId 214, displayGroupId 0, FLAG_PRIVATE
    mBaseDisplayInfo=DisplayInfo{"scrcpy", displayId 21, displayGroupId 3, FLAG_PRIVATE
`
	for _, c := range []struct {
		id, group int
		ok        bool
	}{{214, 0, true}, {21, 3, true}, {2, 0, false}} {
		g, ok := parseDisplayGroup(out, c.id)
		if g != c.group || ok != c.ok {
			t.Errorf("display %d: got %d %v, want %d %v", c.id, g, ok, c.group, c.ok)
		}
	}
}

func TestParseSleepReason(t *testing.T) {
	reason, woke := parseSleepReason("  mLastWakeTime=486219905 (1345102 ms ago)\n  mLastSleepReason=power_button\n")
	if reason != "power_button" || woke != "486219905" {
		t.Errorf("got %q, %q", reason, woke)
	}
	for reason, person := range map[string]bool{"timeout": false, "inattentive": false, "": false,
		"power_button": true, "sleep_button": true, "accessibility": true, "application": true} {
		if personSleep(reason) != person {
			t.Errorf("personSleep(%q) = %v", reason, !person)
		}
	}
}

func TestDarkWakeTrain(t *testing.T) {
	b := darkWakeTrain()
	down := scrcpy.InjectKeycode(scrcpy.ActionDown, keycodeWakeup, 0, 0)
	up := scrcpy.InjectKeycode(scrcpy.ActionUp, keycodeWakeup, 0, 0)
	off := scrcpy.SetDisplayPower(false)
	if !bytes.HasPrefix(b, append(append([]byte{}, down...), up...)) {
		t.Fatal("the train does not start with the wake key")
	}
	rest := b[len(down)+len(up):]
	if len(rest) != darkTrain*len(off) || !bytes.Equal(rest[:len(off)], off) || !bytes.Equal(rest[len(rest)-len(off):], off) {
		t.Fatalf("after the key: %d bytes, want %d panel-off messages", len(rest), darkTrain)
	}
}

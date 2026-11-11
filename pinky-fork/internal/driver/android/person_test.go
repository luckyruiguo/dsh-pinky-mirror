package android

import (
	"strings"
	"testing"
	"time"
)

// The device list follows `getevent -lp` on the Mi 10 Pro (trimmed); the
// events are hand-written in getevent's format, since nobody could touch
// the phone while this was written.
const geteventDevices = `add device 1: /dev/input/event8
  name:     "kona-mtp-snd-card USB_3_5 Jack"
  events:
    SW  (0005): SW_HEADPHONE_INSERT   SW_MICROPHONE_INSERT  SW_LINEOUT_INSERT
add device 2: /dev/input/event7
  name:     "kona-mtp-snd-card Button Jack"
  events:
    KEY (0001): KEY_MEDIA             BTN_1                 BTN_2                 BTN_3
add device 3: /dev/input/event4
  name:     "gpio-keys"
  events:
    KEY (0001): KEY_VOLUMEUP
  input props:
    <none>
add device 4: /dev/input/event5
  name:     "fts"
  events:
    KEY (0001): KEY_W                 KEY_E                 KEY_O                 KEY_LEFTBRACE
                BTN_TOUCH             0152                  KEY_GOTO
    ABS (0003): ABS_MT_SLOT           : value 0, min 0, max 9, fuzz 0, flat 0, resolution 0
                ABS_MT_POSITION_X     : value 0, min 0, max 1079, fuzz 0, flat 0, resolution 0
                ABS_MT_POSITION_Y     : value 0, min 0, max 2339, fuzz 0, flat 0, resolution 0
                ABS_MT_TRACKING_ID    : value 0, min 0, max 65535, fuzz 0, flat 0, resolution 0
  input props:
    INPUT_PROP_DIRECT
add device 5: /dev/input/event1
  name:     "qpnp_pon"
  events:
    KEY (0001): KEY_VOLUMEDOWN        KEY_POWER
  input props:
    <none>
add device 6: /dev/input/event2
  name:     "uinput-goodix"
  events:
    KEY (0001): KEY_HOME              KEY_UP                KEY_LEFT              KEY_RIGHT
                KEY_DOWN              KEY_VOLUMEDOWN        KEY_VOLUMEUP          KEY_POWER
add device 7: /dev/input/event0
  name:     "xiaomi-touch"
  events:
    KEY (0001): BTN_MISC              0153
add device 8: /dev/input/event9
  name:     "Logitech Mouse"
  events:
    KEY (0001): BTN_MOUSE             BTN_RIGHT
    REL (0002): REL_X                 REL_Y
    ABS (0003): ABS_X                 : value 0, min 0, max 100, fuzz 0, flat 0, resolution 0
`

func TestClassifyInputDevices(t *testing.T) {
	devs := classifyInputDevices(geteventDevices)
	if len(devs.touch) != 1 || !devs.touch["/dev/input/event5"] {
		t.Errorf("touchscreens = %v", devs.touch)
	}
	if len(devs.keys) != 2 || !devs.keys["/dev/input/event4"] || !devs.keys["/dev/input/event1"] {
		t.Errorf("key devices = %v (uinput, headset and mouse must be left out)", devs.keys)
	}
}

func TestParseEventLine(t *testing.T) {
	ev, ok := parseEventLine("/dev/input/event5: 0003 0035 0000021c")
	if !ok || ev.path != "/dev/input/event5" || ev.typ != evAbs || ev.code != 0x35 || ev.value != 0x21c {
		t.Fatalf("event = %+v, %v", ev, ok)
	}
	ev, ok = parseEventLine("[   12345.678901] /dev/input/event5: 0003 0039 ffffffff")
	if !ok || ev.value != -1 {
		t.Fatalf("time-stamped release = %+v, %v", ev, ok)
	}
	for _, bad := range []string{"", "pid:1234", "add device 1: /dev/input/event8", "/dev/input/event5: 0003 zz 1", "could not open /dev/input/event9"} {
		if _, ok := parseEventLine(bad); ok {
			t.Errorf("parsed %q", bad)
		}
	}
}

func TestPersonEvents(t *testing.T) {
	devs := classifyInputDevices(geteventDevices)
	// A finger down and up on the screen, a volume key press and release,
	// a fingerprint gesture and a headset button.
	stream := `/dev/input/event5: 0003 0039 00000123
/dev/input/event5: 0001 014a 00000001
/dev/input/event5: 0003 0035 0000021c
/dev/input/event5: 0003 0036 00000492
/dev/input/event5: 0000 0000 00000000
/dev/input/event5: 0003 0039 ffffffff
/dev/input/event5: 0001 014a 00000000
/dev/input/event5: 0000 0000 00000000
/dev/input/event4: 0001 0073 00000001
/dev/input/event4: 0000 0000 00000000
/dev/input/event4: 0001 0073 00000000
/dev/input/event2: 0001 0066 00000001
/dev/input/event7: 0001 00e2 00000001`
	var person []string
	for _, line := range strings.Split(stream, "\n") {
		ev, ok := parseEventLine(line)
		if !ok {
			t.Fatalf("unparsed %q", line)
		}
		if devs.personEvent(ev) {
			person = append(person, line)
		}
	}
	// Seven touch events (EV_SYN excluded) and one key press.
	if len(person) != 7 || person[6] != "/dev/input/event4: 0001 0073 00000001" {
		t.Errorf("person events:\n%s", strings.Join(person, "\n"))
	}
}

func TestPersonSignalRate(t *testing.T) {
	w := &personWatch{ch: make(chan struct{}, 1)}
	t0 := time.Unix(100, 0)
	w.signal(t0)
	w.signal(t0.Add(100 * time.Millisecond)) // too soon: dropped
	if len(w.ch) != 1 {
		t.Fatalf("buffered %d signals", len(w.ch))
	}
	<-w.ch
	w.signal(t0.Add(200 * time.Millisecond))
	if len(w.ch) != 0 {
		t.Error("a signal within the minimum gap went through")
	}
	w.signal(t0.Add(400 * time.Millisecond))
	w.signal(t0.Add(800 * time.Millisecond)) // the reader is slow: never blocks
	if len(w.ch) != 1 {
		t.Errorf("buffered %d signals", len(w.ch))
	}
}

// The Android 10 and 16 emulators' touchscreens: multi-touch axes and no
// input properties at all.
const geteventEmulator = `add device 1: /dev/input/event0
  name:     "gpio-keys"
  events:
    KEY (0001): KEY_POWER
  input props:
    <none>
add device 2: /dev/input/event1
  name:     "virtio_input_multi_touch_1"
  events:
    KEY (0001): BTN_TOOL_RUBBER       BTN_STYLUS
    ABS (0003): ABS_X                 : value 0, min 0, max 32767, fuzz 0, flat 0, resolution 0
                ABS_MT_SLOT           : value 0, min 0, max 10, fuzz 0, flat 0, resolution 0
                ABS_MT_POSITION_X     : value 0, min 0, max 32767, fuzz 0, flat 0, resolution 0
                ABS_MT_POSITION_Y     : value 0, min 0, max 32767, fuzz 0, flat 0, resolution 0
                ABS_MT_TRACKING_ID    : value 0, min 0, max 65535, fuzz 0, flat 0, resolution 0
    SW  (0005): SW_LID
  input props:
    <none>
add device 3: /dev/input/event2
  name:     "virtio_input_multi_touch_2"
  events:
    ABS (0003): ABS_MT_POSITION_X     : value 0, min 0, max 32767, fuzz 0, flat 0, resolution 0
  input props:
    <none>
`

func TestClassifyEmulatorDevices(t *testing.T) {
	devs := classifyInputDevices(geteventEmulator)
	if got := devs.String(); got != "touchscreens [event1, event2] and keys [event0]" {
		t.Errorf("emulator devices: %s", got)
	}
	// With a direct touchscreen present, other multi-touch devices stay out.
	devs = classifyInputDevices(geteventEmulator + `add device 4: /dev/input/event9
  name:     "fts"
  events:
    ABS (0003): ABS_MT_POSITION_X     : value 0, min 0, max 1079, fuzz 0, flat 0, resolution 0
  input props:
    INPUT_PROP_DIRECT
`)
	if got := devs.String(); got != "touchscreens [event9] and keys [event0]" {
		t.Errorf("with a direct touchscreen: %s", got)
	}
}

package scrcpy

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestServerJarEmbedded(t *testing.T) {
	jar := ServerJar()
	if len(jar) < 100_000 || !bytes.HasPrefix(jar, []byte("PK\x03\x04")) {
		t.Fatalf("embedded server looks wrong: %d bytes", len(jar))
	}
}

func TestArgs(t *testing.T) {
	got := Options{}.Args(0x1234abcd)
	want := []string{"5.0.1", "scid=1234abcd", "log_level=info", "audio=false", "tunnel_forward=true", "clipboard_autosync=false", "power_on=false"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("default args = %q", got)
	}
	got = Options{
		MaxSize: 1024, MaxFPS: 30, VideoBitRate: 4_000_000, VideoCodec: "h265",
		Virtual:   &VirtualDisplay{Width: 1080, Height: 2340, DPI: 440, NoSystemDecorations: true},
		DisplayID: 3, LogLevel: "debug",
		Extra: []string{"keep_active=true", "junk", "stay_awake=true", "power_on=true", "power_off_on_close=true", "turn_screen_off=true", "screen_off_timeout=600"},
	}.Args(0xffffffff)
	want = []string{"5.0.1", "scid=7fffffff", "log_level=debug", "audio=false", "tunnel_forward=true", "clipboard_autosync=false", "power_on=false",
		"max_size=1024", "max_fps=30", "video_bit_rate=4000000", "video_codec=h265",
		"new_display=1080x2340/440", "vd_system_decorations=false", "keep_active=true"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %q", got)
	}
	got = Options{Virtual: &VirtualDisplay{}}.Args(1)
	if got[len(got)-1] != "new_display=" {
		t.Errorf("virtual display at main size: %q", got)
	}
	if socketName(0x8000002a) != "scrcpy_0000002a" {
		t.Errorf("socket name = %q", socketName(0x8000002a))
	}
}

func TestControlMessages(t *testing.T) {
	check := func(name string, got []byte, wantHex string) {
		t.Helper()
		if h := hex.EncodeToString(got); h != wantHex {
			t.Errorf("%s = %s, want %s", name, h, wantHex)
		}
	}
	check("key", InjectKeycode(ActionDown, 66, 0, 0x3000), "00"+"00"+"00000042"+"00000000"+"00003000")
	msg, err := InjectText("hi")
	if err != nil {
		t.Fatal(err)
	}
	check("text", msg, "01"+"00000002"+"6869")
	check("touch", InjectTouch(ActionMove, PointerGenericFinger, Position{X: 540, Y: 1170, Width: 1080, Height: 2340}, 1, 0, 0),
		"02"+"02"+"fffffffffffffffe"+"0000021c"+"00000492"+"0438"+"0924"+"ffff"+"00000000"+"00000000")
	check("touch up", InjectTouch(ActionUp, 7, Position{X: 1, Y: 2, Width: 3, Height: 4}, 0, 0, 0)[22:24], "0000")
	check("scroll", InjectScroll(Position{X: 10, Y: 20, Width: 100, Height: 200}, 0, -16, 0),
		"03"+"0000000a"+"00000014"+"0064"+"00c8"+"0000"+"8000"+"00000000")
	check("back", BackOrScreenOn(ActionUp), "0401")
	check("get clipboard", GetClipboard(CopyKeyNone), "0800")
	msg, err = SetClipboard(5, "\u00e9", true)
	if err != nil {
		t.Fatal(err)
	}
	check("set clipboard", msg, "09"+"0000000000000005"+"01"+"00000002"+"c3a9")
	check("power", SetDisplayPower(false), "0a00")
	msg, err = StartApp("?Calculator")
	if err != nil {
		t.Fatal(err)
	}
	check("start app", msg, "10"+"0b"+hex.EncodeToString([]byte("?Calculator")))
	check("reset video", ResetVideo(), "11")

	if _, err := InjectText(strings.Repeat("a", MaxInjectText+1)); err == nil {
		t.Error("over-long inject text accepted")
	}
	if _, err := StartApp(strings.Repeat("a", 256)); err == nil {
		t.Error("over-long app name accepted")
	}
}

func TestFixedPoint(t *testing.T) {
	for f, want := range map[float64]uint16{0: 0, -1: 0, 0.5: 0x8000, 1: 0xffff, 2: 0xffff} {
		if got := u16FixedPoint(f); got != want {
			t.Errorf("u16(%v) = %#x, want %#x", f, got, want)
		}
	}
	for f, want := range map[float64]int16{0: 0, 1: 0x7fff, -1: -0x8000, 0.5: 0x4000, -0.5: -0x4000} {
		if got := i16FixedPoint(f); got != want {
			t.Errorf("i16(%v) = %#x, want %#x", f, got, want)
		}
	}
}

func TestSplitText(t *testing.T) {
	s := strings.Repeat("a", 299) + "\u00e9" + strings.Repeat("b", 10)
	parts := SplitText(s, MaxInjectText)
	if len(parts) != 2 || strings.Join(parts, "") != s {
		t.Fatalf("parts = %d", len(parts))
	}
	for _, p := range parts {
		if len(p) > MaxInjectText || !utf8.ValidString(p) {
			t.Errorf("bad part %q", p)
		}
	}
	if SplitText("", 10) != nil {
		t.Error("empty text should give no parts")
	}
}

func header(ptsFlags uint64, size uint32) []byte {
	b := make([]byte, 12)
	binary.BigEndian.PutUint64(b, ptsFlags)
	binary.BigEndian.PutUint32(b[8:], size)
	return b
}

func TestReadPacket(t *testing.T) {
	var stream bytes.Buffer
	// Session header: flags with the session bit, then width and height.
	sess := make([]byte, 12)
	binary.BigEndian.PutUint32(sess[0:], 0x80000001)
	binary.BigEndian.PutUint32(sess[4:], 1080)
	binary.BigEndian.PutUint32(sess[8:], 2340)
	stream.Write(sess)
	stream.Write(header(flagConfig, 4))
	stream.Write([]byte{0, 0, 0, 1})
	stream.Write(header(flagKeyFrame|123456, 3))
	stream.Write([]byte{9, 9, 9})
	stream.Write(header(140000, 1))
	stream.Write([]byte{7})

	p, err := ReadPacket(&stream)
	if err != nil || !p.Session || p.Width != 1080 || p.Height != 2340 || !p.ClientResize {
		t.Fatalf("session = %+v, %v", p, err)
	}
	p, err = ReadPacket(&stream)
	if err != nil || !p.Config || p.KeyFrame || len(p.Data) != 4 || p.PTS != 0 {
		t.Fatalf("config = %+v, %v", p, err)
	}
	p, err = ReadPacket(&stream)
	if err != nil || !p.KeyFrame || p.Config || p.PTS != 123456*time.Microsecond || !bytes.Equal(p.Data, []byte{9, 9, 9}) {
		t.Fatalf("key frame = %+v, %v", p, err)
	}
	p, err = ReadPacket(&stream)
	if err != nil || p.KeyFrame || p.PTS != 140*time.Millisecond {
		t.Fatalf("frame = %+v, %v", p, err)
	}
	if _, err := ReadPacket(&stream); err != io.EOF {
		t.Fatalf("end = %v", err)
	}
	// A truncated packet is an unexpected EOF.
	trunc := bytes.NewReader(append(header(0, 10), 1, 2))
	if _, err := ReadPacket(trunc); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated = %v", err)
	}
	if _, err := ReadPacket(bytes.NewReader(header(0, maxPacket+1))); err == nil {
		t.Fatal("oversized packet accepted")
	}
}

func TestStreamHeader(t *testing.T) {
	name := make([]byte, deviceNameLength)
	copy(name, "Mi 10 Pro")
	r := bytes.NewReader(append(name, 0x68, 0x32, 0x36, 0x34))
	n, err := readDeviceName(r)
	if err != nil || n != "Mi 10 Pro" {
		t.Fatalf("name = %q, %v", n, err)
	}
	c, err := readCodec(r)
	if err != nil || c != CodecH264 || CodecName(c) != "h264" {
		t.Fatalf("codec = %#x, %v", c, err)
	}
	if _, err := readCodec(bytes.NewReader([]byte{0, 0, 0, 1})); err == nil {
		t.Error("configuration error not reported")
	}
}

func TestReadDeviceMessage(t *testing.T) {
	var b bytes.Buffer
	b.Write([]byte{DeviceClipboard, 0, 0, 0, 5})
	b.WriteString("hello")
	b.Write([]byte{DeviceAckClipboard, 0, 0, 0, 0, 0, 0, 0, 9})
	b.Write([]byte{DeviceUhidOutput, 0, 2, 0, 1, 0xaa})
	m, err := ReadDeviceMessage(&b)
	if err != nil || m.Type != DeviceClipboard || m.Text != "hello" {
		t.Fatalf("clipboard = %+v, %v", m, err)
	}
	m, err = ReadDeviceMessage(&b)
	if err != nil || m.Type != DeviceAckClipboard || m.Sequence != 9 {
		t.Fatalf("ack = %+v, %v", m, err)
	}
	m, err = ReadDeviceMessage(&b)
	if err != nil || m.Type != DeviceUhidOutput || m.ID != 2 || !bytes.Equal(m.Data, []byte{0xaa}) {
		t.Fatalf("uhid = %+v, %v", m, err)
	}
	if _, err := ReadDeviceMessage(bytes.NewReader([]byte{42})); err == nil {
		t.Error("unknown type accepted")
	}
	if _, err := ReadDeviceMessage(bytes.NewReader([]byte{DeviceClipboard, 0, 0})); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("truncated = %v", err)
	}
}

func TestParseAppList(t *testing.T) {
	out := "[server] INFO: Device: [Google] google Pixel (Android 10)\n" +
		"[server] INFO: Processing Android apps... (this may take some time)\n" +
		"[server] INFO: List of apps:\n" +
		" * Calculator                     com.android.calculator2\n" +
		" * \u8bbe\u7f6e                           com.android.settings\n" +
		" - A very long application name indeed\n" +
		"                                  com.example.longname\n" +
		" - Play \u5546\u5e97                        com.android.vending\n" +
		" - X                              com.twitter.android\n"
	apps, ok := ParseAppList(out)
	if !ok {
		t.Fatal("not parsed")
	}
	want := []App{
		{"Calculator", "com.android.calculator2", true},
		{"\u8bbe\u7f6e", "com.android.settings", true},
		{"A very long application name indeed", "com.example.longname", false},
		{"Play \u5546\u5e97", "com.android.vending", false},
		{"X", "com.twitter.android", false},
	}
	if !reflect.DeepEqual(apps, want) {
		t.Errorf("apps =\n%+v\nwant\n%+v", apps, want)
	}
	if _, ok := ParseAppList("[server] ERROR: something"); ok {
		t.Error("output without a list parsed")
	}
}

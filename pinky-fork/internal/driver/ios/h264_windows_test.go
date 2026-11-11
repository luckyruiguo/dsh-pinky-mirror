//go:build windows

package ios

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"golang.org/x/sys/windows"
)

// TestH264Encode encodes one mid-grey picture and looks at what FFmpeg
// returned: Annex B, a key frame, and the SPS and PPS when the encoder
// keeps them apart from its pictures.
func TestH264Encode(t *testing.T) {
	if err := h264Available(); err != nil {
		t.Skipf("no H.264 encoder to encode with: %v", err)
	}
	const w, h = 64, 48
	e, err := newH264Encoder(w, h, 15, 1_000_000)
	if err != nil {
		t.Fatalf("newH264Encoder: %v", err)
	}
	defer e.Close()
	t.Logf("encoding with FFmpeg's %s encoder", e.name)

	// A mid-grey picture: NV12's Y plane, then Cb and Cr interleaved.
	y := bytes.Repeat([]byte{128}, w*h)
	uv := bytes.Repeat([]byte{128}, w*h/2)
	// An encoder may hold its first pictures back while it fills its
	// pipeline, so send a run of them, as the frame stream does. Media
	// Foundation holds back its first twelve pictures: it gives nothing
	// back for them and starts with the thirteenth.
	var out []encodedFrame
	started := -1 // the picture the encoder began giving frames back on
	packets := 0  // the frames it gave back from there on
	for i := 0; i < 30; i++ {
		frames, err := e.encode(y, uv, time.Duration(i)*time.Second/15, i == 0)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		t.Logf("picture %d: %d frames back", i, len(frames))
		if started < 0 && len(frames) > 0 {
			started = i
		}
		if started >= 0 {
			packets++
			if len(frames) != 1 {
				t.Errorf("picture %d: the encoder gave back %d frames, want one", i, len(frames))
			}
		}
		if len(out) == 0 && len(frames) > 0 {
			out = frames
		}
	}
	if len(out) == 0 {
		t.Fatal("the encoder returned no frame for thirty pictures")
	}
	t.Logf("the encoder began at picture %d and gave back %d frames for %d pictures",
		started, packets, 30-started)
	f := out[0]
	if !f.keyFrame {
		t.Error("the first frame, which asked for a key frame, is not one")
	}
	if !bytes.HasPrefix(f.data, []byte{0, 0, 0, 1}) {
		t.Errorf("the picture does not begin with an Annex B start code: % x", f.data)
	}
	switch {
	case len(f.config) > 0:
		for _, typ := range []byte{7, 8} {
			if !annexBHasNAL(f.config, typ) {
				t.Errorf("config lacks the NAL unit of type %d: % x", typ, f.config)
			}
		}
	case annexBHasNAL(f.data, 7) && annexBHasNAL(f.data, 8):
		t.Log("the encoder keeps no extradata; the SPS and PPS travel in the key frame")
	default:
		t.Log("the encoder gave neither extradata nor the SPS and PPS; the decoder takes them from later pictures")
	}

	// A second picture, without asking for a key frame, keeps the encoder
	// going.
	if _, err := e.encode(y, uv, 66*time.Millisecond, false); err != nil {
		t.Fatalf("second encode: %v", err)
	}
}

// TestH264AvailableWithoutFFmpeg points Pinky at an empty folder, where
// there is no FFmpeg, and asks what is missing. FFmpeg is also loaded from
// the executable's folder and the system's library path, so the test is
// only meaningful where it is nowhere else: it is skipped when a test
// before it has already loaded FFmpeg into this process, since Windows
// hands back a loaded library by its name and would find it regardless of
// where it came from.
func TestH264AvailableWithoutFFmpeg(t *testing.T) {
	for _, name := range []string{"avutil-61.dll", "avcodec-63.dll"} {
		var h windows.Handle
		err := windows.GetModuleHandleEx(windows.GET_MODULE_HANDLE_EX_FLAG_UNCHANGED_REFCOUNT,
			windows.StringToUTF16Ptr(name), &h)
		if err == nil && h != 0 {
			t.Skipf("%s is loaded already; run this test alone to see what Pinky reports without FFmpeg", name)
		}
	}
	empty := t.TempDir()
	t.Setenv("PINKY_FFMPEG_DIR", empty)
	if l, err := loadFFmpeg(); err == nil {
		t.Skipf("FFmpeg is also in %s, so the test cannot hide it", strings.Join(l.dirs, ", "))
	}
	err := h264Available()
	if err == nil {
		t.Fatal("h264Available found an encoder in an empty folder")
	}
	if !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("h264Available does not wrap driver.ErrUnsupported: %v", err)
	}
	if !strings.Contains(err.Error(), empty) {
		t.Errorf("h264Available does not say where it looked (%s): %v", empty, err)
	}
	if !strings.Contains(err.Error(), "avutil-61.dll") {
		t.Errorf("h264Available does not say what is missing: %v", err)
	}
	if strings.Contains(err.Error(), "macOS") {
		t.Errorf("h264Available mentions macOS on Windows: %v", err)
	}
}

func TestExtradataToAnnexB(t *testing.T) {
	// An AVCDecoderConfigurationRecord with one SPS of 4 bytes and one PPS
	// of 2, as an MP4 file carries it.
	avcc := []byte{
		1, 0x4d, 0x40, 0x1f, 0xff, // version, profile, compatibility, level, lengthSizeMinusOne
		0xe1,                      // one SPS
		0x00, 0x04, 0x67, 1, 2, 3, // its length and bytes
		0x01,                // one PPS
		0x00, 0x02, 0x68, 4, // its length and bytes
	}
	got := extradataToAnnexB(avcc)
	want := []byte{
		0, 0, 0, 1, 0x67, 1, 2, 3,
		0, 0, 0, 1, 0x68, 4,
	}
	if !bytes.Equal(got, want) {
		t.Errorf("avcC: got % x, want % x", got, want)
	}
	if !annexBHasNAL(got, 7) || !annexBHasNAL(got, 8) {
		t.Errorf("avcC: no SPS or no PPS in % x", got)
	}

	// Media Foundation gives the NAL units themselves, with start codes.
	annexB := []byte{0, 0, 0, 1, 0x67, 1, 2, 3, 0, 0, 1, 0x68, 4}
	if got := extradataToAnnexB(annexB); !bytes.Equal(got, annexB) {
		t.Errorf("Annex B extradata: got % x, want it unchanged", got)
	}

	// Anything else is no extradata at all.
	for _, b := range [][]byte{nil, {0x42}, {2, 3, 4, 5, 6, 7, 8}} {
		if got := extradataToAnnexB(b); got != nil {
			t.Errorf("extradata % x: got % x, want nothing", b, got)
		}
	}
}

func TestAnnexBStart(t *testing.T) {
	for _, c := range []struct {
		b    []byte
		want int
	}{
		{[]byte{0, 0, 0, 1, 0x67}, 4},
		{[]byte{0, 0, 1, 0x67}, 3},
		{[]byte{0, 0, 0, 2, 0x67}, 0},
		{[]byte{0x67}, 0},
		{nil, 0},
	} {
		if got := annexBStart(c.b); got != c.want {
			t.Errorf("annexBStart(% x) = %d, want %d", c.b, got, c.want)
		}
	}
}

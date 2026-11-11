package ios

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// fakeStream is a stream fed by hand: what the receive loop would pass on.
type fakeStream struct {
	t   *testing.T
	m   *media
	seq uint16
}

func newFakeStream(t *testing.T) *fakeStream {
	m := &media{display: 1, started: time.Now(), done: make(chan struct{}), ready: make(chan struct{}),
		subs: map[*subscriber]struct{}{}, needKey: true}
	return &fakeStream{t: t, m: m}
}

// rtp feeds one RTP packet; a marker ends the frame at ts.
func (f *fakeStream) rtp(pt uint8, marker bool, ts uint32, payload []byte) {
	f.t.Helper()
	f.seq++
	raw := rtpPkt(marker, pt, f.seq, ts, payload)
	p, err := parseRTP(raw)
	if err != nil {
		f.t.Fatal(err)
	}
	f.m.onRTP(p, len(raw))
}

// sr feeds an RTCP sender report.
func (f *fakeStream) sr() {
	b := make([]byte, 28)
	b[0], b[1] = 0x80, 200
	binary.BigEndian.PutUint16(b[2:], 6)
	f.m.onRTCP(b)
}

// wire is the wire summary the timeout would log.
func (f *fakeStream) wire() string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	err := f.m.waitReady(ctx)
	var nv *noVideoError
	if !errors.As(err, &nv) || len(nv.tries) != 1 || !errors.Is(err, context.DeadlineExceeded) {
		f.t.Fatalf("waitReady: %v", err)
	}
	return nv.tries[0].Wire
}

// hevcParams is the parameter sets of a 1136x2464 HEVC stream in one
// aggregation packet.
func hevcParams(sps []byte) []byte {
	ap := []byte{hevcAP << 1, 1}
	for _, n := range [][]byte{hevcNAL(hevcVPS, 1), sps, hevcNAL(hevcPPS, 2)} {
		ap = binary.BigEndian.AppendUint16(ap, uint16(len(n)))
		ap = append(ap, n...)
	}
	return ap
}

func TestNoVideoRTCPOnly(t *testing.T) {
	f := newFakeStream(t)
	for range 3 {
		f.sr()
	}
	if got := f.wire(); got != "0 RTP packets (0 bytes), 3 RTCP" {
		t.Errorf("got %q", got)
	}
}

// Packets whose payload is neither HEVC nor H.264 (the forbidden bit set):
// Pinky cannot read the stream at all.
func TestNoVideoUnreadable(t *testing.T) {
	f := newFakeStream(t)
	for i := range 4 {
		f.rtp(96, true, uint32(i*1500), []byte{0x80, 0x01, 0x02})
	}
	got := f.wire()
	for _, want := range []string{
		"4 RTP packets (60 bytes), 0 RTCP", "payload types 96×4", "payloads read as neither HEVC nor H.264",
		"0 access units", "no parameter sets", "0 key frames",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lacks %q", got, want)
		}
	}
}

// Frames come, but never a key frame: Pinky passes them over and asks for
// one.
func TestNoVideoNoKeyFrame(t *testing.T) {
	f := newFakeStream(t)
	f.rtp(100, false, 0, hevcParams(testHEVCSPS(1136, 2464, 0)))
	f.rtp(100, true, 0, hevcNAL(1, 0xAA))
	for i := 1; i <= 2; i++ {
		f.rtp(100, true, uint32(i*1500), hevcNAL(1, 0xBB))
	}
	got := f.wire()
	for _, want := range []string{
		"payload types 100×4", "read as hevc", "3 access units", "NAL types 1×3 32×1 33×1 34×1",
		"parameter sets for 1136x2464", "0 key frames", "3 frames passed over waiting for a key frame",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lacks %q", got, want)
		}
	}
}

// A key frame without parameter sets before it cannot be decoded.
func TestNoVideoKeyFrameWithoutConfig(t *testing.T) {
	f := newFakeStream(t)
	f.rtp(100, true, 0, hevcNAL(19, 0xAA))
	f.rtp(100, true, 1500, hevcNAL(1, 0xBB))
	got := f.wire()
	for _, want := range []string{"read as hevc", "no parameter sets", "1 key frames", "1 key frames before parameter sets"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lacks %q", got, want)
		}
	}
}

// An SPS Pinky cannot read leaves the picture without a size.
func TestNoVideoBadSPS(t *testing.T) {
	f := newFakeStream(t)
	f.rtp(100, false, 0, hevcParams(hevcNAL(hevcSPS, 0xFF)))
	f.rtp(100, true, 0, hevcNAL(1, 0xAA))
	got := f.wire()
	for _, want := range []string{"parameter sets without a size", "1 unreadable SPS"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lacks %q", got, want)
		}
	}
}

// After the first picture, the counts of NAL types stop: they are for
// telling why no picture came.
func TestNoVideoReadyStopsCounting(t *testing.T) {
	f := newFakeStream(t)
	f.rtp(100, false, 0, hevcParams(testHEVCSPS(1136, 2464, 0)))
	f.rtp(100, true, 0, hevcNAL(19, 0xAA))
	select {
	case <-f.m.ready:
	default:
		t.Fatal("no picture")
	}
	f.rtp(100, true, 1500, hevcNAL(1, 0xBB))
	if n := f.m.wire.NALTypes[1]; n != 0 || f.m.wire.Units != 2 {
		t.Fatalf("NAL types %v, units %d", f.m.wire.NALTypes, f.m.wire.Units)
	}
}

func TestWireSummary(t *testing.T) {
	now := time.Now()
	w := wireStats{
		NotRTP: 2, PayloadTypes: map[uint8]int{100: 40, 96: 1}, Codec: "h264", Unreadable: 3, Units: 12,
		NALTypes: map[int]int{1: 11, 7: 1}, WaitedKey: 11,
	}
	st := mediaStats{Packets: 41, Bytes: 51234, Lost: 2, Dropped: 1, FIRs: 4}
	got := wireSummary(w, st, 5, true, 0, 0, now.Add(-1520*time.Millisecond), now)
	want := "41 RTP packets (51234 bytes), 5 RTCP, 2 not RTP, the last 1.5s ago, payload types 96×1 100×40, " +
		"read as h264 after 3 unreadable payloads, 12 access units, NAL types 1×11 7×1, parameter sets without a size, " +
		"0 key frames, 11 frames passed over waiting for a key frame, 2 packets lost, 1 frames dropped incomplete, 4 key frame requests"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if got := wireSummary(wireStats{NotRTP: 7}, mediaStats{}, 0, false, 0, 0, time.Time{}, now); got != "0 RTP packets (0 bytes), 0 RTCP, 7 not RTP" {
		t.Errorf("nothing readable: %q", got)
	}
}

func TestAnswerSummary(t *testing.T) {
	raw := map[string]any{"connection": map[string]any{"streamConfig": map[string]any{
		"RxPayloadType": uint64(100),
		"CustomWidth":   map[string]any{"int": int64(1136)},
		"CustomHeight":  2448.0,
		"Framerate":     int64(60),
		"VideoCodec":    "HEVC",
		"SourcePort":    uint64(51000),
		"LocalSSRC":     []byte("0123456789abcdefXYZ"),
	}}}
	if got := answerSummary(raw); got != "payload 100, 1136x2448, 60 fps, VideoCodec HEVC" {
		t.Errorf("summary %q", got)
	}
	want := "CustomHeight=2448 CustomWidth=1136 Framerate=60 LocalSSRC=<19 bytes> RxPayloadType=100 SourcePort=51000 VideoCodec=HEVC"
	if got := answerConfig(raw); got != want {
		t.Errorf("config\n got %q\nwant %q", got, want)
	}
	if got := answerSummary(map[string]any{"connection": map[string]any{}}); got != "without a stream config" {
		t.Errorf("no config: %q", got)
	}
	if answerSummary(nil) != "" || answerConfig(nil) != "" {
		t.Error("no answer")
	}
}

// The checkup says what the last open got, and forgets it once an open
// works or fails otherwise.
func TestProviderOpenChecks(t *testing.T) {
	p := NewProvider()
	if p.OpenChecks("duo") != nil {
		t.Fatal("checks before any open")
	}
	nv := (*noVideoError)(nil).with(noVideoTry{Display: 1, Wire: "0 RTP packets (0 bytes), 9 RTCP"}, context.DeadlineExceeded, nil)
	p.noteOpen("duo", errors.Join(errors.New("open device"), nv))
	cs := p.OpenChecks("duo")
	if len(cs) != 1 {
		t.Fatalf("checks %+v", cs)
	}
	c := cs[0]
	if c.ID != driver.CheckScreen || c.Status != driver.CheckFail || c.Fix != driver.FixRestart ||
		c.Detail != "no live picture from the phone. no displays listed. Display 1: 0 RTP packets (0 bytes), 9 RTCP." {
		t.Fatalf("check %+v", c)
	}
	if p.OpenChecks("other") != nil {
		t.Fatal("another phone's checks")
	}
	p.noteOpen("duo", errors.New("lockdown: connection refused"))
	if p.OpenChecks("duo") != nil {
		t.Fatal("kept after another failure")
	}
	p.noteOpen("duo", nv)
	p.noteOpen("duo", nil)
	if p.OpenChecks("duo") != nil {
		t.Fatal("kept after an open that worked")
	}
}

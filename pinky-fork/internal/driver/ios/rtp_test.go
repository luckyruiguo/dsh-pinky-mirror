package ios

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

func rtpPkt(marker bool, pt uint8, seq uint16, ts uint32, payload []byte) []byte {
	b := []byte{0x80, pt, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}
	if marker {
		b[1] |= 0x80
	}
	binary.BigEndian.PutUint16(b[2:], seq)
	binary.BigEndian.PutUint32(b[4:], ts)
	return append(b, payload...)
}

func TestParseRTP(t *testing.T) {
	// CSRC count 1, extension with one word, two bytes of padding.
	b := []byte{0xB1, 0xE4, 0x12, 0x34, 0, 0, 0x5D, 0xC0, 0xAA, 0xBB, 0xCC, 0xDD}
	b = append(b, 1, 2, 3, 4)                   // CSRC
	b = append(b, 0xBE, 0xDE, 0, 1, 9, 9, 9, 9) // extension header + one word
	b = append(b, 'h', 'i', 0, 2)               // payload + 2 bytes of padding
	p, err := parseRTP(b)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Marker || p.PT != 100 || p.Seq != 0x1234 || p.TS != 24000 || p.SSRC != 0xAABBCCDD {
		t.Fatalf("header: %+v", p)
	}
	if string(p.Payload) != "hi" {
		t.Fatalf("payload %q", p.Payload)
	}
	if _, err := parseRTP(b[:11]); err == nil {
		t.Fatal("short packet accepted")
	}
	if !isRTCP([]byte{0x81, 200, 0, 6}) || isRTCP(b) {
		t.Fatal("RTCP detection")
	}
}

// hevcNAL builds an HEVC NAL unit of the given type with a body.
func hevcNAL(typ byte, body ...byte) []byte { return append([]byte{typ << 1, 1}, body...) }

func TestDepacketizeHEVC(t *testing.T) {
	vps, sps, pps := hevcNAL(hevcVPS, 1, 2), hevcNAL(hevcSPS, 3), hevcNAL(hevcPPS, 4, 5, 6)
	ap := []byte{hevcAP << 1, 1}
	for _, n := range [][]byte{vps, sps, pps} {
		ap = binary.BigEndian.AppendUint16(ap, uint16(len(n)))
		ap = append(ap, n...)
	}
	var d depacketizer
	var out [][]byte
	out = d.push(ap, out)
	if d.codec != "hevc" || len(out) != 3 || !bytes.Equal(out[0], vps) || !bytes.Equal(out[2], pps) {
		t.Fatalf("aggregation packet: codec %q, %x", d.codec, out)
	}

	// An IDR slice (type 19) in three fragments, the last with the
	// display service's trailer.
	idr := hevcNAL(19, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE)
	fu := func(start, end bool, body ...byte) []byte {
		h := byte(19)
		if start {
			h |= 0x80
		}
		if end {
			h |= 0x40
		}
		return append([]byte{hevcFU << 1, 1, h}, body...)
	}
	out = out[:0]
	out = d.push(fu(true, false, 0xAA, 0xBB), out)
	out = d.push(fu(false, false, 0xCC), out)
	if len(out) != 0 {
		t.Fatal("fragment emitted early")
	}
	last := append([]byte{0xDD, 0xEE}, displayTrailer...)
	out = d.push(fu(false, true, last...), out)
	if len(out) != 1 || !bytes.Equal(out[0], idr) {
		t.Fatalf("reassembled %x, want %x", out, idr)
	}
	if _, key := nalInfo("hevc", out[0]); !key {
		t.Fatal("IDR not a keyframe")
	}

	// A lost middle fragment drops the NAL unit.
	out = out[:0]
	out = d.push(fu(true, false, 1), out)
	d.reset()
	out = d.push(fu(false, true, 2), out)
	if len(out) != 0 {
		t.Fatalf("broken fragment emitted %x", out)
	}

	// A single NAL unit passes through; the trailer goes.
	trail := hevcNAL(1, 7, 7)
	out = d.push(append(append([]byte(nil), trail...), displayTrailer...), out[:0])
	if len(out) != 1 || !bytes.Equal(out[0], trail) {
		t.Fatalf("single NAL %x", out)
	}
	if c, k := nalInfo("hevc", trail); c || k {
		t.Fatal("trailing picture classified as config or key")
	}
}

func TestDepacketizeH264(t *testing.T) {
	sps, pps := []byte{0x67, 0x42, 0x00, 0x1f}, []byte{0x68, 0xCE}
	stap := []byte{0x78}
	for _, n := range [][]byte{sps, pps} {
		stap = binary.BigEndian.AppendUint16(stap, uint16(len(n)))
		stap = append(stap, n...)
	}
	var d depacketizer
	out := d.push(stap, nil)
	if d.codec != "h264" || len(out) != 2 || !bytes.Equal(out[0], sps) {
		t.Fatalf("STAP-A: codec %q %x", d.codec, out)
	}
	out = d.push([]byte{0x7C, 0x85, 1, 2}, out[:0]) // FU-A start, IDR
	out = d.push([]byte{0x7C, 0x45, 3}, out)        // FU-A end
	if len(out) != 1 || !bytes.Equal(out[0], []byte{0x65, 1, 2, 3}) {
		t.Fatalf("FU-A %x", out)
	}
	if c, k := nalInfo("h264", out[0]); c || !k {
		t.Fatal("IDR classification")
	}
}

func TestAnnexB(t *testing.T) {
	got := annexB([][]byte{{1, 2}, {3}})
	want := []byte{0, 0, 0, 1, 1, 2, 0, 0, 0, 1, 3}
	if !bytes.Equal(got, want) {
		t.Fatalf("%x", got)
	}
}

// bitWriter writes the fields an SPS test needs.
type bitWriter struct {
	b    []byte
	nbit int
}

func (w *bitWriter) u(n int, v uint32) {
	for i := n - 1; i >= 0; i-- {
		if w.nbit%8 == 0 {
			w.b = append(w.b, 0)
		}
		if v>>uint(i)&1 == 1 {
			w.b[len(w.b)-1] |= 1 << (7 - w.nbit%8)
		}
		w.nbit++
	}
}

func (w *bitWriter) ue(v uint32) {
	v++
	n := 0
	for x := v; x > 1; x >>= 1 {
		n++
	}
	w.u(n, 0)
	w.u(n+1, v)
}

// escape inserts emulation prevention bytes.
func escape(b []byte) []byte {
	var out []byte
	zeros := 0
	for _, x := range b {
		if zeros >= 2 && x <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, x)
		if x == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

func testHEVCSPS(w, h, cropBottom uint32) []byte {
	var bw bitWriter
	bw.u(4, 0)     // vps id
	bw.u(3, 0)     // max sub layers minus 1
	bw.u(1, 1)     // temporal id nesting
	bw.u(2+1+5, 1) // profile space, tier, profile idc (Main)
	bw.u(32, 0x60000000)
	bw.u(4, 0b1001)
	bw.u(43, 0)
	bw.u(1, 0)
	bw.u(8, 150) // level
	bw.ue(0)     // sps id
	bw.ue(1)     // 4:2:0
	bw.ue(w)
	bw.ue(h)
	if cropBottom > 0 {
		bw.u(1, 1)
		bw.ue(0)
		bw.ue(0)
		bw.ue(0)
		bw.ue(cropBottom)
	} else {
		bw.u(1, 0)
	}
	bw.u(8, 0xFF) // trailing bits for the parser to stop on
	return append([]byte{hevcSPS << 1, 1}, escape(bw.b)...)
}

func TestHEVCSPSSize(t *testing.T) {
	w, h, err := hevcSPSSize(testHEVCSPS(1136, 2464, 0))
	if err != nil || w != 1136 || h != 2464 {
		t.Fatalf("got %dx%d %v", w, h, err)
	}
	// A conformance window of 8 luma rows at 4:2:0 is 16 lines.
	w, h, err = hevcSPSSize(testHEVCSPS(1920, 1088, 4))
	if err != nil || w != 1920 || h != 1080 {
		t.Fatalf("cropped: got %dx%d %v", w, h, err)
	}
	if _, _, err := hevcSPSSize([]byte{hevcSPS << 1, 1, 0}); err == nil {
		t.Fatal("truncated SPS accepted")
	}
}

func TestH264SPSSize(t *testing.T) {
	var bw bitWriter
	bw.u(8, 66) // baseline: no chroma fields
	bw.u(8, 0)
	bw.u(8, 31)
	bw.ue(0) // sps id
	bw.ue(0) // log2_max_frame_num_minus4
	bw.ue(0) // poc type 0
	bw.ue(0) // log2_max_poc_lsb_minus4
	bw.ue(1) // max ref frames
	bw.u(1, 0)
	bw.ue(1280/16 - 1)
	bw.ue(720/16 - 1)
	bw.u(1, 1) // frame mbs only
	bw.u(1, 1) // direct 8x8
	bw.u(1, 0) // no cropping
	bw.u(8, 0xFF)
	w, h, err := h264SPSSize(append([]byte{0x67}, escape(bw.b)...))
	if err != nil || w != 1280 || h != 720 {
		t.Fatalf("got %dx%d %v", w, h, err)
	}
}

// TestMediaAccessUnits feeds RTP packets through the receiver and checks
// what a subscriber gets: the parameter sets as a config packet, then the
// keyframe, then a later frame with its time.
func TestMediaAccessUnits(t *testing.T) {
	m := &media{done: make(chan struct{}), ready: make(chan struct{}), subs: map[*subscriber]struct{}{}, needKey: true}
	sub := &subscriber{ch: make(chan driver.Packet, 16), needKey: true}
	m.subs[sub] = struct{}{}

	sps := testHEVCSPS(1136, 2464, 0)
	ap := []byte{hevcAP << 1, 1}
	for _, n := range [][]byte{hevcNAL(hevcVPS, 1), sps, hevcNAL(hevcPPS, 2)} {
		ap = binary.BigEndian.AppendUint16(ap, uint16(len(n)))
		ap = append(ap, n...)
	}
	feed := func(raw []byte) {
		p, err := parseRTP(raw)
		if err != nil {
			t.Fatal(err)
		}
		m.onRTP(p, len(raw))
	}
	feed(rtpPkt(false, 100, 10, 0, ap))
	feed(rtpPkt(true, 100, 11, 0, hevcNAL(19, 0xAA)))
	feed(rtpPkt(true, 100, 12, 400, hevcNAL(1, 0xBB)))

	want := []struct {
		config, key bool
		pts         time.Duration
	}{{true, false, 0}, {false, true, 0}, {false, false, time.Second / 60}}
	for i, w := range want {
		select {
		case p := <-sub.ch:
			if p.Config != w.config || p.KeyFrame != w.key || p.PTS != w.pts || !bytes.HasPrefix(p.Data, []byte{0, 0, 0, 1}) {
				t.Fatalf("packet %d: config=%v key=%v pts=%v", i, p.Config, p.KeyFrame, p.PTS)
			}
		default:
			t.Fatalf("packet %d missing", i)
		}
	}
	if w, h := m.size(); w != 1136 || h != 2464 {
		t.Fatalf("size %dx%d", w, h)
	}
	select {
	case <-m.ready:
	default:
		t.Fatal("not ready after a keyframe")
	}

	// A lost packet drops the frame and waits for the next keyframe.
	feed(rtpPkt(true, 100, 14, 800, hevcNAL(1, 0xCC)))
	feed(rtpPkt(true, 100, 15, 1200, hevcNAL(1, 0xDD)))
	select {
	case p := <-sub.ch:
		t.Fatalf("frame after loss delivered: %+v", p)
	default:
	}
	st := m.snapshot()
	if st.Lost != 1 || st.Dropped != 1 {
		t.Fatalf("stats %+v", st)
	}
}

// TestMediaLossAcrossFrames loses the end of one frame and the first slice
// of the next keyframe: the keyframe must not go out without its slice.
func TestMediaLossAcrossFrames(t *testing.T) {
	m := &media{done: make(chan struct{}), ready: make(chan struct{}), subs: map[*subscriber]struct{}{}, needKey: true}
	sub := &subscriber{ch: make(chan driver.Packet, 16), needKey: true}
	m.subs[sub] = struct{}{}
	ap := []byte{hevcAP << 1, 1}
	for _, n := range [][]byte{hevcNAL(hevcVPS, 1), testHEVCSPS(1136, 2464, 0), hevcNAL(hevcPPS, 2)} {
		ap = binary.BigEndian.AppendUint16(ap, uint16(len(n)))
		ap = append(ap, n...)
	}
	feed := func(raw []byte) {
		p, err := parseRTP(raw)
		if err != nil {
			t.Fatal(err)
		}
		m.onRTP(p, len(raw))
	}
	feed(rtpPkt(false, 100, 1, 0, ap))
	feed(rtpPkt(true, 100, 2, 0, hevcNAL(19, 0xAA))) // keyframe
	feed(rtpPkt(false, 100, 3, 400, hevcNAL(1, 0x01)))
	// seq 4 (the end of the TS 400 frame) and 5 (the first slice of the TS
	// 800 keyframe) are lost.
	feed(rtpPkt(true, 100, 6, 800, hevcNAL(19, 0xBB)))
	got := 0
	for {
		select {
		case p := <-sub.ch:
			got++
			if p.KeyFrame && bytes.Contains(p.Data, []byte{0xBB}) {
				t.Fatal("a keyframe missing a slice was delivered")
			}
			continue
		default:
		}
		break
	}
	if got != 2 { // the config and the first keyframe
		t.Fatalf("%d packets", got)
	}
	if st := m.snapshot(); st.Lost != 2 || st.Dropped != 2 {
		t.Fatalf("stats %+v", st)
	}
}

func TestJitterAcrossTimestampWrap(t *testing.T) {
	m := &media{started: time.Now().Add(-time.Second)}
	base := uint32(0xFFFFFF00)
	now := m.started.Add(time.Second)
	for i := 0; i < 50; i++ {
		ts := base + uint32(i*400)
		at := now.Add(time.Duration(i) * time.Second / 60)
		if ok, _ := m.track(rtpPacket{Seq: uint16(i), TS: ts}, at); !ok {
			t.Fatal("packet dropped")
		}
	}
	if m.jitter > 100 {
		t.Fatalf("jitter %.0f clock units across the wrap", m.jitter)
	}
}

package ios

import (
	"encoding/binary"
	"testing"
	"time"
)

// rrLoss reads the fraction lost and the cumulative loss from a receiver
// report's first report block.
func rrLoss(t *testing.T, rr []byte) (uint8, int32, uint32) {
	t.Helper()
	if len(rr) < 32 || rr[1] != 201 {
		t.Fatalf("not a receiver report: % x", rr)
	}
	lost := int32(rr[13])<<16 | int32(rr[14])<<8 | int32(rr[15])
	if lost&0x800000 != 0 {
		lost -= 1 << 24
	}
	return rr[12], lost, binary.BigEndian.Uint32(rr[16:])
}

func TestReceiverReportLoss(t *testing.T) {
	now := time.Now()
	m := &media{started: now}
	m.ans.HostSSRC, m.ans.DeviceSSRC = 1, 2
	feed := func(seqs ...uint16) {
		for _, s := range seqs {
			m.track(rtpPacket{Seq: s}, now)
		}
	}
	// Nothing received yet: no loss, as before.
	if f, l, _ := rrLoss(t, m.receiverReport()); f != 0 || l != 0 {
		t.Fatalf("empty: %d %d", f, l)
	}
	// 1..20 with 11 and 12 missing.
	for s := uint16(1); s <= 20; s++ {
		if s != 11 && s != 12 {
			feed(s)
		}
	}
	f, l, ext := rrLoss(t, m.receiverReport())
	if f != 25 || l != 2 || ext != 20 { // 2 of 20 = 25.6/256
		t.Fatalf("after a gap: fraction %d, lost %d, highest %d", f, l, ext)
	}
	// 21..30, nothing lost in this interval: the fraction goes back to 0.
	for s := uint16(21); s <= 30; s++ {
		feed(s)
	}
	if f, l, _ := rrLoss(t, m.receiverReport()); f != 0 || l != 2 {
		t.Fatalf("clean interval: fraction %d, lost %d", f, l)
	}
	// Over USB nothing is lost and the report stays what it was before
	// loss was counted.
	clean := &media{started: now}
	for s := uint16(100); s < 200; s++ {
		clean.track(rtpPacket{Seq: s}, now)
	}
	if f, l, _ := rrLoss(t, clean.receiverReport()); f != 0 || l != 0 {
		t.Fatalf("no loss: fraction %d, lost %d", f, l)
	}
}

func TestReceiverReportWrap(t *testing.T) {
	now := time.Now()
	m := &media{started: now}
	for _, s := range []uint16{65533, 65534, 65535, 2, 3} { // 0 and 1 lost across the wrap
		m.track(rtpPacket{Seq: s}, now)
	}
	f, l, ext := rrLoss(t, m.receiverReport())
	if l != 2 || ext != 1<<16|3 || f != 73 { // 2 of 7
		t.Fatalf("wrap: fraction %d, lost %d, highest %#x", f, l, ext)
	}
}

func TestLossPriorClamps(t *testing.T) {
	var p lossPrior
	if _, lost := p.update(10, 1, 20); lost != -10 { // duplicates make it negative
		t.Fatalf("negative loss %d", lost)
	}
	var q lossPrior
	if _, lost := q.update(1<<30, 0, 0); lost != 0x7fffff {
		t.Fatalf("clamp %d", lost)
	}
}

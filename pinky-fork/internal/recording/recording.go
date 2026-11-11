// Package recording writes a phone's video stream to MP4 files without
// re-encoding, and plays them back as a driver.Stream, so the client shows
// a recording with the same decoder and view as the live mirror.
//
// Files are fragmented MP4 (an init segment, then a fragment about every
// second), so a crash loses at most the fragment being written and leaves a
// file that players open. H.264 is stored as avc1 and HEVC as hvc1, with the
// first parameter sets in the sample entry; parameter sets that change later
// (a rotation) go into the next key frame's sample, which FFmpeg-based
// players, and Open, handle.
package recording

import (
	"bytes"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/video"
)

// timescale is the recording's clock, 90 kHz as usual for video.
const timescale = 90000

// fragmentLength is how much video a fragment holds before the next one
// starts.
const fragmentLength = time.Second

func ticks(d time.Duration) uint64 {
	if d <= 0 {
		return 0
	}
	return uint64(d / time.Microsecond * timescale / 1e6)
}

func fromTicks(t uint64) time.Duration {
	return time.Duration(t/timescale)*time.Second + time.Duration(t%timescale)*time.Second/timescale
}

// NAL unit types the recorder looks at.
const (
	h264SPS = 7
	h264PPS = 8
	h264AUD = 9
	hevcVPS = 32
	hevcSPS = 33
	hevcPPS = 34
	hevcAUD = 35
)

func isParamSet(codec driver.Codec, t int) bool {
	if codec == driver.HEVC {
		return t == hevcVPS || t == hevcSPS || t == hevcPPS
	}
	return t == h264SPS || t == h264PPS
}

func isAUD(codec driver.Codec, t int) bool {
	if codec == driver.HEVC {
		return t == hevcAUD
	}
	return t == h264AUD
}

// paramSets are a stream's parameter sets.
type paramSets struct {
	vps, sps, pps [][]byte
}

func (p paramSets) empty() bool { return len(p.sps) == 0 || len(p.pps) == 0 }

// add files a parameter set NAL unit under its type.
func (p *paramSets) add(codec driver.Codec, nal []byte) {
	c := append([]byte(nil), nal...)
	switch t := video.NALType(codec, nal); {
	case codec == driver.HEVC && t == hevcVPS:
		p.vps = append(p.vps, c)
	case (codec == driver.HEVC && t == hevcSPS) || (codec != driver.HEVC && t == h264SPS):
		p.sps = append(p.sps, c)
	default:
		p.pps = append(p.pps, c)
	}
}

func (p paramSets) all() [][]byte {
	var out [][]byte
	out = append(out, p.vps...)
	out = append(out, p.sps...)
	return append(out, p.pps...)
}

func (p paramSets) equal(q paramSets) bool {
	a, b := p.all(), q.all()
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

// annexB joins NAL units with four-byte start codes.
func annexB(nals [][]byte) []byte {
	n := 0
	for _, u := range nals {
		n += 4 + len(u)
	}
	out := make([]byte, 0, n)
	for _, u := range nals {
		out = append(out, 0, 0, 0, 1)
		out = append(out, u...)
	}
	return out
}

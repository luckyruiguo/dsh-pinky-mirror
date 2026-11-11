package scrcpy

import (
	"encoding/binary"
	"fmt"
	"io"
	"time"
)

// Packet header flags (Streamer.java).
const (
	flagSession  = uint64(1) << 63
	flagConfig   = uint64(1) << 62
	flagKeyFrame = uint64(1) << 61
	ptsMask      = flagKeyFrame - 1
)

// maxPacket bounds one encoded packet; a 4K key frame is far below it.
const maxPacket = 64 << 20

// Packet is one unit of the video stream: either a session marker or an
// encoded packet.
type Packet struct {
	// Session marks the start of a new encoding session (capture start,
	// rotation, resize or reset): Width and Height are the new video size,
	// and a configuration packet and a key frame follow. Data is nil.
	Session       bool
	Width, Height int
	ClientResize  bool

	Data     []byte // Annex B for H.264 and H.265
	PTS      time.Duration
	Config   bool // codec configuration (SPS/PPS), no PTS
	KeyFrame bool
}

// ReadPacket reads the next packet of a video stream whose header (device
// name and codec id) was already consumed.
func ReadPacket(r io.Reader) (Packet, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Packet{}, err
	}
	ptsFlags := binary.BigEndian.Uint64(hdr[:8])
	if ptsFlags&flagSession != 0 {
		flags := binary.BigEndian.Uint32(hdr[0:4])
		return Packet{
			Session:      true,
			Width:        int(binary.BigEndian.Uint32(hdr[4:8])),
			Height:       int(binary.BigEndian.Uint32(hdr[8:12])),
			ClientResize: flags&1 != 0,
		}, nil
	}
	size := binary.BigEndian.Uint32(hdr[8:12])
	if size > maxPacket {
		return Packet{}, fmt.Errorf("scrcpy: video packet too large (%d bytes)", size)
	}
	p := Packet{Data: make([]byte, size)}
	if _, err := io.ReadFull(r, p.Data); err != nil {
		return Packet{}, unexpected(err)
	}
	if ptsFlags&flagConfig != 0 {
		p.Config = true
	} else {
		p.PTS = time.Duration(ptsFlags&ptsMask) * time.Microsecond
		p.KeyFrame = ptsFlags&flagKeyFrame != 0
	}
	return p, nil
}

// deviceNameLength is the fixed size of the device name the server sends
// first on the first socket.
const deviceNameLength = 64

// readDeviceName reads the NUL-padded device name.
func readDeviceName(r io.Reader) (string, error) {
	var buf [deviceNameLength]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return "", err
	}
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	return string(buf[:n]), nil
}

// readCodec reads the stream's codec id. The server sends 0 when it
// disabled the stream and 1 on a configuration error.
func readCodec(r io.Reader) (uint32, error) {
	var buf [4]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	id := binary.BigEndian.Uint32(buf[:])
	switch id {
	case 0:
		return 0, fmt.Errorf("scrcpy: the server disabled the video stream")
	case 1:
		return 0, fmt.Errorf("scrcpy: the server reported a configuration error")
	}
	return id, nil
}

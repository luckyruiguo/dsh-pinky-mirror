package ios

// Helpers for the H.264 bitstream, Annex B: the start codes that end one
// NAL unit and begin the next, and the SPS and PPS that an encoder keeps
// apart from its pictures. h264_darwin.go and h264_windows.go both hand
// Annex B to the stream, and internal/video takes it as it is.

// annexBStart returns the length of the Annex B start code b begins with,
// 4 for 00 00 00 01, 3 for 00 00 01, and 0 when b does not begin with one.
func annexBStart(b []byte) int {
	switch {
	case len(b) >= 4 && b[0] == 0 && b[1] == 0 && b[2] == 0 && b[3] == 1:
		return 4
	case len(b) >= 3 && b[0] == 0 && b[1] == 0 && b[2] == 1:
		return 3
	}
	return 0
}

// annexBHasNAL reports whether b holds an Annex B NAL unit of the given
// type: 7 is SPS, 8 is PPS, 5 is an IDR picture.
func annexBHasNAL(b []byte, typ byte) bool {
	for i := 0; i+3 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			if b[i+3]&0x1f == typ {
				return true
			}
			i += 2
		}
	}
	return false
}

// extradataToAnnexB turns an encoder's extradata into the SPS and PPS in
// Annex B. Extradata comes in two shapes: the AVCDecoderConfigurationRecord
// an MP4 file carries (it begins with 1), or the NAL units themselves with
// start codes, as Windows' Media Foundation gives them. Anything else
// returns nil, and the caller does without: many encoders put the SPS and
// PPS in every key frame anyway.
func extradataToAnnexB(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	if b[0] == 1 {
		return avccConfigToAnnexB(b)
	}
	if annexBStart(b) > 0 {
		return b
	}
	return nil
}

// avccConfigToAnnexB reads the SPS and PPS out of an
// AVCDecoderConfigurationRecord (ISO/IEC 14496-15, section 5.2.4.1) and
// returns them in Annex B.
func avccConfigToAnnexB(b []byte) []byte {
	if len(b) < 7 || b[0] != 1 {
		return nil
	}
	var out []byte
	add := func(pos, n int) int {
		out = append(out, 0, 0, 0, 1)
		out = append(out, b[pos:pos+n]...)
		return pos + n
	}
	pos := 6 // past the five header bytes and the SPS count
	sps := int(b[5] & 0x1f)
	for i := 0; i < sps; i++ {
		if pos+2 > len(b) {
			return out
		}
		n := int(b[pos])<<8 | int(b[pos+1])
		pos += 2
		if n == 0 || pos+n > len(b) {
			return out
		}
		pos = add(pos, n)
	}
	if pos >= len(b) {
		return out
	}
	pps := int(b[pos])
	pos++
	for i := 0; i < pps; i++ {
		if pos+2 > len(b) {
			return out
		}
		n := int(b[pos])<<8 | int(b[pos+1])
		pos += 2
		if n == 0 || pos+n > len(b) {
			return out
		}
		pos = add(pos, n)
	}
	return out
}

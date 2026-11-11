//go:build windows

package ios

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"github.com/DIYgod/pinky/internal/driver"
)

// h264Encoder encodes NV12 pictures of one size to Annex B H.264 with
// FFmpeg, through the bindings in h264_ffmpeg.go. It is the Windows answer
// to h264_darwin.go's VideoToolbox encoder, and keeps to the same settings:
// Main profile, no B-frames, real time.
//
// One goroutine drives it, as it drives the frame stream.
type h264Encoder struct {
	lib    *ffmpegLib
	name   string         // the FFmpeg encoder in use
	codec  unsafe.Pointer // AVCodec
	ctx    unsafe.Pointer // AVCodecContext
	frame  unsafe.Pointer // the picture, NV12, sent again and again
	pkt    unsafe.Pointer // what the encoder returns
	w, h   int
	fps    int
	pixFmt int32  // the pictures' pixel format: this encoder's own
	opened bool   // the encoder was opened; a context that was not is not flushed
	config []byte // SPS and PPS, Annex B, sent with a key frame

	// times are when the pictures of the last encode were taken, in the
	// order it returned them. The frame stream stamps each packet with its
	// own picture's time instead of the moment the encoder gave it back.
	times     []time.Duration
	lastTicks int64 // the last picture fed, in the encoder's own time base
	haveLast  bool
}

// pictureFormat is the pixel format the encoder takes. Windows' own encoder
// takes NV12, the phone's pictures as they are; Cisco's OpenH264 takes
// planar YUV 4:2:0 only, so its pictures are converted on the way in.
func (e *h264Encoder) pictureFormat() int32 {
	if e.name == "libopenh264" {
		return e.lib.pixYUV420P
	}
	return e.lib.pixNV12
}

// encoderName is the FFmpeg encoder in use, for the log.
func (e *h264Encoder) encoderName() string { return e.name }

// encodedFrame is one picture as the encoder produced it.
type encodedFrame struct {
	data     []byte // Annex B
	config   []byte // SPS and PPS, Annex B; only with key frames
	keyFrame bool
}

// keyFrameEvery is the number of pictures between key frames: two seconds
// of them, so that a viewer who joins the mirror gets a picture of a still
// screen without waiting for the encoder's own interval. The frame stream
// asks for a key frame when the screen has not changed for a while, but a
// viewer can join before that.
const keyFrameEvery = 2

// h264TimeBase is the encoder's time base, and nanosecondTimeBase is Go's.
// Microseconds are fine enough to say when a picture was taken to a
// hundredth of a picture at the stream's rate, and a picture's timestamp
// goes through the encoder and back out unchanged.
var (
	h264TimeBase       = AVRational{Num: 1, Den: 1_000_000}
	nanosecondTimeBase = AVRational{Num: 1, Den: 1_000_000_000}
)

// avNoPTS is AV_NOPTS_VALUE, what FFmpeg puts in a timestamp it has none for.
const avNoPTS int64 = -1 << 63

// newH264Encoder starts a real-time H.264 encoder (Main profile, no
// B-frames) for pictures of w by h, which must be even.
func newH264Encoder(w, h, fps, bitRate int) (*h264Encoder, error) {
	if w <= 0 || h <= 0 || w%2 != 0 || h%2 != 0 {
		return nil, fmt.Errorf("H.264: bad picture size %dx%d", w, h)
	}
	if fps <= 0 {
		return nil, fmt.Errorf("H.264: bad frame rate %d", fps)
	}
	if bitRate <= 0 {
		return nil, fmt.Errorf("H.264: bad bit rate %d", bitRate)
	}
	l, err := loadFFmpeg()
	if err != nil {
		return nil, h264Unsupported(err)
	}
	codec, name, err := l.h264Encoder()
	if err != nil {
		return nil, h264Unsupported(err)
	}
	e := &h264Encoder{lib: l, name: name, codec: codec, w: w, h: h, fps: fps}
	e.pixFmt = e.pictureFormat()
	if err := e.open(bitRate); err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}

// open allocates the encoder and opens it with Pinky's settings.
func (e *h264Encoder) open(bitRate int) error {
	l := e.lib
	ctx := l.avcodec_alloc_context3(e.codec)
	if ctx == nil {
		return errors.New("H.264: FFmpeg could not allocate an encoder")
	}
	e.ctx = ctx
	putInt32(ctx, ctxWidth, int32(e.w))
	putInt32(ctx, ctxHeight, int32(e.h))
	putInt32(ctx, ctxPixFmt, e.pixFmt)
	putRational(ctx, ctxTimeBase, h264TimeBase)
	putRational(ctx, ctxFramerate, AVRational{Num: int32(e.fps), Den: 1})
	putInt64(ctx, ctxBitRate, int64(bitRate))
	putInt32(ctx, ctxGOPSize, int32(e.fps*keyFrameEvery))
	putInt32(ctx, ctxMaxBFrames, 0)
	putInt32(ctx, ctxProfile, avProfileH264Main)
	// Let each picture's duration reach the encoder: encode sets it, and
	// Windows' H.264 encoder holds every picture back until it is told how
	// long they last. Low delay asks for real time, giving each picture back
	// as soon as it is encoded rather than batching them for quality.
	putInt32(ctx, ctxFlags, avCodecFlagFrameDuration|avCodecFlagLowDelay)

	var opts unsafe.Pointer
	defer l.av_dict_free(&opts)
	for _, kv := range encoderOptions(e.name) {
		if r := l.av_dict_set(&opts, kv[0], kv[1], 0); r < 0 {
			return l.averror("set "+kv[0], r)
		}
	}
	if r := l.avcodec_open2(ctx, e.codec, &opts); r < 0 {
		return l.averror("open the "+e.name+" encoder at "+fmt.Sprintf("%dx%d", e.w, e.h), r)
	}
	e.opened = true

	f := l.av_frame_alloc()
	if f == nil {
		return errors.New("H.264: FFmpeg could not allocate a picture")
	}
	e.frame = f
	putInt32(f, frameWidth, int32(e.w))
	putInt32(f, frameHeight, int32(e.h))
	putInt32(f, frameFormat, e.pixFmt)
	if r := l.av_frame_get_buffer(f, 0); r < 0 {
		return l.averror("allocate a picture", r)
	}
	p := l.av_packet_alloc()
	if p == nil {
		return errors.New("H.264: FFmpeg could not allocate a packet")
	}
	e.pkt = p
	return nil
}

// encoderOptions are the options each encoder takes on top of the context's
// own fields. An encoder ignores the ones it does not know.
func encoderOptions(name string) [][2]string {
	switch name {
	case "h264_mf":
		// CBR keeps the bit rate the caller asked for while the screen
		// changes and while it is still, and the live streaming scenario is
		// what a phone mirror is. Neither changes how far behind it runs,
		// though: measured with encoderlag_test.go, it gives each picture
		// back twelve pictures after it went in, at any bit rate, rate
		// control mode, scenario or frame rate, with or without the low
		// delay flag. That is why libopenh264 comes first: this encoder is
		// only for a computer whose FFmpeg has nothing better.
		return [][2]string{{"rate_control", "cbr"}, {"scenario", "live_streaming"}}
	case "libopenh264":
		// Bit rate mode holds the bit rate the caller asked for. The default
		// frame skipping stays off, so no picture is dropped silently.
		return [][2]string{{"profile", "main"}, {"rc_mode", "bitrate"}}
	}
	return nil
}

// encode encodes one picture given as NV12 planes: y is w*h bytes, uv
// w*h/2 bytes of interleaved Cb and Cr. A forced key frame starts a
// self-contained picture, for a viewer that joins. It returns what the
// encoder produced, which is one frame, or none when it held one back.
//
// pts is when the picture was taken, counted from the stream's start. A
// stream of screen pictures is variable rate — a still screen costs no
// pictures at all, and a screenshot takes a third of a second — so each
// picture also carries how long it lasted, so that the encoder's rate
// control works with the real timing instead of a nominal 15 pictures a
// second. The picture's own timestamp comes back out with its packet, so
// that the packet says when the picture was taken and not when the encoder
// finished with it.
func (e *h264Encoder) encode(y, uv []byte, pts time.Duration, forceKey bool) ([]encodedFrame, error) {
	if len(y) < e.w*e.h || len(uv) < e.w*e.h/2 {
		return nil, errors.New("H.264: short picture")
	}
	l := e.lib
	if r := l.av_frame_make_writable(e.frame); r < 0 {
		return nil, l.averror("write a picture", r)
	}
	copyPlane(ptrAt(e.frame, frameData), getInt32(e.frame, frameLinesize), y, e.w, e.h)
	if e.pixFmt == l.pixNV12 {
		copyPlane(ptrAt(e.frame, frameData+8), getInt32(e.frame, frameLinesize+4), uv, e.w, e.h/2)
	} else {
		// Planar 4:2:0: the chroma that comes interleaved is split into a
		// plane of Cb and a plane of Cr.
		copyChroma(
			ptrAt(e.frame, frameData+8), getInt32(e.frame, frameLinesize+4),
			ptrAt(e.frame, frameData+16), getInt32(e.frame, frameLinesize+8),
			uv, e.w/2, e.h/2)
	}
	ticks := l.av_rescale_q(int64(pts), nanosecondTimeBase, h264TimeBase)
	putInt64(e.frame, framePTS, ticks)
	putInt32(e.frame, framePictType, 0) // AV_PICTURE_TYPE_NONE
	if forceKey {
		putInt32(e.frame, framePictType, avPictureTypeI)
	}
	// How long this picture lasted, in the time base's ticks. The encoder
	// puts it in the sample it hands Windows, and Windows' encoder holds
	// every picture back until it is told how long they last.
	dur := int64(h264TimeBase.Den) / int64(e.fps)
	if e.haveLast {
		if gap := ticks - e.lastTicks; gap > 0 && gap < int64(h264TimeBase.Den) {
			dur = gap
		}
	}
	putInt64(e.frame, frameDuration, dur)
	e.lastTicks, e.haveLast = ticks, true
	e.times = e.times[:0]
	if r := l.avcodec_send_frame(e.ctx, e.frame); r < 0 && r != avErrorEAGAIN {
		return nil, l.averror("encode", r)
	}
	out, err := e.receive()
	if err != nil {
		return out, err
	}
	return out, nil
}

// packetTimes is when each picture of the last encode was taken, in the
// order encode returned them. The frame stream stamps every packet with its
// own picture's time: an encoder that holds pictures back returns them long
// after they were taken, and a packet stamped with the moment it came out
// would claim a stale picture is fresh.
func (e *h264Encoder) packetTimes() []time.Duration { return e.times }

// packetTime is when the picture of the packet just returned was taken. The
// encoder carries the picture's timestamp from when the picture was fed, so
// the packet has it even when the encoder held the picture back. A packet
// without one (a flush) belongs to the last picture fed.
func (e *h264Encoder) packetTime() time.Duration {
	ticks := getInt64(e.pkt, packetPTS)
	if ticks == avNoPTS {
		ticks = e.lastTicks
	}
	return time.Duration(e.lib.av_rescale_q(ticks, h264TimeBase, nanosecondTimeBase))
}

// receive takes every packet the encoder has, as Annex B.
func (e *h264Encoder) receive() ([]encodedFrame, error) {
	l := e.lib
	var out []encodedFrame
	for {
		r := l.avcodec_receive_packet(e.ctx, e.pkt)
		if r == avErrorEAGAIN || r == avErrorEOF {
			return out, nil
		}
		if r < 0 {
			return out, l.averror("encode", r)
		}
		f := e.packet()
		e.times = append(e.times, e.packetTime())
		l.av_packet_unref(e.pkt)
		out = append(out, f)
	}
}

// packet copies the packet the encoder just returned.
func (e *h264Encoder) packet() encodedFrame {
	n := int(getInt32(e.pkt, packetSize))
	var f encodedFrame
	if n > 0 {
		p := ptrAt(e.pkt, packetData)
		data := append([]byte(nil), unsafe.Slice((*byte)(p), n)...)
		if annexBStart(data) == 0 {
			data = avccToAnnexB(data, 4)
		}
		f.data = data
		f.keyFrame = getInt32(e.pkt, packetFlags)&avPktFlagKey != 0 || hasIDR(data)
	}
	if f.keyFrame {
		f.config = e.parameterSets()
	}
	return f
}

// parameterSets returns the SPS and PPS the encoder keeps in its context's
// extradata, Annex B. Some encoders put them in every key frame instead,
// and then this is empty, which the caller copes with.
func (e *h264Encoder) parameterSets() []byte {
	if e.config != nil {
		return e.config
	}
	n := int(getInt32(e.ctx, ctxExtraSize))
	if n <= 0 {
		return nil
	}
	p := ptrAt(e.ctx, ctxExtradata)
	if p == nil {
		return nil
	}
	e.config = extradataToAnnexB(append([]byte(nil), unsafe.Slice((*byte)(p), n)...))
	return e.config
}

// copyPlane copies a plane into the picture's memory, whose rows are stride
// bytes apart.
func copyPlane(dst unsafe.Pointer, stride int32, src []byte, w, h int) {
	if dst == nil || stride <= 0 {
		return
	}
	for row := 0; row < h; row++ {
		out := unsafe.Slice((*byte)(unsafe.Add(dst, uintptr(row)*uintptr(stride))), w)
		copy(out, src[row*w:(row+1)*w])
	}
}

// copyChroma copies interleaved Cb and Cr into the two planes of a planar
// picture, whose rows are strideU and strideV bytes apart.
func copyChroma(dstU unsafe.Pointer, strideU int32, dstV unsafe.Pointer, strideV int32, src []byte, w, h int) {
	if dstU == nil || dstV == nil || strideU <= 0 || strideV <= 0 {
		return
	}
	for row := 0; row < h; row++ {
		u := unsafe.Slice((*byte)(unsafe.Add(dstU, uintptr(row)*uintptr(strideU))), w)
		v := unsafe.Slice((*byte)(unsafe.Add(dstV, uintptr(row)*uintptr(strideV))), w)
		s := src[row*2*w : (row+1)*2*w]
		for col := 0; col < w; col++ {
			u[col] = s[2*col]
			v[col] = s[2*col+1]
		}
	}
}

// Close stops the encoder and frees what it holds.
func (e *h264Encoder) Close() {
	l := e.lib
	if l == nil {
		return
	}
	if e.ctx != nil {
		if e.opened {
			// Flush what the encoder held back, and drop it: the stream is
			// ending, so no one waits for those pictures.
			l.avcodec_send_frame(e.ctx, nil)
			e.receive()
		}
		l.avcodec_free_context(&e.ctx)
		e.ctx = nil
	}
	if e.frame != nil {
		l.av_frame_free(&e.frame)
	}
	if e.pkt != nil {
		l.av_packet_free(&e.pkt)
	}
}

// h264Available reports whether this computer can encode H.264, saying what
// is missing when it cannot.
func h264Available() error {
	l, err := loadFFmpeg()
	if err != nil {
		return h264Unsupported(err)
	}
	if _, _, err := l.h264Encoder(); err != nil {
		return h264Unsupported(err)
	}
	return nil
}

// h264Unsupported marks an error as one Pinky cannot fix by itself, which
// the app shows as a phone without a live picture.
func h264Unsupported(err error) error {
	return fmt.Errorf("the live picture of iPhones below iOS 27 needs FFmpeg with an H.264 encoder: %v: %w", err, driver.ErrUnsupported)
}

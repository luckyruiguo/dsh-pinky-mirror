//go:build windows

package ios

// H.264 encoding on Windows through FFmpeg's libavcodec, called through
// purego so Pinky builds without cgo. The iPhones below iOS 27 send their
// picture as JPEG frames; internal/driver/ios/framestream.go turns them
// into NV12 and encodes them here, because the mirror and the recorder take
// H.264 only.
//
// The struct offsets below are those of FFmpeg 9 (libavcodec 63, libavutil
// 61). The loader refuses other major versions, whose layouts differ, and
// checks the layout at run time before the first picture.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/windows"
)

// The FFmpeg major versions this file is built for (FFmpeg 9).
const (
	avcodecMajor = 63
	avutilMajor  = 61
)

// Offsets of the fields Pinky reads and writes, for FFmpeg 9 on 64-bit
// Windows. They were printed with offsetof() against FFmpeg 9.0.2's own
// headers; checkLayout verifies them at run time.
const (
	frameData     = 0   // AVFrame.data[8]
	frameLinesize = 64  // AVFrame.linesize[8]
	frameWidth    = 104 // AVFrame.width
	frameHeight   = 108 // AVFrame.height
	frameFormat   = 116 // AVFrame.format
	framePictType = 120 // AVFrame.pict_type
	framePTS      = 136 // AVFrame.pts
	frameDuration = 408 // AVFrame.duration
	frameBuf      = 184 // AVFrame.buf[8]
	packetBuf     = 0   // AVPacket.buf
	packetPTS     = 8   // AVPacket.pts
	packetData    = 24  // AVPacket.data
	packetSize    = 32  // AVPacket.size
	packetFlags   = 40  // AVPacket.flags
	codecID       = 20  // AVCodec.id
	ctxCodecID    = 24  // AVCodecContext.codec_id
	ctxBitRate    = 56  // AVCodecContext.bit_rate
	ctxFlags      = 64  // AVCodecContext.flags
	ctxExtradata  = 72  // AVCodecContext.extradata
	ctxExtraSize  = 80  // AVCodecContext.extradata_size
	ctxTimeBase   = 84  // AVCodecContext.time_base
	ctxFramerate  = 100 // AVCodecContext.framerate
	ctxWidth      = 112 // AVCodecContext.width
	ctxHeight     = 116 // AVCodecContext.height
	ctxPixFmt     = 136 // AVCodecContext.pix_fmt
	ctxMaxBFrames = 200 // AVCodecContext.max_b_frames
	ctxGOPSize    = 332 // AVCodecContext.gop_size
	ctxProfile    = 688 // AVCodecContext.profile
)

// Constants of FFmpeg's API.
const (
	avPktFlagKey      = 1  // AV_PKT_FLAG_KEY
	avPictureTypeI    = 1  // AV_PICTURE_TYPE_I
	avProfileH264Main = 77 // AV_PROFILE_H264_MAIN
	avErrorEAGAIN     = -11
	avErrorEOF        = -541478725
	avLogQuiet        = -8 // AV_LOG_QUIET
	avLogError        = 16 // AV_LOG_ERROR

	// AV_CODEC_FLAG_FRAME_DURATION tells FFmpeg to copy each picture's
	// duration into the samples it hands the encoder. Without it, the Media
	// Foundation encoder holds every picture back until the stream ends.
	avCodecFlagFrameDuration = 1 << 8
	// AV_CODEC_FLAG_LOW_DELAY asks the encoder for real time: the Media
	// Foundation encoder then runs Windows' H.264 codec in its low-latency
	// mode and gives each picture back as soon as it is encoded.
	avCodecFlagLowDelay = 1 << 19
)

// h264EncoderNames are the H.264 encoders Pinky looks for, in order. Cisco's
// OpenH264 comes first because it gives each picture back as soon as it is
// encoded; a mirror cannot afford an encoder that holds pictures back.
// Windows' own Media Foundation encoder is the fallback, since it needs
// nothing but Windows' own libraries, though it holds a picture back for
// about twelve more (see encoderOptions). x264 is not here: its licence
// would make the whole of Pinky GPL.
var h264EncoderNames = []string{"libopenh264", "h264_mf"}

// AVRational is FFmpeg's rational number.
type AVRational struct {
	Num int32
	Den int32
}

// ffmpegLib holds FFmpeg's functions once loaded.
type ffmpegLib struct {
	dirs []string // where the libraries were found, for messages

	// libavutil
	avutil_version         func() uint32
	av_frame_alloc         func() unsafe.Pointer
	av_frame_free          func(*unsafe.Pointer)
	av_frame_get_buffer    func(unsafe.Pointer, int32) int32
	av_frame_make_writable func(unsafe.Pointer) int32
	av_packet_alloc        func() unsafe.Pointer
	av_packet_free         func(*unsafe.Pointer)
	av_packet_unref        func(unsafe.Pointer)
	av_new_packet          func(unsafe.Pointer, int32) int32
	av_dict_set            func(*unsafe.Pointer, string, string, int32) int32
	av_dict_free           func(*unsafe.Pointer)
	av_get_pix_fmt         func(string) int32
	av_strerror            func(int32, *byte, uintptr) int32
	av_log_set_level       func(int32)
	av_rescale_q           func(int64, AVRational, AVRational) int64

	// libavcodec
	avcodec_version              func() uint32
	avcodec_find_encoder_by_name func(string) unsafe.Pointer
	avcodec_alloc_context3       func(unsafe.Pointer) unsafe.Pointer
	avcodec_free_context         func(*unsafe.Pointer)
	avcodec_open2                func(unsafe.Pointer, unsafe.Pointer, *unsafe.Pointer) int32
	avcodec_send_frame           func(unsafe.Pointer, unsafe.Pointer) int32
	avcodec_receive_packet       func(unsafe.Pointer, unsafe.Pointer) int32

	pixNV12    int32 // AV_PIX_FMT_NV12
	pixYUV420P int32 // AV_PIX_FMT_YUV420P
}

var (
	ffmpegMu   sync.Mutex
	ffmpegLibs = map[string]*ffmpegLib{}
)

// ffmpegDir is a folder Pinky looks for FFmpeg's libraries in.
type ffmpegDir struct {
	path  string // "" for the loader's own path
	label string // for messages
}

// loadFFmpeg loads FFmpeg from the folders Pinky looks in and keeps it for
// the next call. It is safe to call more than once.
func loadFFmpeg() (*ffmpegLib, error) {
	dirs := ffmpegSearchDirs()
	key := strings.Join(dirLabels(dirs), ";")
	ffmpegMu.Lock()
	defer ffmpegMu.Unlock()
	if l, ok := ffmpegLibs[key]; ok {
		return l, nil
	}
	l, err := openFFmpeg(dirs)
	if err != nil {
		return nil, err
	}
	ffmpegLibs[key] = l
	return l, nil
}

// ffmpegSearchDirs lists the folders to try, in order: PINKY_FFMPEG_DIR, the
// folder the executable is in, and then where the dynamic loader looks for
// libraries. This is the same order internal/video uses.
func ffmpegSearchDirs() []ffmpegDir {
	var dirs []ffmpegDir
	if d := os.Getenv("PINKY_FFMPEG_DIR"); d != "" {
		dirs = append(dirs, ffmpegDir{path: d, label: d + " (PINKY_FFMPEG_DIR)"})
	}
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		dir := filepath.Dir(exe)
		dirs = append(dirs, ffmpegDir{path: dir, label: dir + " (next to the executable)"})
	}
	return append(dirs, ffmpegDir{label: "the system's library path"})
}

// dirLabels names the folders, for messages.
func dirLabels(dirs []ffmpegDir) []string {
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		out = append(out, d.label)
	}
	return out
}

// openFFmpeg loads FFmpeg's libraries from the given folders, in order, and
// the functions Pinky calls.
func openFFmpeg(dirs []ffmpegDir) (*ffmpegLib, error) {
	open := func(name string) (uintptr, string, error) {
		var errs []string
		for _, dir := range dirs {
			path := name
			if dir.path != "" {
				path = filepath.Join(dir.path, name)
				if _, err := os.Stat(path); err != nil {
					continue
				}
			}
			h, err := openLibrary(path)
			if err == nil {
				return h, dir.label, nil
			}
			errs = append(errs, err.Error())
		}
		msg := fmt.Sprintf("FFmpeg %d not found: no %s in %s", avcodecMajor, name,
			strings.Join(dirLabels(dirs), ", "))
		if len(errs) > 0 {
			msg += " (" + strings.Join(errs, "; ") + ")"
		}
		return 0, "", errors.New(msg)
	}

	l := &ffmpegLib{}
	avutilName := fmt.Sprintf("avutil-%d.dll", avutilMajor)
	avcodecName := fmt.Sprintf("avcodec-%d.dll", avcodecMajor)
	hAvutil, d1, err := open(avutilName)
	if err != nil {
		return nil, err
	}
	preloadSwresample(d1)
	hAvcodec, d2, err := open(avcodecName)
	if err != nil {
		return nil, err
	}
	l.dirs = []string{d1}
	if d2 != d1 {
		l.dirs = append(l.dirs, d2)
	}

	var missing []string
	reg := func(fptr any, h uintptr, name string) {
		sym, err := lookupSymbol(h, name)
		if err != nil || sym == 0 {
			missing = append(missing, name)
			return
		}
		purego.RegisterFunc(fptr, sym)
	}
	reg(&l.avutil_version, hAvutil, "avutil_version")
	reg(&l.av_frame_alloc, hAvutil, "av_frame_alloc")
	reg(&l.av_frame_free, hAvutil, "av_frame_free")
	reg(&l.av_frame_get_buffer, hAvutil, "av_frame_get_buffer")
	reg(&l.av_frame_make_writable, hAvutil, "av_frame_make_writable")
	reg(&l.av_dict_set, hAvutil, "av_dict_set")
	reg(&l.av_dict_free, hAvutil, "av_dict_free")
	reg(&l.av_get_pix_fmt, hAvutil, "av_get_pix_fmt")
	reg(&l.av_strerror, hAvutil, "av_strerror")
	reg(&l.av_log_set_level, hAvutil, "av_log_set_level")
	reg(&l.av_rescale_q, hAvutil, "av_rescale_q")

	reg(&l.avcodec_version, hAvcodec, "avcodec_version")
	reg(&l.avcodec_find_encoder_by_name, hAvcodec, "avcodec_find_encoder_by_name")
	reg(&l.avcodec_alloc_context3, hAvcodec, "avcodec_alloc_context3")
	reg(&l.avcodec_free_context, hAvcodec, "avcodec_free_context")
	reg(&l.avcodec_open2, hAvcodec, "avcodec_open2")
	reg(&l.avcodec_send_frame, hAvcodec, "avcodec_send_frame")
	reg(&l.avcodec_receive_packet, hAvcodec, "avcodec_receive_packet")
	// AVPacket is libavcodec's.
	reg(&l.av_packet_alloc, hAvcodec, "av_packet_alloc")
	reg(&l.av_packet_free, hAvcodec, "av_packet_free")
	reg(&l.av_packet_unref, hAvcodec, "av_packet_unref")
	reg(&l.av_new_packet, hAvcodec, "av_new_packet")
	if len(missing) > 0 {
		return nil, fmt.Errorf("FFmpeg in %s lacks %s; Pinky needs FFmpeg %d",
			strings.Join(l.dirs, ", "), strings.Join(missing, ", "), avcodecMajor)
	}

	for _, v := range []struct {
		name string
		got  uint32
		want uint32
	}{
		{"libavcodec", l.avcodec_version() >> 16, avcodecMajor},
		{"libavutil", l.avutil_version() >> 16, avutilMajor},
	} {
		if v.got != v.want {
			return nil, fmt.Errorf("%s %d in %s; Pinky needs %s %d",
				v.name, v.got, strings.Join(l.dirs, ", "), v.name, v.want)
		}
	}

	if os.Getenv("PINKY_FFMPEG_LOG") == "" {
		l.av_log_set_level(avLogQuiet)
	} else {
		l.av_log_set_level(avLogError)
	}
	l.pixNV12 = l.av_get_pix_fmt("nv12")
	if l.pixNV12 < 0 {
		return nil, errors.New("FFmpeg does not know the nv12 pixel format")
	}
	l.pixYUV420P = l.av_get_pix_fmt("yuv420p")
	if l.pixYUV420P < 0 {
		return nil, errors.New("FFmpeg does not know the yuv420p pixel format")
	}
	if err := l.checkLayout(); err != nil {
		return nil, err
	}
	return l, nil
}

// swresampleMajor is libswresample's major version in FFmpeg 9. Pinky does
// not call it, but some builds of libavcodec (BtbN's, for one) link it.
const swresampleMajor = 7

// preloadSwresample loads libswresample from dir, the folder libavutil came
// from, when it is there. A library's dependencies are looked for on the
// dynamic loader's own path, not in the library's folder, so a libavcodec
// that links libswresample fails to load from a folder of its own (such as
// PINKY_FFMPEG_DIR) unless libswresample is loaded first.
func preloadSwresample(dir string) {
	i := strings.Index(dir, " (")
	if i > 0 {
		dir = dir[:i]
	}
	if dir == "" {
		return
	}
	path := filepath.Join(dir, fmt.Sprintf("swresample-%d.dll", swresampleMajor))
	if _, err := os.Stat(path); err == nil {
		openLibrary(path) // best effort: libavcodec reports what is missing
	}
}

// openLibrary loads a DLL. A DLL given by its full path finds the DLLs it
// imports in its own folder first (LOAD_WITH_ALTERED_SEARCH_PATH), so
// FFmpeg's DLLs work from a folder of their own, such as PINKY_FFMPEG_DIR.
func openLibrary(path string) (uintptr, error) {
	var flags uintptr
	if filepath.IsAbs(path) {
		flags = windows.LOAD_WITH_ALTERED_SEARCH_PATH
	}
	h, err := windows.LoadLibraryEx(path, 0, flags)
	return uintptr(h), err
}

func lookupSymbol(h uintptr, name string) (uintptr, error) {
	return windows.GetProcAddress(windows.Handle(h), name)
}

// h264Encoder finds an H.264 encoder in FFmpeg. It returns the encoder and
// its name.
func (l *ffmpegLib) h264Encoder() (unsafe.Pointer, string, error) {
	for _, name := range h264EncoderNames {
		if c := l.avcodec_find_encoder_by_name(name); c != nil {
			return c, name, nil
		}
	}
	return nil, "", fmt.Errorf("this FFmpeg has no H.264 encoder (looked for %s)",
		strings.Join(h264EncoderNames, ", "))
}

// checkLayout verifies the struct offsets against what FFmpeg itself
// writes, so a build of FFmpeg 9 with another layout fails here with a
// clear error rather than corrupting memory later.
func (l *ffmpegLib) checkLayout() error {
	bad := func(what string) error {
		return fmt.Errorf("FFmpeg in %s has an unexpected %s layout; Pinky supports FFmpeg 9.0 on 64-bit systems",
			strings.Join(l.dirs, ", "), what)
	}
	if unsafe.Sizeof(uintptr(0)) != 8 {
		return errors.New("Pinky's FFmpeg bindings need a 64-bit system")
	}

	f := l.av_frame_alloc()
	if f == nil {
		return errors.New("FFmpeg: av_frame_alloc failed")
	}
	defer l.av_frame_free(&f)
	putInt32(f, frameWidth, 320)
	putInt32(f, frameHeight, 240)
	putInt32(f, frameFormat, l.pixNV12)
	putInt32(f, framePictType, avPictureTypeI)
	putInt64(f, framePTS, 12345)
	putInt64(f, frameDuration, 1)
	if getInt32(f, frameWidth) != 320 || getInt32(f, frameHeight) != 240 ||
		getInt32(f, frameFormat) != l.pixNV12 || getInt32(f, framePictType) != avPictureTypeI ||
		getInt64(f, framePTS) != 12345 || getInt64(f, frameDuration) != 1 {
		return bad("AVFrame")
	}
	if r := l.av_frame_get_buffer(f, 0); r < 0 {
		return bad("AVFrame")
	}
	if ptrAt(f, frameData) == nil || getInt32(f, frameLinesize) < 320 || ptrAt(f, frameBuf) == nil {
		return bad("AVFrame")
	}

	p := l.av_packet_alloc()
	if p == nil {
		return errors.New("FFmpeg: av_packet_alloc failed")
	}
	defer l.av_packet_free(&p)
	if getInt64(p, packetPTS) != avNoPTS {
		return bad("AVPacket")
	}
	if r := l.av_new_packet(p, 100); r < 0 {
		return bad("AVPacket")
	}
	if getInt32(p, packetSize) != 100 || ptrAt(p, packetData) == nil || ptrAt(p, packetBuf) == nil {
		return bad("AVPacket")
	}
	l.av_packet_unref(p)

	codec, _, err := l.h264Encoder()
	if err != nil || codec == nil {
		return nil // no encoder to check the context with
	}
	ctx := l.avcodec_alloc_context3(codec)
	if ctx == nil {
		return errors.New("FFmpeg: avcodec_alloc_context3 failed")
	}
	defer l.avcodec_free_context(&ctx)
	if getInt32(ctx, ctxCodecID) != getInt32(codec, codecID) {
		return bad("AVCodecContext")
	}
	putInt32(ctx, ctxWidth, 320)
	putInt32(ctx, ctxHeight, 240)
	putInt64(ctx, ctxBitRate, 2500000)
	putInt32(ctx, ctxGOPSize, 30)
	putInt32(ctx, ctxMaxBFrames, 0)
	putInt32(ctx, ctxProfile, avProfileH264Main)
	putInt32(ctx, ctxFlags, avCodecFlagFrameDuration)
	putRational(ctx, ctxTimeBase, AVRational{1, 15})
	if getInt32(ctx, ctxWidth) != 320 || getInt32(ctx, ctxHeight) != 240 ||
		getInt64(ctx, ctxBitRate) != 2500000 || getInt32(ctx, ctxGOPSize) != 30 ||
		getInt32(ctx, ctxMaxBFrames) != 0 || getInt32(ctx, ctxProfile) != avProfileH264Main ||
		getInt32(ctx, ctxFlags) != avCodecFlagFrameDuration ||
		getRational(ctx, ctxTimeBase) != (AVRational{1, 15}) {
		return bad("AVCodecContext")
	}
	return nil
}

// averror turns an FFmpeg error code into a Go error.
func (l *ffmpegLib) averror(op string, code int32) error {
	var buf [128]byte
	msg := fmt.Sprintf("error %d", code)
	if l.av_strerror(code, &buf[0], uintptr(len(buf))) == 0 {
		n := 0
		for n < len(buf) && buf[n] != 0 {
			n++
		}
		msg = string(buf[:n])
	}
	return fmt.Errorf("FFmpeg: %s: %s", op, msg)
}

func ptrAt(base unsafe.Pointer, off uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Add(base, off))
}

func getInt32(base unsafe.Pointer, off uintptr) int32 {
	return *(*int32)(unsafe.Add(base, off))
}

func putInt32(base unsafe.Pointer, off uintptr, v int32) {
	*(*int32)(unsafe.Add(base, off)) = v
}

func getInt64(base unsafe.Pointer, off uintptr) int64 {
	return *(*int64)(unsafe.Add(base, off))
}

func putInt64(base unsafe.Pointer, off uintptr, v int64) {
	*(*int64)(unsafe.Add(base, off)) = v
}

func getRational(base unsafe.Pointer, off uintptr) AVRational {
	return AVRational{
		Num: getInt32(base, off),
		Den: getInt32(base, off+4),
	}
}

func putRational(base unsafe.Pointer, off uintptr, v AVRational) {
	putInt32(base, off, v.Num)
	putInt32(base, off+4, v.Den)
}

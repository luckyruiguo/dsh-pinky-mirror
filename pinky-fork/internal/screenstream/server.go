package screenstream

// 实时画面流：把手机的 H.264 视频解成 RGBA，交给 ffmpeg 编成 JPEG，
// 以 MJPEG 供 Web 客户端（DSH 侧边栏）显示。
//
// 为什么用 ffmpeg 编码：Go 标准库的 image/jpeg 编 644x1440 要 320ms/帧（约 3fps），
// 而 ffmpeg 的 libavcodec 能到 30fps 以上。Pinky 自带 FFmpeg 9 动态库。

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"image"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/video"
)

// Options 是画面流的设置。
type Options struct {
	FPS    int
	Width  int
	MaxAge time.Duration
	Log    *slog.Logger
}

// Server 提供 /screen/stream（MJPEG）、/screen/frame.jpg、/screen/health。
type Server struct {
	g   *gateway.Gateway
	opt Options

	mu     sync.RWMutex
	frame  []byte
	seq    uint64
	lastAt time.Time

	takeover atomic.Bool
	dbgN     atomic.Int64
	ffmpeg   string

	viewers map[chan []byte]struct{}
	vmu     sync.Mutex
}

// New 构建画面流服务。
func New(g *gateway.Gateway, opt Options) *Server {
	if opt.FPS <= 0 {
		opt.FPS = 15
	}
	if opt.Width <= 0 {
		opt.Width = 720
	}
	if opt.MaxAge <= 0 {
		opt.MaxAge = 4 * time.Second
	}
	s := &Server{g: g, opt: opt, viewers: make(map[chan []byte]struct{})}
	s.ffmpeg = findFFmpeg()
	if s.ffmpeg == "" && opt.Log != nil {
		opt.Log.Warn("screenstream: no ffmpeg found, falling back to slow encoder")
	}
	go s.pump()
	return s
}

func findFFmpeg() string {
	if v := os.Getenv("PINKY_FFMPEG_BIN"); v != "" {
		return v
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	// Pinky 自带库目录旁偶尔也有 ffmpeg 可执行文件
	if dir := os.Getenv("PINKY_FFMPEG_DIR"); dir != "" {
		p := filepath.Join(dir, "ffmpeg")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func (s *Server) dbg(msg string, kv ...any) {
	if s.opt.Log != nil {
		s.opt.Log.Info("screenstream: "+msg, kv...)
	}
}

// Takeover 读取/设置「接管屏幕操作」开关（Muse 式接管），默认关闭。
func (s *Server) Takeover(v ...bool) bool {
	if len(v) == 0 {
		return s.takeover.Load()
	}
	s.takeover.Store(v[0])
	return v[0]
}

func (s *Server) pump() {
	backoff := time.Second
	for {
		if err := s.runOnce(); err != nil && s.opt.Log != nil {
			s.opt.Log.Warn("screenstream: stream ended", "err", err)
		}
		time.Sleep(backoff)
		if backoff < 5*time.Second {
			backoff *= 2
		} else {
			backoff = time.Second
		}
	}
}

func (s *Server) firstDevice() (string, error) {
	for _, d := range s.g.Devices() {
		if d.Info.State == driver.StateOnline {
			return d.Info.ID, nil
		}
	}
	return "", driver.ErrNotFound
}

func (s *Server) runOnce() error {
	id, err := s.firstDevice()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.dbg("opening stream", "device", id)
	st, err := s.g.Stream(ctx, id, driver.StreamOptions{MaxSize: s.opt.Width * 2, MaxFPS: s.opt.FPS})
	if err != nil {
		s.dbg("stream failed", "err", err)
		return err
	}
	defer st.Close()
	s.dbg("stream opened", "codec", string(st.Codec()))

	wake := make(chan struct{}, 1)
	p, err := video.NewPlayer(st, video.PlayerOptions{
		SkipRepeats: true,
		OnFrame:     func() { select { case wake <- struct{}{}: default: } },
	})
	if err != nil {
		return err
	}
	defer p.Close()
	s.dbg("player ready")

	// ffmpeg 编码管道：rawvideo(rgba) stdin → mjpeg stdout
	var enc *encoderProc
	startEnc := func(w, h int) error {
		if s.ffmpeg == "" {
			return errNoFFmpeg
		}
		e, err := startEncoder(s.ffmpeg, w, h, s.opt.Width, s.onJPEG)
		if err != nil {
			return err
		}
		enc = e
		return nil
	}
	defer func() {
		if enc != nil {
			enc.close()
		}
	}()

	minGap := time.Second / time.Duration(s.opt.FPS)
	var last time.Time
	lastW, lastH := 0, 0

	for {
		// 等新帧信号；超时也照样尝试 Take，
		// 避免 wake 被消费后画面静止就永久卡住。
		select {
		case <-wake:
		case <-time.After(150 * time.Millisecond):
		}
		f := p.Take()
		if f == nil {
			if err := p.Err(); err != nil {
				return err
			}
			continue
		}
		img := f.Image
		if img == nil {
			continue
		}
		if !last.IsZero() && time.Since(last) < minGap {
			continue
		}
		b := img.Bounds()
		if enc == nil || b.Dx() != lastW || b.Dy() != lastH {
			if enc != nil {
				enc.close()
				enc = nil
			}
			if err := startEnc(b.Dx(), b.Dy()); err != nil {
				return err
			}
			lastW, lastH = b.Dx(), b.Dy()
		}
		tw := time.Now()
		if err := enc.write(img); err != nil {
			return err
		}
		if s.opt.Log != nil && s.dbgN.Add(1)%20 == 0 {
			s.opt.Log.Info("screenstream: frame", "size", fmt.Sprintf("%dx%d", b.Dx(), b.Dy()),
				"write", time.Since(tw).Milliseconds(), "size_bytes", len(img.Pix))
		}
		last = time.Now()
	}
}

var errNoFFmpeg = &noFFmpegError{}

type noFFmpegError struct{}

func (*noFFmpegError) Error() string { return "screenstream: ffmpeg not found" }

// onJPEG 是编码器每出一帧的回调。
func (s *Server) onJPEG(jpg []byte) {
	s.mu.Lock()
	s.frame = jpg
	s.seq++
	s.lastAt = time.Now()
	s.mu.Unlock()

	s.vmu.Lock()
	for ch := range s.viewers {
		select {
		case ch <- jpg:
		default:
		}
	}
	s.vmu.Unlock()
}

// Snapshot 返回最新一帧与其时间。
func (s *Server) Snapshot() ([]byte, time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.frame, s.lastAt
}

// ---------- ffmpeg 编码进程 ----------

type encoderProc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	mu     sync.Mutex
	closed bool
	cancel chan struct{}
	// frames 是有界队列：编码器落后时丢旧帧，绝不阻塞采集
	frames chan []byte
}

func startEncoder(bin string, w, h, outW int, onJPEG func([]byte)) (*encoderProc, error) {
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-f", "rawvideo", "-pix_fmt", "rgba", "-s", itoa(w) + "x" + itoa(h),
		"-i", "-",
		// 缩到侧边栏所需宽度再编码：更小的 JPEG，前端更省
		"-vf", fmt.Sprintf("scale=%d:-2:flags=fast_bilinear", outW),
		"-c:v", "mjpeg", "-q:v", "5",
		// 关键：每帧立即吐出，别让 ffmpeg 攒批导致画面卡住
		"-flush_packets", "1",
		"-f", "mjpeg", "-",
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = os.Environ()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	e := &encoderProc{cmd: cmd, stdin: stdin, cancel: make(chan struct{}), frames: make(chan []byte, 2)}

	// 写协程：独立于采集循环，ffmpeg 慢时只丢帧，不拖慢采集
	go func() {
		for {
			select {
			case <-e.cancel:
				return
			case pix := <-e.frames:
				if _, err := e.stdin.Write(pix); err != nil {
					return
				}
			}
		}
	}()

	go func() {
		SOI := []byte{0xff, 0xd8}
		EOI := []byte{0xff, 0xd9}
		acc := make([]byte, 0, 1<<20)
		buf := make([]byte, 256<<10)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				acc = append(acc, buf[:n]...)
				for {
					a := bytes.Index(acc, SOI)
					if a < 0 {
						if len(acc) > 1<<20 {
							acc = acc[:0]
						}
						break
					}
					if a > 0 {
						acc = acc[a:]
					}
					b := bytes.Index(acc, EOI)
					if b < 0 {
						break
					}
					frame := make([]byte, b+2)
					copy(frame, acc[:b+2])
					acc = acc[b+2:]
					onJPEG(frame)
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return e, nil
}

// write 把一帧投递给编码器。队列满时丢弃这一帧（保留最新画面优先），
// 保证采集循环永远不被 ffmpeg 阻塞。
func (e *encoderProc) write(img *image.RGBA) error {
	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if closed {
		return nil
	}
	pix := make([]byte, len(img.Pix))
	copy(pix, img.Pix)
	for {
		select {
		case e.frames <- pix:
			return nil
		default:
			// 队列满：丢掉最旧的一帧再试
			select {
			case <-e.frames:
			default:
			}
		}
	}
}

func (e *encoderProc) close() {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.closed = true
	e.mu.Unlock()
	close(e.frames)
	e.stdin.Close()
	select {
	case <-e.cancel:
	default:
		close(e.cancel)
	}
	go func() {
		done := make(chan struct{})
		go func() { e.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			e.cmd.Process.Kill()
		}
	}()
}

// pressKey 向当前手机发送一个导航键（返回/主页/最近任务）。
// 走 Gateway.Key，与 Pinky 客户端点击按键是同一条路径：
// 它属于「人的输入」，因此会按 Pinky 的规则处理 Agent 暂停。
func (s *Server) pressKey(k driver.Key) error {
	id, err := s.firstDevice()
	if err != nil {
		return err
	}
	return s.g.Key(id, k)
}

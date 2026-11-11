package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/video"
)

// bench decodes ffmpeg test streams in real time and reports the CPU the
// decoder uses and how long a picture takes from its packet to RGBA, or
// with -yuv to the planes the mirror hands MyGo.
func bench(args []string) error {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	dur := fs.Duration("t", 20*time.Second, "how long each run decodes")
	only := fs.String("run", "", "only runs whose name contains this")
	threads := fs.Int("convert-threads", 0, "threads that convert pictures to RGBA; 0 is the decoder's default")
	yuv := fs.Bool("yuv", false, "hand pictures over as their planes at the stream's size, as the mirror does, rather than RGBA at the run's")
	fs.Parse(args)
	if err := video.Load(); err != nil {
		return err
	}
	fmt.Printf("FFmpeg: %s\nCPU: %s, %d cores\n\n", video.Version(), cpuName(), runtime.NumCPU())
	type run struct {
		name       string
		codec      driver.Codec
		w, h       int
		outW, outH int
		software   bool
		source     string
	}
	runs := []run{
		{"h264 1920x1080 → full size, hardware", driver.H264, 1920, 1080, 0, 0, false, "testsrc2"},
		{"h264 1920x1080 → full size, software", driver.H264, 1920, 1080, 0, 0, true, "testsrc2"},
		{"h264 1080x2400 → full size, hardware", driver.H264, 1080, 2400, 0, 0, false, "testsrc2"},
		{"h264 1080x2400 → 600x1332 (mirror at 2x), hardware", driver.H264, 1080, 2400, 600, 1332, false, "testsrc2"},
		{"h264 1080x2400 → 600x1332, software", driver.H264, 1080, 2400, 600, 1332, true, "testsrc2"},
		{"h264 1080x2400 → full size, software", driver.H264, 1080, 2400, 0, 0, true, "testsrc2"},
		{"hevc 1206x2622 → 600x1304, hardware", driver.HEVC, 1206, 2622, 600, 1304, false, "testsrc"},
		{"hevc 1920x1080 → full size, hardware", driver.HEVC, 1920, 1080, 0, 0, false, "testsrc2"},
	}
	for _, r := range runs {
		if *only != "" && !strings.Contains(r.name, *only) {
			continue
		}
		res, err := benchOne(r.codec, r.w, r.h, r.outW, r.outH, r.software, r.source, *dur, *threads, *yuv)
		if err != nil {
			fmt.Printf("%s: %v\n", r.name, err)
			continue
		}
		fmt.Printf("%s\n  %s\n", r.name, res)
	}
	return nil
}

func benchOne(codec driver.Codec, w, h, outW, outH int, software bool, source string, dur time.Duration, threads int, yuv bool) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dur+30*time.Second)
	defer cancel()
	s, err := video.StartTestStream(ctx, video.TestStreamOptions{Codec: codec, Width: w, Height: h, FPS: 60, Source: source})
	if err != nil {
		return "", err
	}
	frames := make(chan struct{}, 1)
	p, err := video.NewPlayer(s, video.PlayerOptions{
		Decoder: video.DecoderOptions{Software: software, ConvertThreads: threads},
		YUV:     yuv,
		OnFrame: func() {
			select {
			case frames <- struct{}{}:
			default:
			}
		},
	})
	if err != nil {
		s.Close()
		return "", err
	}
	defer p.Close()
	p.SetSize(outW, outH)
	// Warm up: let the stream start and the decoder settle.
	warm := time.After(3 * time.Second)
	for done := false; !done; {
		select {
		case <-frames:
			p.Take()
		case <-warm:
			done = true
		case <-p.Done():
			return "", fmt.Errorf("stream ended: %v", p.Err())
		}
	}
	before := p.Stats()
	u0, s0 := cpuTimes()
	vt0 := xpcCPU()
	start := time.Now()
	var lat []time.Duration
	end := time.After(dur)
	for done := false; !done; {
		select {
		case <-frames:
			if f := p.Take(); f != nil {
				lat = append(lat, f.Ready.Sub(f.Received))
			}
		case <-end:
			done = true
		case <-p.Done():
			return "", fmt.Errorf("stream ended: %v", p.Err())
		}
	}
	el := time.Since(start)
	u1, s1 := cpuTimes()
	vt1 := xpcCPU()
	after := p.Stats()
	cpu := (u1 - u0 + s1 - s0).Seconds() / el.Seconds() * 100
	n := after.Decoded - before.Decoded
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	pct := func(q float64) time.Duration {
		if len(lat) == 0 {
			return 0
		}
		return lat[min(len(lat)-1, int(q*float64(len(lat))))]
	}
	hw := after.Hardware
	if hw == "" {
		hw = "software"
	}
	out := fmt.Sprintf("decoder %s · %.1f fps · CPU %.0f%% of one core (this process)", hw, float64(n)/el.Seconds(), cpu)
	if vt1 >= 0 && vt0 >= 0 && hw == "videotoolbox" {
		out += fmt.Sprintf(" + %.0f%% in VTDecoderXPCService", (vt1-vt0).Seconds()/el.Seconds()*100)
	}
	to := "RGBA"
	if yuv {
		to = "planes"
	}
	out += fmt.Sprintf("\n  packet → %s: median %v, p95 %v, max %v · decode %v + convert %v (means) · dropped %d of %d", to,
		pct(0.5).Round(10*time.Microsecond), pct(0.95).Round(10*time.Microsecond), pct(1).Round(10*time.Microsecond),
		after.Decode.Round(10*time.Microsecond), after.Convert.Round(10*time.Microsecond), after.Dropped-before.Dropped, n)
	return out, nil
}

func cpuName() string {
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil {
			return strings.TrimSpace(string(out))
		}
	}
	return runtime.GOARCH
}

// xpcCPU sums the CPU time of macOS's VideoToolbox decoder services, which
// decode outside the app's process; -1 elsewhere.
func xpcCPU() time.Duration {
	if runtime.GOOS != "darwin" {
		return -1
	}
	out, err := exec.Command("ps", "-A", "-o", "time=,comm=").Output()
	if err != nil {
		return -1
	}
	var total time.Duration
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !strings.Contains(line, "VTDecoderXPCService") {
			continue
		}
		total += parsePSTime(f[0])
	}
	return total
}

// parsePSTime parses ps's [[dd-]hh:]mm:ss.cc.
func parsePSTime(s string) time.Duration {
	var d time.Duration
	if i := strings.Index(s, "-"); i > 0 {
		days, _ := strconv.Atoi(s[:i])
		d += time.Duration(days) * 24 * time.Hour
		s = s[i+1:]
	}
	parts := strings.Split(s, ":")
	mult := time.Second
	for i := len(parts) - 1; i >= 0; i-- {
		v, _ := strconv.ParseFloat(parts[i], 64)
		d += time.Duration(v * float64(mult))
		mult *= 60
	}
	return d
}

var _ = os.Stdout

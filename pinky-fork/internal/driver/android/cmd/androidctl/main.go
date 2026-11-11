// Command androidctl exercises Pinky's Android driver from a terminal, for
// development and acceptance tests.
//
//	androidctl devices
//	androidctl [-s serial] observe [-json]
//	androidctl [-s serial] screenshot FILE.png
//	androidctl [-s serial] tap X Y [-long]
//	androidctl [-s serial] swipe X1 Y1 X2 Y2 [MS]
//	androidctl [-s serial] type [-append] [-submit] TEXT
//	androidctl [-s serial] key NAME          (back, home, recents, enter, ...)
//	androidctl [-s serial] launch APP        (package or display name)
//	androidctl [-s serial] apps
//	androidctl [-s serial] locked
//	androidctl [-s serial] screen            (whether the screen is on)
//	androidctl [-s serial] wake
//	androidctl [-s serial] unlock            (PIN from $ANDROIDCTL_PIN or stdin)
//	androidctl [-s serial] files             (list the phone's Pinky folder)
//	androidctl [-s serial] pull PATH FILE    (PATH inside the Pinky folder)
//	androidctl [-s serial] push FILE PATH
//	androidctl [-s serial] stream [-seconds N] [-max-size N] [-out FILE.h264]
//	androidctl [-s serial] settle [-max MS]  (time until the screen is still)
//	androidctl [-s serial] run [-person] STEP...  (steps with action and settle timings)
//	androidctl [-s serial] companion-install [-force] (install or update the companion app)
//	androidctl [-s serial] companion-state
//	androidctl [-s serial] checkup [-json]   (what the phone needs for Pinky, item by item)
//	androidctl [-s serial] bg [-app APP] [-out FILE.h264] [-seconds N] [STEP...]
//	androidctl wifi-search [-seconds N]
//	androidctl pair-qr FILE.png
//	androidctl pair-code ADDR CODE
//	androidctl wifi-connect ADDR
//	androidctl wifi-reconnect [-seconds N]
//
// Steps for run and bg: tap:X,Y  longtap:X,Y  swipe:X1,Y1,X2,Y2[,MS]
// pinch:CX,CY,FROM,TO[,MS[,ANGLE[,ROTATE]]] (fingers FROM then TO pixels
// apart around CX,CY)  key:NAME  type:TEXT  launch:APP  sleep:MS  observe
// shot:FILE.png  state (bg: the phone's own screen)
//
// Run commands that touch a phone through scripts/devlock.sh.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image/png"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
	"github.com/DIYgod/pinky/internal/screen"
)

func main() {
	serial := flag.String("s", os.Getenv("ANDROID_SERIAL"), "device serial (default: the only device)")
	verbose := flag.Bool("v", false, "print driver debug messages")
	flag.Usage = usage
	flag.Parse()
	if flag.NArg() == 0 {
		usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	cfg := android.Config{
		// As the app does: an adb server that runs, or one started with
		// the adb of the Android SDK, Homebrew, PATH or Pinky
		// (adb.StartServer; PINKY_ADB names one).
		StartServer: func(ctx context.Context) error {
			t, err := adb.StartServer(ctx)
			if err == nil && t.Path != "" {
				fmt.Fprintf(os.Stderr, "androidctl: started an adb server with %s (%s, %s)\n", t.Path, t.Source, t.Version)
			}
			return err
		},
	}
	if *verbose {
		cfg.Logf = func(f string, a ...any) { fmt.Fprintf(os.Stderr, "\u00b7 "+f+"\n", a...) }
	}
	cmd, args := flag.Arg(0), flag.Args()[1:]
	if ok, err := wifi(ctx, cfg, cmd, args); ok {
		if err != nil {
			fmt.Fprintln(os.Stderr, "androidctl:", err)
			os.Exit(1)
		}
		return
	}
	p := android.NewProvider(cfg)
	if err := run(ctx, p, *serial, cmd, args); err != nil {
		fmt.Fprintln(os.Stderr, "androidctl:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: androidctl [-s serial] [-v] command [args]

commands:
  devices                         list phones
  observe [-json] [-ocr] [-all]   print the screen as agents read it; -ocr adds
                                  text the tree misses over web views and
                                  canvases (from a screenshot), -all all of it
  screenshot FILE.png             save a screenshot
  tap X Y [-long]                 tap, or long-press with -long
  swipe X1 Y1 X2 Y2 [MS]          swipe (default 300 ms)
  type [-append] [-submit] TEXT   type into the focused field
  key NAME                        back, home, recents, enter, tab, delete, ...
  launch APP                      start an app by package or name
  apps                            list launcher apps
  locked                          print whether the lock screen shows
  screen                          print whether the screen is on
  wake                            turn the screen on (KEYCODE_WAKEUP)
  unlock                          enter the PIN from $ANDROIDCTL_PIN, or the
                                  first line of stdin, on the lock screen
  files                           list the phone's Pinky folder (/sdcard/Pinky)
  pull PATH FILE                  copy PATH from the Pinky folder to FILE
  push FILE PATH                  copy FILE to PATH in the Pinky folder
  stream [-seconds N] [-max-size N] [-out FILE.h264]
                                  video statistics, optionally saving the stream
  settle [-max MS]                time until the screen stops changing
  run [-person] STEP...           run steps, timing each action and its settle;
                                  -person counts PersonWatcher signals meanwhile
  companion-install [-force]      install or update the companion app and turn
                                  its service on (setup only)
  companion-state                 on, off, missing, outdated or no_apk
  bg [-app APP] [-seconds N] [-out FILE.h264] [STEP...]
                                  open a virtual display and run steps on it

Wi-Fi (no phone needed; the adb server is $ADB_SERVER_SOCKET's):
  wifi-search [-seconds N]        search the network for wireless debugging
                                  services, as Pinky does itself
  pair-qr FILE.png                pair by QR code: write it to FILE.png, wait
                                  for the phone and print the progress
  pair-code ADDR CODE             pair with the address and six-digit code of
                                  Pair device with pairing code
  wifi-connect ADDR               connect to a paired phone at the address
                                  Wireless debugging shows
  wifi-reconnect [-seconds N]     list phones every 2 s with reconnecting on

steps: tap:X,Y longtap:X,Y swipe:X1,Y1,X2,Y2[,MS] key:NAME type:TEXT
       launch:APP sleep:MS observe shot:FILE.png state (bg only: whether
       the phone's own screen is on and locked)
`)
}

func run(ctx context.Context, p *android.Provider, serial, cmd string, args []string) error {
	if cmd == "devices" {
		return devices(ctx, p)
	}
	if serial == "" {
		infos, err := p.List(ctx)
		if err != nil {
			return err
		}
		var online []string
		for _, i := range infos {
			if i.State == driver.StateOnline {
				online = append(online, i.ID)
			}
		}
		if len(online) != 1 {
			return fmt.Errorf("%d phones online; choose one with -s", len(online))
		}
		serial = online[0]
	}
	d, err := p.OpenDevice(ctx, serial)
	if err != nil {
		return err
	}
	defer d.Close()
	switch cmd {
	case "observe":
		fs := flag.NewFlagSet("observe", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "print JSON")
		withOCR := fs.Bool("ocr", false, "add OCR text where the tree misses it")
		all := fs.Bool("all", false, "with -ocr, all the text the tree does not show")
		fs.Parse(args)
		return observe(ctx, d, *asJSON, driver.ObserveOptions{OCR: *withOCR || *all, OCRAll: *all})
	case "screenshot":
		if len(args) != 1 {
			return errors.New("usage: screenshot FILE.png")
		}
		return screenshot(ctx, d, args[0])
	case "tap":
		fs := flag.NewFlagSet("tap", flag.ExitOnError)
		long := fs.Bool("long", false, "long press")
		fs.Parse(reorder(args))
		x, y, err := ints2(fs.Args())
		if err != nil {
			return err
		}
		return timed("tap", func() error { return d.Tap(ctx, screen.Point{X: x, Y: y}, driver.TapOptions{LongPress: *long}) })
	case "swipe":
		if len(args) < 4 {
			return errors.New("usage: swipe X1 Y1 X2 Y2 [MS]")
		}
		v, err := atois(args)
		if err != nil {
			return err
		}
		dur := 300 * time.Millisecond
		if len(v) > 4 {
			dur = time.Duration(v[4]) * time.Millisecond
		}
		return timed("swipe", func() error {
			return d.Swipe(ctx, screen.Point{X: v[0], Y: v[1]}, screen.Point{X: v[2], Y: v[3]}, dur)
		})
	case "type":
		fs := flag.NewFlagSet("type", flag.ExitOnError)
		appendText := fs.Bool("append", false, "keep the field's text")
		submit := fs.Bool("submit", false, "press Enter afterwards")
		fs.Parse(args)
		text := strings.Join(fs.Args(), " ")
		err := timed("type", func() error {
			return d.Type(ctx, text, driver.TypeOptions{Append: *appendText, Submit: *submit})
		})
		if n := d.TakeInputNote(); n != "" {
			fmt.Println("note:", n)
		}
		return err
	case "key":
		if len(args) != 1 {
			return errors.New("usage: key NAME")
		}
		return timed("key", func() error { return d.Key(ctx, driver.Key(args[0])) })
	case "launch":
		if len(args) == 0 {
			return errors.New("usage: launch APP")
		}
		return timed("launch", func() error { return d.Launch(ctx, strings.Join(args, " ")) })
	case "apps":
		apps, err := d.Apps(ctx)
		if err != nil {
			return err
		}
		for _, a := range apps {
			sys := " "
			if a.System {
				sys = "*"
			}
			fmt.Printf("%s %-40s %s\n", sys, a.ID, a.Name)
		}
		return nil
	case "locked":
		start := time.Now()
		l, err := d.Locked(ctx)
		if err != nil {
			return err
		}
		fmt.Println(l)
		fmt.Fprintf(os.Stderr, "(%v)\n", time.Since(start).Round(time.Millisecond))
		return nil
	case "screen":
		start := time.Now()
		on, err := d.ScreenOn(ctx)
		if err != nil {
			return err
		}
		fmt.Println(on)
		fmt.Fprintf(os.Stderr, "(%v)\n", time.Since(start).Round(time.Millisecond))
		return nil
	case "wake":
		return timed("wake", func() error { return d.Wake(ctx) })
	case "unlock":
		// Never on the command line, where other processes can read it.
		pin := os.Getenv("ANDROIDCTL_PIN")
		if pin == "" {
			line, err := bufio.NewReader(os.Stdin).ReadString('\n')
			if err != nil && line == "" {
				return errors.New("unlock: give the PIN in $ANDROIDCTL_PIN or on stdin")
			}
			pin = strings.TrimRight(line, "\r\n")
		}
		return timed("unlock", func() error { return d.Unlock(ctx, pin) })
	case "files":
		start := time.Now()
		list, err := d.ListFiles(ctx)
		if err != nil {
			return err
		}
		for _, f := range list {
			kind := "f"
			if f.Dir {
				kind = "d"
			}
			fmt.Printf("%s %10d %s %s\n", kind, f.Size, f.ModTime.Format("2006-01-02 15:04:05"), f.Path)
		}
		fmt.Fprintf(os.Stderr, "(%d entries in %v)\n", len(list), time.Since(start).Round(time.Millisecond))
		return nil
	case "pull":
		if len(args) != 2 {
			return errors.New("usage: pull PATH FILE")
		}
		f, err := os.Create(args[1])
		if err != nil {
			return err
		}
		defer f.Close()
		return timed("pull", func() error { return d.Pull(ctx, args[0], f) })
	case "push":
		if len(args) != 2 {
			return errors.New("usage: push FILE PATH")
		}
		f, err := os.Open(args[0])
		if err != nil {
			return err
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		return timed("push", func() error { return d.Push(ctx, args[1], f, fi.Size(), fi.ModTime()) })
	case "stream":
		fs := flag.NewFlagSet("stream", flag.ExitOnError)
		secs := fs.Float64("seconds", 5, "how long to read")
		maxSize := fs.Int("max-size", 0, "longest side in pixels")
		out := fs.String("out", "", "write the H.264 stream (Annex B) to this file")
		fs.Parse(args)
		st, err := d.Stream(ctx, driver.StreamOptions{MaxSize: *maxSize})
		if err != nil {
			return err
		}
		return streamStats(ctx, st, time.Duration(*secs*float64(time.Second)), *out)
	case "settle":
		fs := flag.NewFlagSet("settle", flag.ExitOnError)
		maxMS := fs.Int("max", 3000, "limit in milliseconds")
		fs.Parse(args)
		start := time.Now()
		ok, err := d.WaitSettled(ctx, time.Duration(*maxMS)*time.Millisecond)
		if err != nil {
			return err
		}
		fmt.Printf("settled=%v after %v\n", ok, time.Since(start).Round(time.Millisecond))
		return nil
	case "run":
		fs := flag.NewFlagSet("run", flag.ExitOnError)
		person := fs.Bool("person", false, "count PersonWatcher signals during the steps")
		fs.Parse(args)
		if !*person {
			return runSteps(ctx, d, d, fs.Args())
		}
		signals := d.PersonInput()
		var n int
		done := make(chan struct{})
		go func() {
			defer close(done)
			for range signals {
				n++
				fmt.Printf("\u00b7 person input at %s\n", time.Now().Format("15:04:05.000"))
			}
		}()
		time.Sleep(2 * time.Second) // let getevent start
		err := runSteps(ctx, d, d, fs.Args())
		time.Sleep(time.Second)
		d.Close()
		<-done
		fmt.Printf("person input signals during the run: %d\n", n)
		return err
	case "companion-install":
		fs := flag.NewFlagSet("companion-install", flag.ExitOnError)
		force := fs.Bool("force", false, "install even when the phone has the same version (a build with other changes)")
		fs.Parse(args)
		start := time.Now()
		res, err := d.SetUpCompanion(ctx, *force)
		if err != nil {
			return err
		}
		switch {
		case res.Replaced:
			fmt.Printf("replaced build %d (signed with another key) with %s build %d\n", res.Before.Code, res.Bundled.Name, res.Bundled.Code)
		case res.Installed && res.Before.Code > 0:
			fmt.Printf("updated build %d to %s build %d\n", res.Before.Code, res.Bundled.Name, res.Bundled.Code)
		case res.Installed:
			fmt.Printf("installed %s build %d\n", res.Bundled.Name, res.Bundled.Code)
		default:
			fmt.Printf("build %d is installed; the bundled one is %d\n", res.Before.Code, res.Bundled.Code)
		}
		fmt.Printf("its service is on (%v)\n", time.Since(start).Round(time.Millisecond))
		return nil
	case "companion-state":
		start := time.Now()
		st, err := d.CompanionState(ctx)
		if err != nil {
			return err
		}
		fmt.Println(st)
		fmt.Fprintf(os.Stderr, "(%v)\n", time.Since(start).Round(time.Millisecond))
		return nil
	case "checkup":
		start := time.Now()
		cu, err := d.Checkup(ctx)
		if err != nil {
			return err
		}
		if len(args) > 0 && args[0] == "-json" {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(cu)
		}
		fmt.Printf("family %q, system %q\n", cu.Family, cu.System)
		for _, c := range cu.Checks {
			fix := ""
			if c.Fix != "" {
				fix = " [" + c.Fix + "]"
			}
			fmt.Printf("%-5s %-14s %s%s\n", c.Status, c.ID, c.Detail, fix)
		}
		fmt.Fprintf(os.Stderr, "(%v)\n", time.Since(start).Round(time.Millisecond))
		return nil
	case "bg":
		return background(ctx, d, args)
	}
	return fmt.Errorf("unknown command %q", cmd)
}

func devices(ctx context.Context, p *android.Provider) error {
	infos, err := p.List(ctx)
	if err != nil {
		return err
	}
	for _, i := range infos {
		fmt.Printf("%-16s %-12s %-14s Android %-4s %-4s %s", i.ID, i.State, i.Name, i.OSVersion, i.Connection, i.Model)
		if i.Detail != "" {
			fmt.Printf("  (%s)", i.Detail)
		}
		fmt.Println()
	}
	return nil
}

func observe(ctx context.Context, d *android.Device, asJSON bool, opt driver.ObserveOptions) error {
	start := time.Now()
	s, st, err := d.ObserveWithText(ctx, opt)
	if err != nil {
		return err
	}
	took := time.Since(start)
	if opt.OCR || st.Areas > 0 {
		fmt.Fprintf(os.Stderr, "(OCR: %d areas the tree misses; %s)\n", st.Areas, textSummary(st))
	}
	screen.NewRefs().Assign(s)
	redact(s)
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(s)
	}
	fmt.Println(s.Render())
	fmt.Fprintf(os.Stderr, "(%d elements in %v)\n", len(s.Elements), took.Round(time.Millisecond))
	return nil
}

// textSummary says what a read did about OCR.
func textSummary(st android.TextStats) string {
	switch {
	case st.Err != nil:
		return "failed: " + st.Err.Error()
	case st.Skipped != "":
		return "not run: " + st.Skipped
	}
	return fmt.Sprintf("%d lines from the %s, %d added; capture %v, OCR %v", st.Lines, st.From, st.Added,
		st.Capture.Round(time.Millisecond), st.OCR.Round(time.Millisecond))
}

func screenshot(ctx context.Context, d driver.Device, path string) error {
	start := time.Now()
	img, err := d.Screenshot(ctx)
	if err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		return err
	}
	b := img.Bounds()
	fmt.Fprintf(os.Stderr, "%dx%d screenshot in %v \u2192 %s\n", b.Dx(), b.Dy(), time.Since(start).Round(time.Millisecond), path)
	return nil
}

func timed(what string, f func() error) error {
	start := time.Now()
	if err := f(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s done in %v\n", what, time.Since(start).Round(time.Millisecond))
	return nil
}

func streamStats(ctx context.Context, st driver.Stream, dur time.Duration, out string) error {
	defer st.Close()
	var w io.Writer
	if out != "" {
		f, err := os.Create(out)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	timer := time.NewTimer(dur)
	defer timer.Stop()
	start := time.Now()
	var first, last time.Time
	var frames, keys, configs int
	var bytes int64
	var sizes []int
loop:
	for {
		select {
		case p, ok := <-st.Packets():
			if !ok {
				if err := st.Err(); err != nil {
					return err
				}
				break loop
			}
			now := time.Now()
			if w != nil {
				w.Write(p.Data)
			}
			if p.Config {
				configs++
				continue
			}
			if first.IsZero() {
				first = now
			}
			last = now
			frames++
			bytes += int64(len(p.Data))
			sizes = append(sizes, len(p.Data))
			if p.KeyFrame {
				keys++
			}
		case <-timer.C:
			break loop
		case <-ctx.Done():
			break loop
		}
	}
	vw, vh := st.Size()
	elapsed := time.Since(start)
	fmt.Printf("video %dx%d %s, %v\n", vw, vh, st.Codec(), elapsed.Round(time.Millisecond))
	fmt.Printf("frames %d (key frames %d, config packets %d), %d bytes\n", frames, keys, configs, bytes)
	if frames > 1 {
		span := last.Sub(first).Seconds()
		fmt.Printf("frame rate %.1f fps over %.2fs of frames; %.1f fps over the whole run\n",
			float64(frames-1)/span, span, float64(frames)/elapsed.Seconds())
		fmt.Printf("bit rate %.2f Mbit/s over the whole run\n", float64(bytes)*8/elapsed.Seconds()/1e6)
		sort.Ints(sizes)
		fmt.Printf("frame bytes: min %d, median %d, p90 %d, max %d\n",
			sizes[0], sizes[len(sizes)/2], sizes[len(sizes)*9/10], sizes[len(sizes)-1])
	}
	return nil
}

// stepper is what run steps need: a device, optionally with settle.
// pinchStep runs a pinch step: CX,CY,FROM,TO[,MS[,ANGLE[,ROTATE]]].
func pinchStep(ctx context.Context, d driver.Device, arg string) error {
	p, ok := d.(driver.Pincher)
	if !ok {
		return fmt.Errorf("pinch: %w", driver.ErrUnsupported)
	}
	v, err := atois(strings.Split(arg, ","))
	if err != nil {
		return err
	}
	if len(v) < 4 {
		return errors.New("pinch needs CX,CY,FROM,TO")
	}
	g := driver.Pinch{Center: screen.Point{X: v[0], Y: v[1]}, From: v[2], To: v[3], Angle: 90, Duration: 500 * time.Millisecond}
	if len(v) > 4 {
		g.Duration = time.Duration(v[4]) * time.Millisecond
	}
	if len(v) > 5 {
		g.Angle = float64(v[5])
	}
	if len(v) > 6 {
		g.Rotate = float64(v[6])
	}
	return p.Pinch(ctx, g)
}

type stepper interface {
	driver.Device
	driver.Settler
}

type statser interface {
	VideoStats(ctx context.Context, since time.Time) (android.VideoStats, error)
}

// trace prints every video packet of each step when set (ANDROIDCTL_TRACE=1).
var trace = os.Getenv("ANDROIDCTL_TRACE") != ""

func runSteps(ctx context.Context, d stepper, st statser, steps []string) error {
	runStart := time.Now()
	defer videoSummary(ctx, st, runStart)
	var settleTimes []time.Duration
	for i, step := range steps {
		if i > 0 {
			// Pace input like a person.
			time.Sleep(300 * time.Millisecond)
		}
		kind, arg, _ := strings.Cut(step, ":")
		start := time.Now()
		var err error
		action := true
		switch kind {
		case "tap", "longtap":
			var x, y int
			x, y, err = ints2(strings.Split(arg, ","))
			if err == nil {
				err = d.Tap(ctx, screen.Point{X: x, Y: y}, driver.TapOptions{LongPress: kind == "longtap"})
			}
		case "swipe":
			var v []int
			v, err = atois(strings.Split(arg, ","))
			if err == nil && len(v) < 4 {
				err = errors.New("swipe needs X1,Y1,X2,Y2")
			}
			if err == nil {
				dur := 300 * time.Millisecond
				if len(v) > 4 {
					dur = time.Duration(v[4]) * time.Millisecond
				}
				err = d.Swipe(ctx, screen.Point{X: v[0], Y: v[1]}, screen.Point{X: v[2], Y: v[3]}, dur)
			}
		case "pinch":
			err = pinchStep(ctx, d, arg)
		case "key":
			err = d.Key(ctx, driver.Key(arg))
		case "type":
			err = d.Type(ctx, arg, driver.TypeOptions{})
			if n, ok := d.(driver.InputNoter); ok {
				if note := n.TakeInputNote(); note != "" {
					fmt.Println("note:", note)
				}
			}
		case "launch":
			err = d.Launch(ctx, arg)
		case "sleep":
			action = false
			var ms int
			ms, err = strconv.Atoi(arg)
			time.Sleep(time.Duration(ms) * time.Millisecond)
		case "observe":
			action = false
			var s *screen.Screen
			s, err = d.Observe(ctx, driver.ObserveOptions{})
			if err == nil {
				screen.NewRefs().Assign(s)
				redact(s)
				fmt.Println(s.Render())
				fmt.Printf("(observe took %v)\n", time.Since(start).Round(time.Millisecond))
			}
		case "shot":
			action = false
			err = screenshot(ctx, d, arg)
		case "state":
			// The phone's own screen, for background display checks.
			action = false
			if ps, ok := d.(phoneStater); ok {
				var line string
				line, err = ps.phoneState(ctx)
				fmt.Printf("phone: %s\n", line)
			}
		default:
			err = fmt.Errorf("unknown step %q", step)
		}
		if err != nil {
			return fmt.Errorf("step %d %q: %w", i+1, step, err)
		}
		if !action {
			continue
		}
		acted := time.Now()
		ok, err := d.WaitSettled(ctx, 0)
		if err != nil {
			return fmt.Errorf("step %d %q: settle: %w", i+1, step, err)
		}
		done := time.Now()
		total := done.Sub(start)
		settleTimes = append(settleTimes, total)
		line := fmt.Sprintf("%-28s action %6v  settle %6v  total %6v  settled=%v", step,
			acted.Sub(start).Round(time.Millisecond), done.Sub(acted).Round(time.Millisecond), total.Round(time.Millisecond), ok)
		if vs, err := st.VideoStats(ctx, start); err == nil {
			var n, changes, bytes int
			for _, p := range vs.Packets {
				if p.Config {
					continue
				}
				n++
				bytes += p.Size
				if p.Change {
					changes++
				}
			}
			line += fmt.Sprintf("  frames %d (changes %d), %d bytes", n, changes, bytes)
			if trace {
				var prev time.Duration
				for i, p := range vs.Packets {
					gap := time.Duration(0)
					if i > 0 {
						gap = p.PTS - prev
					}
					prev = p.PTS
					flags := ""
					if p.Config {
						flags += " config"
					}
					if p.KeyFrame {
						flags += " key"
					}
					if p.Change {
						flags += " CHANGE"
					}
					line += fmt.Sprintf("\n    +%5dms  pts gap %4dms  %7d B%s", p.At.Sub(start).Milliseconds(), gap.Milliseconds(), p.Size, flags)
				}
			}
		}
		fmt.Println(line)
	}
	if len(settleTimes) > 0 {
		sorted := append([]time.Duration(nil), settleTimes...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		fmt.Printf("action\u2192settled over %d actions: median %v, min %v, max %v\n", len(sorted),
			sorted[len(sorted)/2].Round(time.Millisecond), sorted[0].Round(time.Millisecond), sorted[len(sorted)-1].Round(time.Millisecond))
	}
	return nil
}

// videoSummary prints the frame rate and bit rate of the video since
// start: the average over the whole run and the busiest second.
func videoSummary(ctx context.Context, st statser, start time.Time) {
	vs, err := st.VideoStats(ctx, start)
	if err != nil {
		return
	}
	elapsed := time.Since(start)
	var frames, bytes int
	var times []time.Time
	var sizes []int
	for _, p := range vs.Packets {
		if p.Config {
			continue
		}
		frames++
		bytes += p.Size
		times = append(times, p.At)
		sizes = append(sizes, p.Size)
	}
	if frames == 0 || elapsed <= 0 {
		return
	}
	// Busiest one-second window.
	var peakFrames, peakBytes, j, wbytes int
	for i := range times {
		wbytes += sizes[i]
		for times[i].Sub(times[j]) >= time.Second {
			wbytes -= sizes[j]
			j++
		}
		if n := i - j + 1; n > peakFrames {
			peakFrames = n
		}
		peakBytes = max(peakBytes, wbytes)
	}
	fmt.Printf("video %dx%d %s over %v: %d frames, %.1f fps and %.2f Mbit/s on average; busiest second %d fps, %.2f Mbit/s\n",
		vs.Width, vs.Height, vs.Codec, elapsed.Round(time.Millisecond), frames,
		float64(frames)/elapsed.Seconds(), float64(bytes)*8/elapsed.Seconds()/1e6, peakFrames, float64(peakBytes)*8/1e6)
}

// phoneStater reports the phone's own screen during background steps.
type phoneStater interface {
	phoneState(ctx context.Context) (string, error)
}

// bgSteps runs steps on a background display and reads the main screen's
// state for the state step.
type bgSteps struct {
	*android.Background
	phone *android.Device
}

func (b bgSteps) phoneState(ctx context.Context) (string, error) {
	on, err := b.phone.ScreenOn(ctx)
	if err != nil {
		return "", err
	}
	locked, err := b.phone.Locked(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("screen on %v, locked %v", on, locked), nil
}

func background(ctx context.Context, d *android.Device, args []string) error {
	fs := flag.NewFlagSet("bg", flag.ExitOnError)
	app := fs.String("app", "", "app to start on the virtual display")
	out := fs.String("out", "", "write the display's H.264 stream to this file")
	secs := fs.Float64("seconds", 2, "seconds to keep streaming after the steps")
	decor := fs.Bool("decorations", false, "show the status bar, taskbar and launcher on the display")
	keep := fs.Bool("keep", false, "move every app on the display to the main display when it closes")
	fs.Parse(args)
	start := time.Now()
	b, err := d.OpenDisplay(ctx, android.DisplayOptions{App: *app, SystemDecorations: *decor, KeepContent: *keep})
	if err != nil {
		return err
	}
	defer b.Close()
	fmt.Printf("virtual display %d ready in %v\n", b.DisplayID(), time.Since(start).Round(time.Millisecond))
	var st driver.Stream
	var done chan struct{}
	if *out != "" {
		st, err = b.Stream(ctx, driver.StreamOptions{})
		if err != nil {
			return err
		}
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer f.Close()
		done = make(chan struct{})
		go func() {
			defer close(done)
			for p := range st.Packets() {
				f.Write(p.Data)
			}
		}()
	}
	if _, err := b.WaitSettled(ctx, 0); err != nil {
		return err
	}
	if err := runSteps(ctx, bgSteps{b, d}, b, fs.Args()); err != nil {
		return err
	}
	time.Sleep(time.Duration(*secs * float64(time.Second)))
	if st != nil {
		st.Close()
		<-done
	}
	return nil
}

// redact hides notification text, which may hold the owner's messages,
// unless ANDROIDCTL_NOTIFICATIONS=1.
func redact(s *screen.Screen) {
	if os.Getenv("ANDROIDCTL_NOTIFICATIONS") != "" {
		return
	}
	for i, ev := range s.Events {
		if ev.Kind == screen.EventNotification {
			s.Events[i].Text = "(text hidden)"
		}
	}
}

func ints2(args []string) (int, int, error) {
	v, err := atois(args)
	if err != nil {
		return 0, 0, err
	}
	if len(v) != 2 {
		return 0, 0, errors.New("want X Y")
	}
	return v[0], v[1], nil
}

func atois(args []string) ([]int, error) {
	out := make([]int, len(args))
	for i, a := range args {
		n, err := strconv.Atoi(strings.TrimSpace(a))
		if err != nil {
			return nil, fmt.Errorf("not a number: %q", a)
		}
		out[i] = n
	}
	return out, nil
}

// reorder moves flags in front of positional arguments, so "tap 1 2 -long"
// works like "tap -long 1 2".
func reorder(args []string) []string {
	var flags, rest []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") && len(a) > 1 && (a[1] < '0' || a[1] > '9') {
			flags = append(flags, a)
		} else {
			rest = append(rest, a)
		}
	}
	return append(flags, rest...)
}

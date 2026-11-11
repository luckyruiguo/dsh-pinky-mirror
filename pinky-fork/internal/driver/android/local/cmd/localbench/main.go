// Command localbench measures the standalone prototype's actions on the
// phone it runs on, through the service's local socket only: no adb input,
// no screencap. Run it on the phone (adb push, then adb shell, whose uid the
// service accepts) while Pinky Standalone's accessibility service is on.
//
// It opens Settings, scrolls it, taps its search field, types into it,
// long-presses it, goes back and home, and prints each action's latency.
// It takes no screenshot it keeps: pictures are decoded and dropped.
//
//	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o localbench ./internal/driver/android/local/cmd/localbench
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/local"
	"github.com/DIYgod/pinky/internal/screen"
)

var (
	n       = flag.Int("n", 10, "repetitions of each action")
	app     = flag.String("app", "com.android.settings", "app to act on")
	search  = flag.String("search", "search", "text of the label of the field to tap and type into (case-insensitive)")
	text    = flag.String("text", "pinky", "text to type")
	results = map[string][]time.Duration{}
	order   []string
)

func main() {
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "localbench:", err)
		report()
		os.Exit(1)
	}
	report()
}

func run(ctx context.Context) error {
	t0 := time.Now()
	d, err := local.Open(ctx, local.Standalone)
	if err != nil {
		return err
	}
	defer d.Close()
	record("open (connect + ping)", time.Since(t0))
	info := d.Info()
	fmt.Printf("phone: %s, Android %s\n", info.Name, info.OSVersion)

	for range *n {
		if _, err := timed("ping", func() error { _, err := d.Client().Ping(ctx); return err }); err != nil {
			return err
		}
	}
	var apps []driver.App
	if _, err := timed("apps", func() (err error) { apps, err = d.Apps(ctx); return }); err != nil {
		return err
	}
	fmt.Printf("apps: %d launchable\n", len(apps))

	// The service refuses to open Pinky itself.
	if err := d.Launch(ctx, local.Standalone.Package); !errors.Is(err, local.ErrOwnUI) {
		return fmt.Errorf("launching Pinky itself: want ErrOwnUI, got %v", err)
	}
	fmt.Println("own UI: launching Pinky itself refused")

	if err := d.Key(ctx, driver.KeyHome); err != nil {
		return err
	}
	settle(ctx, d)
	t0launch := time.Now()
	if _, err := timed("launch", func() error { return d.Launch(ctx, *app) }); err != nil {
		return err
	}
	settleTimed(ctx, d, "launch → settled")
	// A cold start can draw its first window after the screen first
	// settles: read until the app is in front with something on it.
	tf := time.Now()
	for {
		s, err := d.Observe(ctx, driver.ObserveOptions{})
		if err != nil {
			return err
		}
		if s.App.ID == *app && len(s.Elements) > 0 {
			break
		}
		if time.Since(tf) > 10*time.Second {
			return fmt.Errorf("%s did not come to the front: %q with %d elements", *app, s.App.ID, len(s.Elements))
		}
		time.Sleep(50 * time.Millisecond)
	}
	record("launch → app's screen read", time.Since(t0launch))

	var s *screen.Screen
	for range *n {
		if _, err := timed("observe (tree)", func() (err error) { s, err = d.Observe(ctx, driver.ObserveOptions{}); return }); err != nil {
			return err
		}
	}
	fmt.Printf("screen: %s, %d elements, %dx%d, keyboard %v, locked %v\n", s.App.ID, len(s.Elements), s.Width, s.Height, s.Keyboard, s.Locked)
	if s.App.ID != *app {
		return fmt.Errorf("%s is not in front: %s", *app, s.App.ID)
	}

	for range *n {
		var shot *local.Shot
		if _, err := timed("screenshot jpeg q80 (round trip)", func() (err error) { shot, err = d.Capture(ctx, 80); return }); err != nil {
			return err
		}
		record("  of which capture", time.Duration(shot.CaptureMS)*time.Millisecond)
		record("  of which encode", time.Duration(shot.EncodeMS)*time.Millisecond)
		recordN("  jpeg size (KB)", shot.Bytes/1024)
		if _, err := timed("  decode in Go", func() error { _, err := shot.Decode(); return err }); err != nil {
			return err
		}
	}
	for range 3 {
		var shot *local.Shot
		if _, err := timed("screenshot png (round trip)", func() (err error) { shot, err = d.Capture(ctx, 0); return }); err != nil {
			return err
		}
		recordN("  png size (KB)", shot.Bytes/1024)
	}

	mid := screen.Point{X: s.Width / 2, Y: s.Height / 2}
	up := screen.Point{X: s.Width / 2, Y: s.Height / 4}
	for i := range *n {
		from, to := mid, up
		if i%2 == 1 {
			from, to = up, mid
		}
		took, err := timed("swipe 300 ms (round trip)", func() error { return d.Swipe(ctx, from, to, 300*time.Millisecond) })
		if err != nil {
			return err
		}
		record("  swipe overhead beyond its 300 ms", took-300*time.Millisecond)
		settleTimed(ctx, d, "swipe → settled")
	}

	field, ok := find(s, *search)
	if !ok {
		return fmt.Errorf("no element labelled %q on %s", *search, *app)
	}
	took, err := timed("tap (round trip)", func() error { return d.Tap(ctx, field.Bounds.Center(), driver.TapOptions{}) })
	if err != nil {
		return err
	}
	record("  tap overhead beyond its 50 ms", took-50*time.Millisecond)
	settleTimed(ctx, d, "tap → settled")
	s, err = d.Observe(ctx, driver.ObserveOptions{})
	if err != nil {
		return err
	}
	input, ok := focusedInput(s)
	if !ok {
		return fmt.Errorf("tapping %q focused no field (app %s)", field.Label, s.App.ID)
	}
	fmt.Printf("tapped %q: field %q has focus, keyboard %v\n", field.Label, input.Label, s.Keyboard)

	for i := range *n {
		want := fmt.Sprintf("%s %d", *text, i)
		if _, err := timed("type (set_text)", func() error { return d.Type(ctx, want, driver.TypeOptions{}) }); err != nil {
			return err
		}
		s, err = d.Observe(ctx, driver.ObserveOptions{})
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(s.Elements, func(e screen.Element) bool { return strings.Contains(e.Label, want) }) {
			return fmt.Errorf("typed %q but the screen does not show it", want)
		}
		// Tap the field again, a harmless tap with the field focused.
		took, err := timed("tap (round trip)", func() error { return d.Tap(ctx, input.Bounds.Center(), driver.TapOptions{}) })
		if err != nil {
			return err
		}
		record("  tap overhead beyond its 50 ms", took-50*time.Millisecond)
	}
	fmt.Printf("typed %d times, each read back from the screen\n", *n)

	for range 3 {
		took, err := timed("long press 800 ms (round trip)", func() error {
			return d.Tap(ctx, input.Bounds.Center(), driver.TapOptions{LongPress: true})
		})
		if err != nil {
			return err
		}
		record("  long press overhead beyond its 800 ms", took-800*time.Millisecond)
		settle(ctx, d)
		_ = d.Key(ctx, driver.KeyBack) // close the text selection's toolbar
		settle(ctx, d)
	}

	for range 3 {
		if _, err := timed("back", func() error { return d.Key(ctx, driver.KeyBack) }); err != nil {
			return err
		}
		settleTimed(ctx, d, "back → settled")
	}
	if _, err := timed("home", func() error { return d.Key(ctx, driver.KeyHome) }); err != nil {
		return err
	}
	settleTimed(ctx, d, "home → settled")
	return nil
}

func find(s *screen.Screen, label string) (screen.Element, bool) {
	l := strings.ToLower(label)
	for _, e := range s.Elements {
		if strings.Contains(strings.ToLower(e.Label), l) || strings.Contains(strings.ToLower(e.ID), l) {
			return e, true
		}
	}
	return screen.Element{}, false
}

func focusedInput(s *screen.Screen) (screen.Element, bool) {
	for _, e := range s.Elements {
		if e.Has(screen.StateFocused) || e.Role == "textbox" || e.Role == "searchbox" {
			return e, true
		}
	}
	return screen.Element{}, false
}

func settle(ctx context.Context, d *local.Device) { _, _ = d.WaitSettled(ctx, 3*time.Second) }

func settleTimed(ctx context.Context, d *local.Device, name string) {
	t := time.Now()
	settle(ctx, d)
	record(name, time.Since(t))
}

func timed(name string, f func() error) (time.Duration, error) {
	t := time.Now()
	err := f()
	took := time.Since(t)
	if err != nil {
		return took, fmt.Errorf("%s: %w", name, err)
	}
	record(name, took)
	return took, nil
}

func record(name string, d time.Duration) {
	if _, ok := results[name]; !ok {
		order = append(order, name)
	}
	results[name] = append(results[name], d)
}

// recordN records a count in a duration's place (1 ms per unit).
func recordN(name string, v int) { record(name, time.Duration(v)*time.Millisecond) }

func report() {
	fmt.Printf("\n%-40s %4s %8s %8s %8s %8s\n", "action", "n", "min", "p50", "p90", "max")
	for _, name := range order {
		ds := slices.Clone(results[name])
		slices.Sort(ds)
		q := func(f float64) string { return ms(ds[min(len(ds)-1, int(f*float64(len(ds))))]) }
		fmt.Printf("%-40s %4d %8s %8s %8s %8s\n", name, len(ds), ms(ds[0]), q(0.5), q(0.9), ms(ds[len(ds)-1]))
	}
}

func ms(d time.Duration) string { return fmt.Sprintf("%.1f", float64(d.Microseconds())/1000) }

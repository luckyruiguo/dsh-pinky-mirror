package gateway

import (
	"context"
	"image"
	"image/color"
	"math"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// A synthetic Pixel home screen, 1080×2400: a grid of app icons on a
// gradient wallpaper, and a dock that stays when the page shrinks.
const (
	homeW, homeH = 1080, 2400
	iconSide     = 150
)

type homeScreen struct {
	page, dock []homeIcon
}

type homeIcon struct {
	r   screen.Rect
	pat [5][5]uint8
	hue int
}

func newHome(seed int64) *homeScreen {
	rng := rand.New(rand.NewSource(seed))
	icon := func(x, y int) homeIcon {
		ic := homeIcon{r: screen.Rect{X: x, Y: y, W: iconSide, H: iconSide}, hue: rng.Intn(3)}
		for i := range ic.pat {
			for j := range ic.pat[i] {
				ic.pat[i][j] = uint8(30 + rng.Intn(200))
			}
		}
		return ic
	}
	h := &homeScreen{}
	for row := range 5 {
		for col := range 4 {
			h.page = append(h.page, icon(75+col*250, 420+row*300))
		}
	}
	for col := range 4 {
		h.dock = append(h.dock, icon(75+col*250, 2080))
	}
	return h
}

func (ic homeIcon) at(x, y float64) (color.RGBA, bool) {
	fx, fy := x-float64(ic.r.X), y-float64(ic.r.Y)
	if fx < 0 || fy < 0 || fx >= float64(ic.r.W) || fy >= float64(ic.r.H) {
		return color.RGBA{}, false
	}
	v := ic.pat[int(fy)*5/ic.r.H][int(fx)*5/ic.r.W]
	c := color.RGBA{v, v, v, 255}
	switch ic.hue {
	case 1:
		c.R = 255 - v
	case 2:
		c.B = 255 - v
	}
	return c, true
}

// draw renders the screen with the page scaled by s about c, without the
// icons in skip, and with extra icons drawn on top.
func (h *homeScreen) draw(w, hh int, s float64, c screen.Point, skip map[int]bool, extra ...homeIcon) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, hh))
	k := float64(homeW) / float64(w)
	for py := range hh {
		for px := range w {
			x, y := (float64(px)+0.5)*k, (float64(py)+0.5)*k
			col := color.RGBA{uint8(60 + y/2400*80), uint8(90 + x/1080*40), 140, 255}
			for _, ic := range h.dock {
				if v, ok := ic.at(x, y); ok {
					col = v
				}
			}
			ux, uy := float64(c.X)+(x-float64(c.X))/s, float64(c.Y)+(y-float64(c.Y))/s
			for i, ic := range h.page {
				if skip[i] {
					continue
				}
				if v, ok := ic.at(ux, uy); ok {
					col = v
				}
			}
			for _, ic := range extra {
				if v, ok := ic.at(x, y); ok {
					col = v
				}
			}
			img.SetRGBA(px, py, col)
		}
	}
	return img
}

func (h *homeScreen) marks() []screen.Rect {
	var out []screen.Rect
	for _, ic := range h.page {
		out = append(out, ic.r)
	}
	for _, ic := range h.dock {
		out = append(out, ic.r)
	}
	return out
}

func centre(r screen.Rect) screen.Point { return screen.Point{X: r.X + r.W/2, Y: r.Y + r.H/2} }

var shrinkTo = screen.Point{X: 540, Y: 990}

func shrunk(p screen.Point, s float64) screen.Point {
	return screen.Point{
		X: int(math.Round(float64(shrinkTo.X) + s*float64(p.X-shrinkTo.X))),
		Y: int(math.Round(float64(shrinkTo.Y) + s*float64(p.Y-shrinkTo.Y))),
	}
}

func TestFindDropFollowsShrunkPage(t *testing.T) {
	home := newHome(1)
	held := 0 // the top-left app is picked up
	lifted := home.page[held]
	lifted.r.X, lifted.r.Y = 140, 380 // under the finger, off its place
	before := home.draw(homeW, homeH, 1, shrinkTo, nil)
	marks := home.marks()[1:]
	for _, tc := range []struct {
		name   string
		scale  float64
		w, h   int
		target int // page icon the drop is on
	}{
		{"0.78", 0.78, homeW, homeH, 10},
		{"0.78 bottom right", 0.78, homeW, homeH, 19},
		{"0.85", 0.85, homeW, homeH, 6},
		{"0.66", 0.66, homeW, homeH, 13},
		{"half size frame", 0.78, homeW / 2, homeH / 2, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			during := home.draw(tc.w, tc.h, tc.scale, shrinkTo, map[int]bool{held: true}, lifted)
			to := centre(home.page[tc.target].r)
			start := time.Now()
			f := findDrop(before, during, homeW, homeH, to, marks)
			took := time.Since(start)
			want := shrunk(to, tc.scale)
			t.Logf("to %v → %v (want %v) scale %.2f score %.3f in %v", to, f.To, want, f.Scale, f.Score, took)
			if f.Why != "" || !f.Shrunk {
				t.Fatalf("findDrop = %+v, want a shrink", f)
			}
			if dist(f.To, want) > 8 {
				t.Errorf("drop at %v, want %v", f.To, want)
			}
		})
	}
}

func TestFindDropLeavesStill(t *testing.T) {
	home := newHome(2)
	before := home.draw(homeW, homeH, 1, shrinkTo, nil)
	shrunkFrame := home.draw(homeW, homeH, 0.78, shrinkTo, map[int]bool{0: true})
	dockTo := centre(home.dock[2].r)
	for _, tc := range []struct {
		name   string
		during image.Image
		to     screen.Point
	}{
		{"same picture", before, centre(home.page[9].r)},
		{"drop on the dock", shrunkFrame, dockTo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := findDrop(before, tc.during, homeW, homeH, tc.to, home.marks()[1:])
			if f.Why != "" || f.Shrunk || f.To != tc.to {
				t.Errorf("findDrop = %+v, want %v unchanged", f, tc.to)
			}
		})
	}
}

func TestFindDropGivesUp(t *testing.T) {
	home := newHome(3)
	before := home.draw(homeW, homeH, 1, shrinkTo, nil)
	flat := image.NewGray(image.Rect(0, 0, homeW, homeH))
	for i := range flat.Pix {
		flat.Pix[i] = 128
	}
	other := newHome(4).draw(homeW, homeH, 1, shrinkTo, nil)
	to := centre(home.page[10].r)
	for _, tc := range []struct {
		name   string
		during image.Image
	}{
		{"flat", flat},
		{"another screen", other},
		{"another shape", image.NewRGBA(image.Rect(0, 0, 1000, 1000))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := findDrop(before, tc.during, homeW, homeH, to, home.marks())
			if f.Why == "" {
				t.Errorf("findDrop = %+v, want it to give up", f)
			}
		})
	}
	if f := findDrop(before, before, homeW, homeH, to, nil); f.Why == "" {
		t.Errorf("findDrop without marks = %+v, want it to give up", f)
	}
}

func TestWithPickup(t *testing.T) {
	stops := []driver.DragStop{{Point: screen.Point{X: 1060, Y: 900}, Pause: time.Second}, {Point: screen.Point{X: 300, Y: 800}}}
	g := driver.Drag{From: screen.Point{X: 200, Y: 900}, Hold: 800 * time.Millisecond, Stops: stops}
	r := withPickup(g, homeW, homeH)
	if len(r.Stops) != 3 || r.Stops[1] != stops[0] || r.Stops[2] != stops[1] {
		t.Fatalf("stops = %+v, want the pickup before %+v", r.Stops, stops)
	}
	if p := r.Stops[0].Point; p != (screen.Point{X: 308, Y: 900}) {
		t.Errorf("pickup at %v, want 308,900 (a tenth of the width toward the first stop)", p)
	}
	// Straight up when the first stop is where it starts; never at an edge.
	g = driver.Drag{From: screen.Point{X: 10, Y: 100}, Stops: []driver.DragStop{{Point: screen.Point{X: 10, Y: 100}}}}
	if p := withPickup(g, homeW, homeH).Stops[0].Point; p != (screen.Point{X: 90, Y: 192}) {
		t.Errorf("pickup at %v, want 90,192", p)
	}
	d := dropAt(r.Stops[1:], screen.Point{X: 400, Y: 860})
	if d[0] != stops[0] || d[1].Point != (screen.Point{X: 400, Y: 860}) || stops[1].Point != (screen.Point{X: 300, Y: 800}) {
		t.Errorf("dropAt = %+v; stops %+v", d, stops)
	}
}

// followPhone is a demo Android phone whose home screen shrinks its page
// once an app is held up, as Pixel Launcher does.
type followPhone struct {
	demo.Android
	home    *homeScreen
	held    int // the page element picked up
	mu      sync.Mutex
	lifted  bool
	shots   int
	shot    chan struct{}
	dropped []driver.Drag
}

func (f *followPhone) Screenshot(context.Context) (image.Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shots++
	if f.shots == 1 {
		close(f.shot)
	}
	if !f.lifted {
		return f.home.draw(homeW, homeH, 1, shrinkTo, nil), nil
	}
	return f.home.draw(homeW, homeH, 0.78, shrinkTo, map[int]bool{f.held: true}), nil
}

func (f *followPhone) DragReplan(ctx context.Context, g driver.Drag, at int, replan func(context.Context) []driver.DragStop) error {
	stops := g.Stops
	if replan != nil {
		<-f.shot // the picture from before the drag
		f.mu.Lock()
		f.lifted = true
		f.mu.Unlock()
		if rest := replan(ctx); rest != nil {
			stops = append(append([]driver.DragStop(nil), g.Stops[:at+1]...), rest...)
		}
	}
	g.Stops = stops
	f.mu.Lock()
	f.dropped = append(f.dropped, g)
	f.lifted = false
	f.mu.Unlock()
	return f.Android.Drag(ctx, g)
}

type followProvider struct {
	*demo.Provider
	phone *followPhone
}

func (p followProvider) Open(ctx context.Context, id string) (driver.Device, error) {
	dev, err := p.Provider.Open(ctx, id)
	if err != nil {
		return nil, err
	}
	p.phone.Android = dev.(demo.Android)
	return p.phone, nil
}

// TestDragFollowsHomeScreen drags an app onto another on a home screen
// that shrinks: the finger lets go where the other app went, and the
// result says so. A drag without a hold is left as it is.
func TestDragFollowsHomeScreen(t *testing.T) {
	ctx := context.Background()
	phone := &followPhone{shot: make(chan struct{})}
	dev := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	g, _ := newTestGateway(t, func(o *Options) {
		o.Providers = []driver.Provider{followProvider{demo.NewProvider(driver.Android, dev), phone}}
	})
	s, res := connect(t, g, "Tidy the home screen")
	// The home screen's pictures show its elements as textured boxes.
	home := newHome(5)
	home.page, home.dock = nil, nil
	var picked, onto *screen.Element
	for i, e := range res.Screen.Elements {
		if e.Source != screen.SourceTree || e.Bounds.Y < 300 {
			continue
		}
		ic := newHome(int64(10 + i)).page[0]
		ic.r = e.Bounds
		home.page = append(home.page, ic)
		switch {
		case picked == nil:
			picked = &res.Screen.Elements[i]
			phone.held = len(home.page) - 1
		case onto == nil && e.Bounds.Y > 800:
			onto = &res.Screen.Elements[i]
		}
	}
	if picked == nil || onto == nil {
		t.Fatalf("no apps to drag on %+v", res.Screen.Elements)
	}
	phone.home = home
	to := onto.Bounds.Center()
	res = s.Act(ctx, Call{}, Action{Tool: "drag", Ref: picked.Ref, To: &to, HoldMS: 800})
	mustStatus(t, res, StatusSettled)
	want := shrunk(to, 0.78)
	if len(phone.dropped) != 1 {
		t.Fatalf("drags %+v", phone.dropped)
	}
	got := phone.dropped[0]
	if len(got.Stops) != 2 || dist(got.To(), want) > 8 {
		t.Fatalf("drag %+v, want a pickup and a drop near %v", got, want)
	}
	if !strings.Contains(res.Message, "shrank the home screen page") {
		t.Errorf("message %q, want it to say where it let go", res.Message)
	}

	// No hold: the page scrolls instead, and the drag runs as given.
	res = s.Act(ctx, Call{}, Action{Tool: "drag", Ref: picked.Ref, To: &to})
	mustStatus(t, res, StatusSettled)
	ins := dev.Inputs()
	if in := ins[len(ins)-1]; len(phone.dropped) != 1 || in.Kind != "drag" || in.To != to {
		t.Errorf("input %+v after %d followed drags, want a plain drag to %v", in, len(phone.dropped), to)
	}
	if strings.Contains(res.Message, "home screen page") {
		t.Errorf("message %q", res.Message)
	}
}

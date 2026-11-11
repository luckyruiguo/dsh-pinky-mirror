package ios

import (
	"context"
	"encoding/json"
	"math"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// pointerActions is the body of POST /actions.
type pointerActions struct {
	Actions []struct {
		Type       string `json:"type"`
		ID         string `json:"id"`
		Parameters struct {
			PointerType string `json:"pointerType"`
		} `json:"parameters"`
		Actions []map[string]any `json:"actions"`
	} `json:"actions"`
}

// TestWDAPinch checks a pinch's W3C actions: two touch pointers with as
// many ticks each, down at the gesture's start, moving together for its
// duration, and up at its end, in WDA's points.
func TestWDAPinch(t *testing.T) {
	f := &fakeWDA{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	d := &WDADevice{c: newWDAURL(srv.URL, ""), scale: 3, width: 1179, height: 2556}
	g := driver.Pinch{Center: screen.Point{X: 600, Y: 1200}, From: 300, To: 900, Angle: 0, Rotate: 90, Duration: 400 * time.Millisecond}
	if err := d.Pinch(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	var body pointerActions
	if err := json.Unmarshal([]byte(f.bodies["POST /actions"][0]), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Actions) != 2 || body.Actions[0].ID == body.Actions[1].ID {
		t.Fatalf("%d pointers: %+v", len(body.Actions), body.Actions)
	}
	n := g.Steps(wdaPinchStep)
	for i, p := range body.Actions {
		acts := p.Actions
		if p.Type != "pointer" || p.Parameters.PointerType != "touch" || len(acts) != n+3 {
			t.Fatalf("finger %d: %s %s with %d actions, want %d", i, p.Type, p.Parameters.PointerType, len(acts), n+3)
		}
		if acts[1]["type"] != "pointerDown" || acts[len(acts)-1]["type"] != "pointerUp" {
			t.Fatalf("finger %d: %v", i, acts)
		}
		var total float64
		for _, a := range acts[2 : len(acts)-1] {
			total += a["duration"].(float64)
		}
		if math.Abs(total-400) > float64(n) {
			t.Errorf("finger %d moves for %v ms", i, total)
		}
		// Start and end, in points (3 pixels each).
		for _, c := range []struct {
			act map[string]any
			t   float64
		}{{acts[0], 0}, {acts[len(acts)-2], 1}} {
			a, b := g.At(c.t)
			want := []screen.Point{a, b}[i]
			if x, y := c.act["x"].(float64), c.act["y"].(float64); math.Abs(x-(float64(want.X)+0.5)/3) > 0.01 || math.Abs(y-(float64(want.Y)+0.5)/3) > 0.01 {
				t.Errorf("finger %d at t=%v: %v,%v points, want %v pixels", i, c.t, x, y, want)
			}
		}
	}
}

// TestPinchRecorder records the mirror's two-finger touches and plays them
// back as one gesture with their timing.
func TestPinchRecorder(t *testing.T) {
	var r pinchRecorder
	t0 := time.Unix(100, 0)
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	pt := func(x, y int) screen.Point { return screen.Point{X: x, Y: y} }
	r.touch(driver.TouchDown, pt(500, 1000), pt(700, 1000), at(0))
	r.touch(driver.TouchMove, pt(499, 1000), pt(701, 1000), at(20)) // too close
	r.touch(driver.TouchMove, pt(450, 1000), pt(750, 1000), at(40))
	r.touch(driver.TouchMove, pt(400, 1000), pt(800, 1000), at(45)) // too soon
	a, b, steps, err := r.touch(driver.TouchUp, pt(300, 1000), pt(900, 1000), at(200))
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 3 || len(b) != 3 || a[1] != pt(450, 1000) || b[2] != pt(900, 1000) || steps[1] != 40*time.Millisecond || steps[2] != 160*time.Millisecond {
		t.Fatalf("paths %v %v steps %v", a, b, steps)
	}
	if a, _, _, _ := r.touch(driver.TouchUp, pt(0, 0), pt(0, 0), at(300)); a != nil {
		t.Fatal("up without down")
	}
	// A long pinch keeps both ends.
	r.touch(driver.TouchDown, pt(500, 1000), pt(600, 1000), at(1000))
	for i := 1; i <= 500; i++ {
		r.touch(driver.TouchMove, pt(500-i, 1000), pt(600+i, 1000), at(1000+i*20))
	}
	a, b, _, _ = r.touch(driver.TouchUp, pt(0, 1000), pt(1100, 1000), at(12000))
	if len(a) > recMaxPoints+1 || a[0] != pt(500, 1000) || b[len(b)-1] != pt(1100, 1000) {
		t.Fatalf("long pinch: %d points, %v .. %v", len(a), a[0], b[len(b)-1])
	}
}

// TestWDAMirrorPinch plays the person's pinch on the mirror back as two
// pointers, in screen points from the half-size picture.
func TestWDAMirrorPinch(t *testing.T) {
	f := &fakeWDA{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	d := &WDADevice{c: newWDAURL(srv.URL, ""), scale: 2, width: 1000, height: 2000}
	d.video = fixedSizeStream{w: 500, h: 1000}
	ctx := context.Background()
	d.Touch2(ctx, driver.TouchDown, screen.Point{X: 200, Y: 500}, screen.Point{X: 300, Y: 500})
	if len(f.bodies["POST /actions"]) != 0 {
		t.Fatal("played before the fingers lifted")
	}
	if err := d.Touch2(ctx, driver.TouchUp, screen.Point{X: 100, Y: 500}, screen.Point{X: 400, Y: 500}); err != nil {
		t.Fatal(err)
	}
	var body pointerActions
	json.Unmarshal([]byte(f.bodies["POST /actions"][0]), &body)
	if len(body.Actions) != 2 {
		t.Fatalf("%d pointers", len(body.Actions))
	}
	// Picture 200,500 is screen 400,1000: 200.25,500.25 points.
	if x := body.Actions[0].Actions[0]["x"]; x != 200.25 {
		t.Errorf("first finger down at x %v points", x)
	}
	if x := body.Actions[1].Actions[2]["x"]; x != 400.25 {
		t.Errorf("second finger lifts at x %v points", x)
	}
}

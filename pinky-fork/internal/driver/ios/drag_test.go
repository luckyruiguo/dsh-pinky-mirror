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

// TestWDADrag checks a drag's W3C actions: one touch pointer down at the
// press point, a pause for the hold and each rest, moves that take the
// drag's time, and up at the drop point, in WDA's points.
func TestWDADrag(t *testing.T) {
	f := &fakeWDA{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	d := &WDADevice{c: newWDAURL(srv.URL, ""), scale: 3, width: 1179, height: 2556}
	g := driver.Drag{
		From: screen.Point{X: 300, Y: 1200},
		Hold: time.Second,
		Stops: []driver.DragStop{
			{Point: screen.Point{X: 1150, Y: 1200}, Move: 400 * time.Millisecond, Pause: 1200 * time.Millisecond},
			{Point: screen.Point{X: 600, Y: 900}, Move: 300 * time.Millisecond},
		},
	}
	if err := d.Drag(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	var body pointerActions
	if err := json.Unmarshal([]byte(f.bodies["POST /actions"][0]), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Actions) != 1 {
		t.Fatalf("%d pointers", len(body.Actions))
	}
	p := body.Actions[0]
	acts := p.Actions
	if p.Type != "pointer" || p.Parameters.PointerType != "touch" {
		t.Fatalf("pointer %s %s", p.Type, p.Parameters.PointerType)
	}
	if acts[0]["type"] != "pointerMove" || acts[1]["type"] != "pointerDown" || acts[len(acts)-1]["type"] != "pointerUp" {
		t.Fatalf("actions %v", acts)
	}
	inPoints := func(a map[string]any, want screen.Point) bool {
		x, y := a["x"].(float64), a["y"].(float64)
		return math.Abs(x-(float64(want.X)+0.5)/3) < 0.01 && math.Abs(y-(float64(want.Y)+0.5)/3) < 0.01
	}
	if !inPoints(acts[0], g.From) {
		t.Errorf("down at %v,%v", acts[0]["x"], acts[0]["y"])
	}
	if !inPoints(acts[len(acts)-2], g.To()) {
		t.Errorf("up after %v", acts[len(acts)-2])
	}
	var pauses []float64
	var total float64
	for _, a := range acts[2 : len(acts)-1] {
		total += a["duration"].(float64)
		if a["type"] == "pause" {
			pauses = append(pauses, a["duration"].(float64))
		}
	}
	if len(pauses) != 2 || pauses[0] != 1000 || pauses[1] != 1200 {
		t.Errorf("pauses %v, want the hold and the rest", pauses)
	}
	if math.Abs(total-float64(g.Duration().Milliseconds())) > 20 {
		t.Errorf("the finger is down for %v ms, want %v", total, g.Duration().Milliseconds())
	}
}

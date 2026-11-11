package gateway

import (
	"context"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// keyboardDev is a demo phone that shows the same screen on every read:
// one with the keyboard up.
type keyboardDev struct {
	*demo.Device
	screen func() *screen.Screen
}

func (d *keyboardDev) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	return d.screen(), nil
}

func keyboardGateway(t *testing.T, show func() *screen.Screen) (*Session, *Result, *demo.Device) {
	t.Helper()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	g := newDevGateway(t, base, &keyboardDev{Device: base, screen: show})
	s, res := connect(t, g, "Search settings")
	return s, res, base
}

// refOf finds the first element with the given role in a result's screen.
func refOf(t *testing.T, res *Result, role string) int {
	t.Helper()
	for _, e := range res.Screen.Elements {
		if e.Role == role && e.Ref != 0 {
			return e.Ref
		}
	}
	t.Fatalf("no %s on screen:\n%s", role, res.Screen.Render())
	return 0
}

// lastInput returns the last input of kind the phone got.
func lastInput(t *testing.T, d *demo.Device, kind string) demo.Input {
	t.Helper()
	ins := d.Inputs()
	for i := len(ins) - 1; i >= 0; i-- {
		if ins[i].Kind == kind {
			return ins[i]
		}
	}
	t.Fatalf("no %s among %+v", kind, ins)
	return demo.Input{}
}

func noInput(t *testing.T, d *demo.Device, kind string) {
	t.Helper()
	for _, in := range d.Inputs() {
		if in.Kind == kind {
			t.Fatalf("the %s ran: %+v", kind, in)
		}
	}
}

// The recorded search page of Settings with the keyboard up (GitHub issue
// #71): the results list spans the screen below the search field, its
// middle lies just above the keyboard, and a swipe up across it started on
// the keyboard and typed a character. The swipe now keeps to the part of
// the list above the keyboard, and so does one across the whole screen.
func TestSwipeKeepsClearOfTheKeyboard(t *testing.T) {
	ctx := context.Background()
	rec := recordedScreen(t, "search-keyboard")
	kb := rec.KeyboardFrame
	if !rec.Keyboard || kb != (screen.Rect{X: 0, Y: 1517, W: 1080, H: 883}) {
		t.Fatalf("keyboard %v at %+v", rec.Keyboard, kb)
	}
	if len(rec.Covered) != 1 || rec.Covered[0] != (screen.Rect{W: 1080, H: 128}) {
		t.Fatalf("covered %+v, want the status bar", rec.Covered)
	}
	s, res, d := keyboardGateway(t, func() *screen.Screen { return recordedScreen(t, "search-keyboard") })
	list := refOf(t, res, screen.RoleList)
	bounds := func() screen.Rect {
		e, _ := res.Screen.Find(list)
		return e.Bounds
	}()
	if !bounds.Contains(screen.Point{X: 540, Y: 2000}) {
		t.Fatalf("the list %+v does not reach under the keyboard: the recording no longer shows the bug", bounds)
	}
	for _, dir := range []string{"up", "down", "left", "right"} {
		for _, r := range []int{list, 0} {
			mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "swipe", Ref: r, Direction: dir}), StatusSettled)
			in := lastInput(t, d, "swipe")
			for _, p := range []screen.Point{in.P, in.To} {
				if kb.Contains(p) || p.Y < 128 {
					t.Errorf("swipe %s on [%d] from %v to %v touches the keyboard or the status bar", dir, r, in.P, in.To)
				}
				if r != 0 && !bounds.Contains(p) {
					t.Errorf("swipe %s on [%d] from %v to %v leaves the list %+v", dir, r, in.P, in.To, bounds)
				}
			}
		}
	}
}

// keyboardOver is a screen of 1080x2400 with the keyboard over its lower
// part from y 1500, and the elements given.
func keyboardOver(els ...screen.Element) func() *screen.Screen {
	return func() *screen.Screen {
		s := &screen.Screen{
			Width: 1080, Height: 2400, App: screen.App{Name: "Search", ID: "com.example.search"}, Orientation: "portrait",
			Keyboard: true, KeyboardFrame: screen.Rect{Y: 1500, W: 1080, H: 900},
		}
		s.Elements = append(s.Elements, screen.Element{Role: screen.RoleInput, Label: "Search", Bounds: screen.Rect{X: 40, Y: 150, W: 1000, H: 120},
			States: []string{screen.StateEditable, screen.StateFocused}, Source: screen.SourceTree, Path: "app"})
		for _, e := range els {
			e.Source, e.Path = screen.SourceTree, "app"
			s.Elements = append(s.Elements, e)
		}
		return s
	}
}

// When too little of the element shows above the keyboard to swipe in, the
// swipe is refused with what to do, rather than run on the keyboard.
func TestSwipeRefusedUnderTheKeyboard(t *testing.T) {
	ctx := context.Background()
	s, res, d := keyboardGateway(t, keyboardOver(
		screen.Element{Role: screen.RoleList, Label: "Results", Bounds: screen.Rect{Y: 1350, W: 1080, H: 1050}, States: []string{screen.StateScrollable}},
		screen.Element{Role: screen.RoleList, Label: "Suggestions", Bounds: screen.Rect{Y: 1350, W: 1080, H: 150}, States: []string{screen.StateScrollable}},
	))
	results := ref(t, res, "Results")
	res = s.Act(ctx, Call{}, Action{Tool: "swipe", Ref: results, Direction: "up"})
	if res.Error != ErrInvalidArgument || !strings.Contains(res.Message, "keyboard") || !strings.Contains(res.Message, "phone_key back") {
		t.Fatalf("got %q %q", res.Error, res.Message)
	}
	// Sideways there is room: the part above the keyboard is wide enough.
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "swipe", Ref: results, Direction: "left"}), StatusSettled)
	in := lastInput(t, d, "swipe")
	if in.P.Y >= 1500 || in.P.Y < 1350 {
		t.Fatalf("sideways swipe at y %d, want between the list's top and the keyboard", in.P.Y)
	}
	// An explicit from and to is the agent's to choose.
	from, to := screen.Point{X: 540, Y: 2000}, screen.Point{X: 540, Y: 1000}
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "swipe", From: &from, To: &to}), StatusSettled)
}

// A tap, a drag or a pinch by ref on an element whose middle lies under the
// keyboard lands on its part above it, and is refused when none of it
// shows.
func TestTouchByRefKeepsClearOfTheKeyboard(t *testing.T) {
	ctx := context.Background()
	s, res, d := keyboardGateway(t, keyboardOver(
		screen.Element{Role: screen.RoleButton, Label: "Display", Bounds: screen.Rect{X: 40, Y: 1300, W: 1000, H: 400}},
		screen.Element{Role: screen.RoleButton, Label: "Hidden", Bounds: screen.Rect{X: 40, Y: 1800, W: 1000, H: 150}},
		screen.Element{Role: screen.RoleImage, Label: "Map", Bounds: screen.Rect{X: 0, Y: 300, W: 1080, H: 2100}},
	))
	kb := screen.Rect{Y: 1500, W: 1080, H: 900}
	display, hidden, mp := ref(t, res, "Display"), ref(t, res, "Hidden"), ref(t, res, "Map")

	mustStatus(t, tap(s, display), StatusSettled)
	if p := lastInput(t, d, "tap").P; kb.Contains(p) || p.Y < 1300 {
		t.Fatalf("tap at %v, want on Display above the keyboard", p)
	}
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "drag", Ref: display, To: &screen.Point{X: 540, Y: 600}}), StatusSettled)
	if p := lastInput(t, d, "drag").P; kb.Contains(p) || p.Y < 1300 {
		t.Fatalf("drag from %v, want from Display above the keyboard", p)
	}
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "pinch", Ref: mp, Scale: 0.5}), StatusSettled)
	in := lastInput(t, d, "pinch")
	for _, p := range []screen.Point{in.P, in.To} {
		if kb.Contains(p) {
			t.Fatalf("pinch fingers at %v and %v, one on the keyboard", in.P, in.To)
		}
	}
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "pinch", Scale: 2}), StatusSettled)
	in = lastInput(t, d, "pinch")
	for _, p := range []screen.Point{in.P, in.To} {
		if kb.Contains(p) {
			t.Fatalf("pinch on the screen with fingers at %v and %v, one on the keyboard", in.P, in.To)
		}
	}

	n := len(d.Inputs())
	for _, a := range []Action{
		{Tool: "tap", Ref: hidden},
		{Tool: "drag", Ref: hidden, To: &screen.Point{X: 540, Y: 600}},
		{Tool: "pinch", Ref: hidden, Scale: 2},
		{Tool: "swipe", Ref: hidden, Direction: "up"},
	} {
		res := s.Act(ctx, Call{}, a)
		if res.Error != ErrInvalidArgument || !strings.Contains(res.Message, "under the keyboard") {
			t.Errorf("%s on an element under the keyboard: %q %q", a.Tool, res.Error, res.Message)
		}
	}
	if got := d.Inputs(); len(got) != n {
		t.Fatalf("inputs ran: %+v", got[n:])
	}
}

// What lies on a cover belongs to it: a keyboard's full-screen panel and
// the elements on a system bar take touches as before, and a screen
// without a keyboard frame swipes as it always did.
func TestCoversThatDoNotCount(t *testing.T) {
	sheet := &screen.Screen{Width: 1080, Height: 2400, Keyboard: true, KeyboardFrame: screen.Rect{Y: 128, W: 1080, H: 2272}}
	if got, res := swipeArea(sheet, nil, "up"); res != nil || got != (screen.Rect{W: 1080, H: 2400}) {
		t.Fatalf("on a keyboard sheet: %+v, %+v", got, res)
	}
	bar := &screen.Screen{Width: 1080, Height: 2400, Covered: []screen.Rect{{W: 1080, H: 128}}}
	clock := screen.Element{Ref: 3, Role: screen.RoleButton, Label: "Clock", Bounds: screen.Rect{X: 20, Y: 20, W: 100, H: 80}}
	if got, res := touchable(bar, &clock); res != nil || got.Bounds != clock.Bounds {
		t.Fatalf("a status bar's own button: %+v, %+v", got.Bounds, res)
	}
	hidden := &screen.Screen{Width: 1080, Height: 2400, KeyboardFrame: screen.Rect{Y: 1500, W: 1080, H: 900}}
	if got, res := swipeArea(hidden, nil, "up"); res != nil || got != (screen.Rect{W: 1080, H: 2400}) {
		t.Fatalf("with the keyboard hidden: %+v, %+v", got, res)
	}
	unknown := &screen.Screen{Width: 1080, Height: 2400, Keyboard: true}
	if got, res := swipeArea(unknown, nil, "up"); res != nil || got != (screen.Rect{W: 1080, H: 2400}) {
		t.Fatalf("with the keyboard's frame unknown: %+v, %+v", got, res)
	}
}

// A pinch that fails because its element is too narrow or at the screen's
// edge says so, even while the keyboard shows: hiding it would not help.
// A keyboard whose own keys are listed has no frame, and they take taps.
func TestPinchAtTheEdgeIsNotTheKeyboards(t *testing.T) {
	ctx := context.Background()
	s, res, d := keyboardGateway(t, keyboardOver(
		screen.Element{Role: screen.RoleImage, Label: "Edge", Bounds: screen.Rect{X: 0, Y: 1400, W: 60, H: 400}},
		screen.Element{Role: screen.RoleImage, Label: "Corner", Bounds: screen.Rect{X: 0, Y: 300, W: 60, H: 60}},
	))
	for _, name := range []string{"Edge", "Corner"} {
		res := s.Act(ctx, Call{}, Action{Tool: "pinch", Ref: ref(t, res, name), Scale: 2})
		if res.Error != ErrInvalidArgument || strings.Contains(res.Message, "keyboard") || !strings.Contains(res.Message, "edges") {
			t.Errorf("pinch on %s: %q %q", name, res.Error, res.Message)
		}
	}
	noInput(t, d, "pinch")

	panel := func() *screen.Screen {
		s := keyboardOver(screen.Element{Role: screen.RoleButton, Label: "Next", Bounds: screen.Rect{X: 600, Y: 2200, W: 400, H: 100}, Path: "input_method:"})()
		s.KeyboardFrame = screen.Rect{}
		return s
	}
	s, res, d = keyboardGateway(t, panel)
	mustStatus(t, tap(s, ref(t, res, "Next")), StatusSettled)
	if p := lastInput(t, d, "tap").P; p != (screen.Point{X: 800, Y: 2250}) {
		t.Fatalf("tap on the keyboard's own key at %v", p)
	}
}

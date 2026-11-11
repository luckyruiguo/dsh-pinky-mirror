package ios

import (
	"reflect"
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

// Elements as iOS 27.0.1 gives them in Clock (iPhone 13 mini, 2026-11-08).
func TestRAXRoleIOS27(t *testing.T) {
	for _, c := range []struct {
		name   string
		e      raxElement
		role   string
		states []string
		label  string
	}{
		{"a tab of the floating tab bar",
			raxElement{Type: xcButton, AXRole: axpRoleTab, Traits: 0x210000009, Label: "Alarms", Value: "1", Enabled: true},
			screen.RoleTab, []string{screen.StateSelected}, "Alarms"},
		{"an unselected tab, first push",
			raxElement{AXRole: axpRoleTab, Traits: 0x210000001, Label: "Timers", Value: "0", Enabled: true},
			screen.RoleTab, nil, "Timers"},
		{"a picker wheel reads its row, not its index",
			raxElement{Type: xcPickerWheel, AXRole: axpRoleAdjustable, Traits: 0x200081000, Value: "5003", ValueText: "12 o’clock", Enabled: true},
			screen.RoleOther, []string{stateAdjustable}, "12 o’clock"},
		{"the same wheel in a first push",
			raxElement{AXRole: axpRoleAdjustable, Traits: 0x200081000, Value: "5006", ValueText: "26 minutes", Enabled: true},
			screen.RoleOther, []string{stateAdjustable}, "26 minutes"},
		{"static text in a first push",
			raxElement{AXRole: axpRoleText, Traits: 0x2200000040, Label: "Label", Enabled: true},
			screen.RoleText, nil, "Label"},
		{"a cell with a switch",
			raxElement{AXRole: axpRoleSwitch, Traits: 0x20002200000000, Label: "9:00PM, Alarm", Value: "0", ValueText: "0", Enabled: true},
			screen.RoleSwitch, nil, "9:00PM, Alarm"},
		{"a text field",
			raxElement{Type: xcTextField, AXRole: axpRoleTextField, Value: "Alarm", ValueText: "Alarm", Placeholder: "Alarm", Enabled: true},
			screen.RoleInput, []string{screen.StateEditable}, "Alarm"},
	} {
		role, states := raxRole(c.e, false)
		if role != c.role || !reflect.DeepEqual(states, c.states) {
			t.Errorf("%s: %s %v, want %s %v", c.name, role, states, c.role, c.states)
		}
		if l := raxLabel(c.e, role); l != c.label {
			t.Errorf("%s: label %q, want %q", c.name, l, c.label)
		}
	}
}

// A first push lacks types and identifiers; what walks told of the same
// elements fills them in, so a read from a push and a read from a walk give
// the same refs.
func TestRAXFill(t *testing.T) {
	s := &raxSession{}
	walked := []raxElement{
		{Object: &axpObject{PID: 7, ID: 1, IsApp: true}, Type: 1},
		{Object: &axpObject{PID: 7, ID: 2}, Type: xcStaticText, Ident: "recent-timer-0", Label: "2 min"},
		{Object: &axpObject{PID: 7, ID: 3}, Type: 0, Label: "Recents"},
	}
	s.learn(walked)
	push := []raxElement{
		{Object: &axpObject{PID: 7, ID: 1, IsApp: true}},
		{Object: &axpObject{PID: 7, ID: 2}, Label: "2 min"},
		{Object: &axpObject{PID: 7, ID: 3}, Label: "Recents"},
	}
	got, known := s.fill(push)
	if !known || got[1].Type != xcStaticText || got[1].Ident != "recent-timer-0" || got[0].Type != 1 {
		t.Fatalf("known %v, filled %+v", known, got)
	}
	if push[1].Type != 0 || push[1].Ident != "" {
		t.Error("fill changed the pushed tree itself")
	}
	toScreen := func(els []raxElement) []screen.Element {
		for i := range els {
			els[i].IsElement, els[i].Visible, els[i].Enabled, els[i].HasFrame = i > 0, true, true, true
			els[i].Frame = axpRect{0, float64(100 * i), 375, 50}
			if i > 0 {
				els[i].Parent = 0
			} else {
				els[i].Parent = -1
			}
		}
		out, _, _ := raxScreenElements(els, 3, 1125, 2436, "com.apple.mobiletimer")
		return out
	}
	a, b := toScreen(got), toScreen(append([]raxElement(nil), walked...))
	if !reflect.DeepEqual(a, b) {
		t.Errorf("a filled push and a walk differ:\n%+v\n%+v", a, b)
	}
	if _, known := s.fill(append(push, raxElement{Object: &axpObject{PID: 7, ID: 9}})); known {
		t.Error("an element no walk saw counted as known")
	}
}

// Rows behind iOS 27's floating tab bar (Clock › Alarms): a row whose middle
// is under the bar keeps only the part above it, or goes.
func TestRAXScreenElementsTabBar(t *testing.T) {
	el := func(id uint64, typ, axRole int, label string, f axpRect) raxElement {
		return raxElement{Object: &axpObject{PID: 5, ID: id}, Parent: 0, Type: typ, AXRole: axRole, Label: label,
			Frame: f, HasFrame: true, IsElement: true, Enabled: true, Visible: true}
	}
	els := []raxElement{
		{Object: &axpObject{PID: 5, ID: 100, IsApp: true}, Parent: -1, Label: "Clock", Frame: axpRect{0, 0, 375, 812}, HasFrame: true, Type: 1},
		el(1, xcStaticText, axpRoleText, "9:00PM, Alarm", axpRect{0, 508, 375, 102}),
		el(2, xcStaticText, axpRoleText, "6:30AM, Alarm", axpRect{0, 690, 375, 102}), // middle under the bar
		el(3, xcStaticText, axpRoleText, "7:00AM, Alarm", axpRect{0, 760, 375, 52}),  // nothing above the bar left
		el(4, xcButton, axpRoleTab, "World Clock", axpRect{25, 733, 109, 54}),
		el(5, xcButton, axpRoleTab, "Alarms", axpRect{114, 733, 87, 54}),
		el(6, xcButton, axpRoleTab, "Stopwatch", axpRect{182, 733, 101, 54}),
		el(7, xcButton, axpRoleTab, "Timers", axpRect{263, 733, 87, 54}),
	}
	got, _, _ := raxScreenElements(els, 3, 1125, 2436, "com.apple.mobiletimer")
	byLabel := map[string]screen.Element{}
	for _, e := range got {
		byLabel[e.Label] = e
	}
	if e := byLabel["9:00PM, Alarm"]; e.Bounds != (screen.Rect{X: 0, Y: 1524, W: 1125, H: 306}) {
		t.Errorf("an uncovered row changed: %v", e.Bounds)
	}
	// The bar with its margin: 727 to 793 points, 2181 to 2379 pixels.
	if e, ok := byLabel["6:30AM, Alarm"]; !ok || e.Bounds != (screen.Rect{X: 0, Y: 2070, W: 1125, H: 111}) {
		t.Errorf("a half covered row: %v (%v); want the part above the bar", e.Bounds, ok)
	}
	if e, ok := byLabel["7:00AM, Alarm"]; ok {
		t.Errorf("a row behind the bar is listed at %v", e.Bounds)
	}
	for _, l := range []string{"World Clock", "Alarms", "Stopwatch", "Timers"} {
		if e, ok := byLabel[l]; !ok || e.Role != screen.RoleTab {
			t.Errorf("tab %q: %+v", l, e)
		}
	}
}

func TestRAXScreenElementsKeyboardCovers(t *testing.T) {
	const app, kb = 10, 20
	el := func(pid int, id uint64, typ int, label string, f axpRect) raxElement {
		return raxElement{Object: &axpObject{PID: pid, ID: id}, Parent: 0, Type: typ, Label: label,
			Frame: f, HasFrame: true, IsElement: true, Enabled: true, Visible: true}
	}
	els := []raxElement{
		{Object: &axpObject{PID: app, ID: 1, IsApp: true}, Parent: -1, Label: "Settings", Frame: axpRect{0, 0, 375, 812}, HasFrame: true, Type: 1},
		el(app, 2, xcCell, "General", axpRect{16, 200, 343, 52}),
		el(app, 3, xcCell, "Privacy", axpRect{16, 600, 343, 52}), // behind the keyboard
	}
	host := el(app, 4, 0, "", axpRect{0, 461, 375, 351})
	host.Traits = traitRemoteHost
	els = append(els, host)
	for i, l := range "QWERTYUIOPASDFGHJKL" {
		x := float64(i%10) * 37.5
		y := 10 + float64(i/10)*54
		els = append(els, el(kb, uint64(100+i), xcStaticText, string(l), axpRect{x, y, 37.5, 54}))
	}
	els = append(els, el(kb, 200, xcStaticText, "space", axpRect{0, 280, 375, 54}))
	got, keyboard, _ := raxScreenElements(els, 3, 1125, 2436, "com.apple.Preferences")
	if !keyboard || len(got) != 1 || got[0].Label != "General" {
		t.Errorf("keyboard %v, elements %+v; want only the row above the keyboard", keyboard, got)
	}
}

func TestLargestOutside(t *testing.T) {
	b := screen.Rect{X: 0, Y: 0, W: 100, H: 100}
	for _, c := range []struct {
		o, want screen.Rect
	}{
		{screen.Rect{X: 0, Y: 70, W: 100, H: 50}, screen.Rect{X: 0, Y: 0, W: 100, H: 70}},
		{screen.Rect{X: 0, Y: -10, W: 100, H: 30}, screen.Rect{X: 0, Y: 20, W: 100, H: 80}},
		{screen.Rect{X: 60, Y: -10, W: 60, H: 200}, screen.Rect{X: 0, Y: 0, W: 60, H: 100}},
		{screen.Rect{X: -10, Y: -10, W: 200, H: 200}, screen.Rect{}},
	} {
		if got := largestOutside(b, c.o); got != c.want {
			t.Errorf("largestOutside(%v) = %v, want %v", c.o, got, c.want)
		}
	}
}

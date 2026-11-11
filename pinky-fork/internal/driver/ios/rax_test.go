package ios

import (
	"reflect"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/screen"
	"howett.net/plist"
)

// archive builds a keyed archive whose root is built by root, with the
// writer the request encoder uses.
func archive(t *testing.T, root func(w *keyedWriter) plist.UID) []byte {
	t.Helper()
	w := &keyedWriter{objects: []any{"$null"}, classes: map[string]plist.UID{}}
	r := root(w)
	b, err := plist.Marshal(map[string]any{
		"$archiver": "NSKeyedArchiver",
		"$version":  uint64(100000),
		"$top":      map[string]any{"root": r},
		"$objects":  w.objects,
	}, plist.BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestArchiveAXPRequest(t *testing.T) {
	obj := &axpObject{PID: 37, ID: 16809516602719719110, IsApp: true} // above MaxInt64, as real IDs are
	b, err := archiveAXPRequest(axpRequest{Type: axpReqAttributes, Client: raxClientType, Object: obj,
		Params: map[string]any{"attributes": []int{axpAttrFrame, axpAttrLabel}}})
	if err != nil {
		t.Fatal(err)
	}
	v, err := unarchiveAXP(b)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := v.(axpDict)
	if !ok || d["$class"] != "AXPTranslatorRequest" {
		t.Fatalf("decoded %#v", v)
	}
	if d["requestType"] != uint64(axpReqAttributes) || d["clientType"] != uint64(raxClientType) || d["attributeType"] != uint64(0) {
		t.Errorf("fields %#v", d)
	}
	if got, ok := d["translation"].(*axpObject); !ok || *got != *obj {
		t.Errorf("translation %#v, want %#v", d["translation"], obj)
	}
	params, _ := d["parameters"].(axpDict)
	if attrs, _ := params["attributes"].([]any); len(attrs) != 2 || toInt(attrs[0]) != axpAttrFrame || toInt(attrs[1]) != axpAttrLabel {
		t.Errorf("parameters %#v", params)
	}
}

func TestUnarchiveAXPResponse(t *testing.T) {
	b := archive(t, func(w *keyedWriter) plist.UID {
		app := w.add(map[string]any{
			"$class": w.class("AXPTranslationObject"), "pid": uint64(11121), "objectID": w.add(uint64(23247)),
			"isApplicationElement": true, "bridgeDelegateToken": plist.UID(0), "rawElementData": plist.UID(0),
		})
		rect := w.add(map[string]any{"$class": w.class("NSValue"), "NS.special": uint64(3), "NS.rectval": w.add("{{16, 104.5}, {343, 90}}")})
		point := w.add(map[string]any{"$class": w.class("NSValue"), "NS.special": uint64(1), "NS.pointval": w.add("{187.5, 149}")})
		label := w.add(map[string]any{"$class": w.class("NSMutableString"), "NS.string": "Family"})
		attrs := w.add(map[string]any{
			"$class":     w.class("NSDictionary"),
			"NS.keys":    []any{w.add(int64(axpAttrFrame)), w.add(int64(axpAttrLabel)), w.add(int64(axpAttrActivationPoint)), w.add(int64(axpAttrIsElement))},
			"NS.objects": []any{rect, label, point, w.add(true)},
		})
		el := w.add(map[string]any{
			"$class": w.class("AXPTranslatorResponse"), "associatedRequestType": uint64(axpReqAttributes),
			"associatedTranslationObject": app, "associatedNotificationObject": plist.UID(0),
			"attribute": uint64(0), "error": uint64(0), "notification": uint64(0), "resultData": attrs,
		})
		list := w.add(map[string]any{"$class": w.class("NSArray"), "NS.objects": []any{el}})
		result := w.add(map[string]any{
			"$class":     w.class("NSDictionary"),
			"NS.keys":    []any{w.add("treeDump"), w.add("treeDumpType")},
			"NS.objects": []any{list, w.add("AXPTreeDumpTypeInitialDump")},
		})
		return w.add(map[string]any{
			"$class": w.class("AXPTranslatorResponse"), "associatedRequestType": uint64(axpReqTreeDump),
			"associatedTranslationObject": plist.UID(0), "associatedNotificationObject": plist.UID(0),
			"attribute": uint64(0), "error": uint64(0), "notification": uint64(0), "resultData": result,
		})
	})
	v, err := unarchiveAXP(b)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := v.(*axpResponse)
	if !ok || r.RequestType != axpReqTreeDump {
		t.Fatalf("decoded %#v", v)
	}
	d := r.Result.(axpDict)
	if d["treeDumpType"] != "AXPTreeDumpTypeInitialDump" {
		t.Errorf("type %v", d["treeDumpType"])
	}
	el := d["treeDump"].([]any)[0].(*axpResponse)
	if el.Object == nil || *el.Object != (axpObject{PID: 11121, ID: 23247, IsApp: true}) {
		t.Errorf("object %#v", el.Object)
	}
	ad := el.Result.(axpDict)
	if ad[axpAttrFrame] != (axpRect{16, 104.5, 343, 90}) || ad[axpAttrLabel] != "Family" ||
		ad[axpAttrActivationPoint] != (axpPoint{187.5, 149}) || ad[axpAttrIsElement] != true {
		t.Errorf("attributes %#v", ad)
	}
}

func TestUnarchiveAXPRejects(t *testing.T) {
	for _, b := range [][]byte{nil, []byte("not a plist"), archive(t, func(w *keyedWriter) plist.UID { return plist.UID(99) })} {
		if _, err := unarchiveAXP(b); err == nil {
			t.Errorf("%q: no error", b)
		}
	}
	// A self-referencing array must not recurse forever.
	b := archive(t, func(w *keyedWriter) plist.UID {
		cls := w.class("NSArray")
		u := plist.UID(len(w.objects))
		w.add(map[string]any{"$class": cls, "NS.objects": []any{u}})
		return u
	})
	if _, err := unarchiveAXP(b); err == nil {
		t.Error("a cycle decoded without error")
	}
}

func TestTreeFromDump(t *testing.T) {
	app := &axpObject{PID: 1, ID: 100, IsApp: true}
	a, b, c := &axpObject{PID: 1, ID: 1}, &axpObject{PID: 1, ID: 2}, &axpObject{PID: 1, ID: 3}
	list := []any{
		&axpResponse{RequestType: axpReqAttribute, Attribute: 129, Result: uint64(1)},
		&axpResponse{RequestType: axpReqFrontmostApp, Result: app},
		&axpResponse{RequestType: axpReqAttributes, Object: app, Result: axpDict{axpAttrNavigationOrder: []any{a, b}, axpAttrLabel: "App"}},
		&axpResponse{RequestType: axpReqAttributes, Object: b, Result: axpDict{axpAttrLabel: "B"}},
		&axpResponse{RequestType: axpReqAttributes, Object: a, Result: axpDict{axpAttrChildren: []any{c}, axpAttrLabel: "A"}},
		&axpResponse{RequestType: axpReqAttributes, Object: c, Result: axpDict{axpAttrLabel: "C"}},
	}
	els := treeFromDump(list)
	var got []string
	for _, e := range els {
		got = append(got, e.Label)
	}
	if !reflect.DeepEqual(got, []string{"App", "A", "C", "B"}) {
		t.Fatalf("order %v", got)
	}
	if els[1].Parent != 0 || els[2].Parent != 1 || els[3].Parent != 0 || els[2].Depth != 2 {
		t.Errorf("parents %+v", els)
	}
	if treeFromDump(list[2:]) != nil {
		t.Error("a dump without the app element gave a tree")
	}
}

func TestRAXTreeUnnamed(t *testing.T) {
	named := &raxTree{els: []raxElement{{}, {IsElement: true, HasFrame: true, Label: "7"}}}
	unnamed := &raxTree{els: []raxElement{{}, {IsElement: true, HasFrame: true}}}
	if named.unnamed() || !unnamed.unnamed() {
		t.Errorf("unnamed: %v %v", named.unnamed(), unnamed.unnamed())
	}
}

func TestRAXScreenElements(t *testing.T) {
	const app, kb, widget, web = 10, 20, 30, 40
	el := func(pid int, parent int, typ int, label string, f axpRect) raxElement {
		return raxElement{Object: &axpObject{PID: pid, ID: uint64(len(label) + int(f.X) + int(f.Y))}, Parent: parent, Type: typ,
			Label: label, Frame: f, HasFrame: true, IsElement: true, Enabled: true, Visible: true}
	}
	els := []raxElement{
		{Object: &axpObject{PID: app, IsApp: true}, Parent: -1, Label: "Settings", Frame: axpRect{0, 0, 375, 812}, HasFrame: true, Type: xcOther},
		el(app, 0, xcButton, "Wi-Fi", axpRect{16, 528, 343, 52}),
		el(app, 0, xcSwitch, "Airplane Mode", axpRect{16, 476, 343, 52}),
		el(app, 0, xcSearchField, "", axpRect{28, 746, 319, 28}),
		el(app, 0, xcStaticText, "Off screen", axpRect{16, 900, 343, 40}),
		{Object: &axpObject{PID: app}, Parent: 0, Type: xcTable, Frame: axpRect{0, 100, 375, 600}, HasFrame: true, Visible: true},
		el(app, 5, xcCell, "General", axpRect{16, 200, 343, 52}),
		el(widget, 0, xcImage, "Featured Photo", axpRect{0, 0, 159, 159}),
		el(app, 0, 0, "", axpRect{0, 461, 375, 351}), // the keyboard's host
		el(kb, 0, xcStaticText, "Q", axpRect{3, 68, 31.5, 42}),
	}
	els[2].Value = "1"
	els[3].Value = "Search"
	els[3].Focused = true
	els[8].Traits = traitRemoteHost
	for _, l := range "WERTYUIOP" {
		els = append(els, el(kb, 0, xcStaticText, string(l), axpRect{40, 68, 31.5, 42}))
	}
	// Another process behind a host that is not a keyboard: placed by it.
	host := el(app, 0, 0, "", axpRect{0, 100, 375, 400})
	host.Traits = traitRemoteHost
	els = append(els, host, el(web, 0, xcLink, "Read more", axpRect{10, 20, 100, 30}))

	got, keyboard, kbFrame := raxScreenElements(els, 3, 1125, 2436, "com.apple.Preferences")
	// The keyboard's frame is the box of its keys, placed by their host.
	if want := (screen.Rect{X: 9, Y: 1587, W: 206, H: 126}); kbFrame != want {
		t.Errorf("keyboard frame %+v, want %+v", kbFrame, want)
	}
	if !keyboard {
		t.Error("keyboard not detected")
	}
	want := []screen.Element{
		{Role: screen.RoleButton, Label: "Wi-Fi", Bounds: screen.Rect{X: 48, Y: 1584, W: 1029, H: 156}},
		{Role: screen.RoleSwitch, Label: "Airplane Mode", States: []string{screen.StateChecked}, Bounds: screen.Rect{X: 48, Y: 1428, W: 1029, H: 156}},
		{Role: screen.RoleInput, Label: "Search", States: []string{screen.StateEditable, screen.StateFocused}, Bounds: screen.Rect{X: 84, Y: 2238, W: 957, H: 84}},
		{Role: screen.RoleButton, Label: "General", Bounds: screen.Rect{X: 48, Y: 600, W: 1029, H: 156}},
		{Role: screen.RoleLink, Label: "Read more", Bounds: screen.Rect{X: 30, Y: 360, W: 300, H: 90}},
	}
	if len(got) != len(want) {
		for _, e := range got {
			t.Log(e.Line(), e.Bounds)
		}
		t.Fatalf("%d elements, want %d", len(got), len(want))
	}
	for i := range want {
		g := got[i]
		if g.Role != want[i].Role || g.Label != want[i].Label || g.Bounds != want[i].Bounds || !reflect.DeepEqual(g.States, want[i].States) || g.Source != screen.SourceTree {
			t.Errorf("element %d = %+v, want %+v", i, g, want[i])
		}
	}
	if got[3].Path != "com.apple.Preferences/t26" {
		t.Errorf("path %q", got[3].Path)
	}
}

// Safari lists a page's elements from the web content process, beside its
// own, with no host view and with frames in screen points. A widget's
// process looks the same but its frames are relative to the widget.
func TestRAXScreenElementsWebContent(t *testing.T) {
	const app, web, widget = 10, 20, 30
	el := func(pid, typ, role int, label string, f axpRect) raxElement {
		return raxElement{Object: &axpObject{PID: pid, ID: uint64(len(label) + int(f.X) + int(f.Y))}, Parent: 0, Type: typ, AXRole: role,
			Label: label, Frame: f, HasFrame: true, IsElement: true, Enabled: true, Visible: true, Web: pid == web}
	}
	user := el(web, 0, axpRoleTextField, "Username", axpRect{16, 244, 370, 37})
	pass := el(web, 0, axpRoleTextField, "Foo", axpRect{16, 326, 370, 37})
	pass.Traits = traitSecureText
	hidden := el(web, xcLink, axpRoleButton, "Below the fold", axpRect{16, 855, 157, 25})
	hidden.Visible = false
	els := []raxElement{
		{Object: &axpObject{PID: app, IsApp: true}, Parent: -1, Label: "Safari", Frame: axpRect{0, 0, 402, 874}, HasFrame: true, Type: xcOther},
		el(web, xcLink, axpRoleButton, "Forgot your password?", axpRect{109, 525, 184, 24}),
		user, pass, hidden,
		el(app, xcButton, axpRoleButton, "Tabs", axpRect{320, 792, 48, 48}),
		el(widget, xcImage, axpRoleImage, "Featured Photo", axpRect{0, 0, 159, 159}),
	}
	// A page's pagination, eight one-digit links, is not a keyboard.
	for i := 1; i <= 8; i++ {
		els = append(els, el(web, xcLink, axpRoleButton, string(rune('0'+i)), axpRect{float64(10 * i), 600, 8, 20}))
	}
	got, keyboard, _ := raxScreenElements(els, 3, 1206, 2622, "com.apple.mobilesafari")
	if keyboard {
		t.Error("a page's one-digit links took for a keyboard")
	}
	type row struct {
		role, label string
		states      []string
		bounds      screen.Rect
	}
	var rows []row
	for _, e := range got {
		rows = append(rows, row{e.Role, e.Label, e.States, e.Bounds})
	}
	want := []row{
		{screen.RoleLink, "Forgot your password?", nil, screen.Rect{X: 327, Y: 1575, W: 552, H: 72}},
		{screen.RoleInput, "Username", []string{screen.StateEditable}, screen.Rect{X: 48, Y: 732, W: 1110, H: 111}},
		{screen.RoleInput, "Foo", []string{screen.StateEditable, screen.StatePassword}, screen.Rect{X: 48, Y: 978, W: 1110, H: 111}},
		{screen.RoleButton, "Tabs", nil, screen.Rect{X: 960, Y: 2376, W: 144, H: 144}},
	}
	if len(rows) != len(want)+8 || !reflect.DeepEqual(rows[:len(want)], want) {
		t.Errorf("elements = %+v\nwant %+v and 8 links", rows, want)
	}
}

func TestRAXRole(t *testing.T) {
	for _, c := range []struct {
		e      raxElement
		role   string
		states []string
	}{
		{raxElement{Type: xcKey, Enabled: true}, screen.RoleButton, nil},
		{raxElement{Type: xcSecureTextField, Enabled: true}, screen.RoleInput, []string{screen.StateEditable, screen.StatePassword}},
		{raxElement{Type: xcButton, Enabled: false, Selected: true}, screen.RoleButton, []string{screen.StateSelected, screen.StateDisabled}},
		// First pushes have no AutomationType: traits and the translator's role.
		{raxElement{Traits: traitKeyboardKey, Enabled: true}, screen.RoleButton, nil},
		{raxElement{AXRole: axpRoleSwitch, Value: "1", Enabled: true}, screen.RoleSwitch, []string{screen.StateChecked}},
		{raxElement{AXRole: axpRoleTextField, Enabled: true}, screen.RoleInput, []string{screen.StateEditable}},
		// UIKit's secure text field trait: native password fields carry it
		// as well as a web page's.
		{raxElement{AXRole: axpRoleTextField, Traits: traitSecureText, Enabled: true}, screen.RoleInput, []string{screen.StateEditable, screen.StatePassword}},
		{raxElement{AXRole: axpRoleTextField, Traits: traitSecureText, Enabled: true, Web: true}, screen.RoleInput, []string{screen.StateEditable, screen.StatePassword}},
		// A text view being edited (UIKit's text entry and is-editing
		// traits, bits 18 and 21) is no password field.
		{raxElement{AXRole: axpRoleTextField, Traits: 1<<18 | 1<<21, Enabled: true, Focused: true}, screen.RoleInput, []string{screen.StateEditable, screen.StateFocused}},
		{raxElement{AXRole: axpRoleButton, Enabled: true}, screen.RoleButton, nil},
		{raxElement{Traits: traitStaticText | traitHeader, Enabled: true}, screen.RoleText, nil},
	} {
		role, states := raxRole(c.e, false)
		if role != c.role || !reflect.DeepEqual(states, c.states) {
			t.Errorf("raxRole(%+v) = %s %v, want %s %v", c.e, role, states, c.role, c.states)
		}
	}
	if role, _ := raxRole(raxElement{Type: xcButton, Enabled: true}, true); role != screen.RoleTab {
		t.Errorf("a button in a tab bar is %s", role)
	}
}

// Calculator's display names its value in its identifier.
func TestRAXElementIdentifierValue(t *testing.T) {
	e, _ := raxElementFrom(&axpObject{ID: 1}, axpDict{axpAttrIdentifier: "StandardInputView;value:144", axpAttrRole: 4}, 0, 1)
	if e.Ident != "StandardInputView" || e.Value != "144" {
		t.Errorf("ident %q, value %q; want StandardInputView and 144", e.Ident, e.Value)
	}
	if role, _ := raxRole(e, false); role != screen.RoleText || raxLabel(e, role) != "144" {
		t.Errorf("the display reads %s %q; want text \"144\"", role, raxLabel(e, role))
	}
	e, _ = raxElementFrom(&axpObject{ID: 2}, axpDict{axpAttrIdentifier: "Display;value:12", axpAttrValue: "13"}, 0, 1)
	if e.Ident != "Display" || e.Value != "13" {
		t.Errorf("ident %q, value %q; want the element's own value to win", e.Ident, e.Value)
	}
	e, _ = raxElementFrom(&axpObject{ID: 3}, axpDict{axpAttrIdentifier: "com.apple.settings.general"}, 0, 1)
	if e.Ident != "com.apple.settings.general" || e.Value != "" {
		t.Errorf("a plain identifier changed: %+v", e)
	}
}

func TestRAXLabel(t *testing.T) {
	for _, c := range []struct {
		e    raxElement
		role string
		want string
	}{
		{raxElement{Label: "Software Update Available", Value: "1"}, screen.RoleButton, "Software Update Available, 1"},
		{raxElement{Label: "Airplane Mode", Value: "0"}, screen.RoleSwitch, "Airplane Mode"},
		{raxElement{Value: "Search"}, screen.RoleInput, "Search"},
		{raxElement{Placeholder: "Name"}, screen.RoleInput, "Name"},
		{raxElement{Ident: "StandardInputView"}, screen.RoleOther, "StandardInputView"},
		{raxElement{Ident: "StandardInputView", Value: "144"}, screen.RoleText, "144"},
		{raxElement{Label: "Clock", ValueText: "3:51 AM"}, screen.RoleButton, "Clock, 3:51 AM"},
	} {
		if got := raxLabel(c.e, c.role); got != c.want {
			t.Errorf("raxLabel(%+v) = %q, want %q", c.e, got, c.want)
		}
	}
}

func TestPixelRect(t *testing.T) {
	for _, c := range []struct {
		f    axpRect
		want screen.Rect
	}{
		{axpRect{22.666666666666686, 65, 68, 90.66666666666666}, screen.Rect{X: 68, Y: 195, W: 204, H: 272}},
		{axpRect{-10, 800, 100, 40}, screen.Rect{X: 0, Y: 2400, W: 270, H: 36}}, // clipped
		{axpRect{0, 900, 375, 40}, screen.Rect{}},                               // below the screen
	} {
		if got := pixelRect(c.f, 3, 1125, 2436); got != c.want {
			t.Errorf("pixelRect(%v) = %v, want %v", c.f, got, c.want)
		}
	}
}

func TestOCRExtras(t *testing.T) {
	els := []screen.Element{{Label: "Wi-Fi, DIYgod", Bounds: screen.Rect{X: 0, Y: 1500, W: 1125, H: 156}}}
	lines := []ocrLine{
		{Text: "DIYgod", X: 0.8, Y: 0.64, W: 0.1, H: 0.02, Confidence: 0.9}, // inside Wi-Fi's row
		{Text: "Low Power", X: 0.1, Y: 0.1, W: 0.2, H: 0.02, Confidence: 0.9},
		{Text: "noise", X: 0.5, Y: 0.5, W: 0.1, H: 0.02, Confidence: 0.1},
	}
	got := ocrExtras(els, lines, 1125, 2436)
	if len(got) != 1 || got[0].Label != "Low Power" || got[0].Source != screen.SourceOCR {
		t.Errorf("extras %+v", got)
	}
}

func TestRAXTimingConstants(t *testing.T) {
	// A pushed tree must be able to arrive after an input and still be
	// trusted, and the wait for a second push must end before trust does.
	if raxPushAfter >= raxPushTrust || raxSecondPush >= raxPushTrust || raxRequestTimeout <= 0 {
		t.Error("inconsistent timing constants")
	}
	_ = time.Second
}

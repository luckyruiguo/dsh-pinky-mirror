package companion

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

// Synthetic trees only: never commit dumps from real phones.

const (
	screenW = 1080
	screenH = 1920
)

func statusBar() Window {
	return Window{ID: 20, Type: WindowSystem, Layer: 20, Bounds: Bounds{0, 0, screenW, 63}}
}

func navBar() Window {
	return Window{ID: 21, Type: WindowSystem, Layer: 21, Title: "Navigation bar", Bounds: Bounds{0, 1794, screenW, screenH}}
}

func appWindow(pkg, name string, root *Node) Window {
	return Window{ID: 1, Type: WindowApplication, Layer: 1, Bounds: Bounds{0, 0, screenW, screenH},
		Active: true, Focused: true, Package: pkg, App: name, Root: root}
}

func tv(text string, b Bounds) *Node {
	return &Node{Class: "android.widget.TextView", Text: text, Bounds: b}
}

// row is a Settings-style preference: a clickable row with a title and a
// summary.
func row(title, summary string, top int) *Node {
	return &Node{
		Class: "android.widget.LinearLayout", Clickable: true, Focusable: true,
		Bounds: Bounds{0, top, screenW, top + 188},
		Children: []*Node{
			{Class: "android.widget.ImageView", ID: "android:id/icon", Bounds: Bounds{40, top + 60, 110, top + 130}},
			{Class: "android.widget.RelativeLayout", Bounds: Bounds{160, top + 30, 1040, top + 160}, Children: []*Node{
				{Class: "android.widget.TextView", ID: "android:id/title", Text: title, Bounds: Bounds{160, top + 30, 1040, top + 90}},
				{Class: "android.widget.TextView", ID: "android:id/summary", Text: summary, Bounds: Bounds{160, top + 95, 1040, top + 160}},
			}},
		},
	}
}

func settingsTree() *Tree {
	switchRow := &Node{
		Class: "android.widget.LinearLayout", Clickable: true, Focusable: true,
		Bounds: Bounds{0, 800, screenW, 988},
		Children: []*Node{
			{Class: "android.widget.TextView", ID: "android:id/title", Text: "Wi‑Fi", Bounds: Bounds{160, 830, 900, 890}},
			{Class: "android.widget.Switch", ID: "android:id/switch_widget", Checkable: true, Checked: true, Bounds: Bounds{920, 860, 1040, 930}},
		},
	}
	root := &Node{Class: "android.widget.FrameLayout", Bounds: Bounds{0, 0, screenW, screenH}, Children: []*Node{
		{Class: "android.widget.LinearLayout", ID: "com.android.settings:id/search_bar", Clickable: true,
			Bounds: Bounds{40, 80, 1040, 220}, Children: []*Node{
				{Class: "android.widget.ImageView", Desc: "", Bounds: Bounds{60, 110, 120, 190}},
				tv("Search settings", Bounds{140, 110, 900, 190}),
			}},
		{Class: "androidx.recyclerview.widget.RecyclerView", ID: "com.android.settings:id/recycler_view", Scrollable: true,
			Bounds: Bounds{0, 231, screenW, 1794}, Children: []*Node{
				row("网络和互联网", "WLAN、移动网络", 240),
				row("Connected devices", "Bluetooth, NFC", 520),
				switchRow,
			}},
	}}
	return &Tree{
		Display: Display{Width: screenW, Height: screenH, DensityDPI: 420},
		Windows: []Window{navBar(), statusBar(), appWindow("com.android.settings", "设置", root)},
	}
}

func lines(s *screen.Screen) []string {
	screen.NewRefs().Assign(s)
	return strings.Split(s.Render(), "\n")
}

func TestScreenSettings(t *testing.T) {
	s := settingsTree().Screen(ScreenOptions{})
	got := lines(s)
	want := []string{
		"screen 1080x1920 · app: 设置 (com.android.settings) · portrait · keyboard hidden",
		`[1] button "Search settings" @540,150`,
		`[2] list @540,1012 scrollable`,
		`[3] button "` + "网络和互联网, WLAN、移动网络" + `" @540,334`,
		`[4] button "Connected devices, Bluetooth, NFC" @540,614`,
		`[5] switch "Wi` + "‑" + `Fi" @540,894 checked`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("screen:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if s.Keyboard || s.Locked || s.Orientation != "portrait" {
		t.Errorf("header state: keyboard=%v locked=%v orientation=%q", s.Keyboard, s.Locked, s.Orientation)
	}
}

func TestScreenPathsAndIDs(t *testing.T) {
	s := settingsTree().Screen(ScreenOptions{})
	byLabel := map[string]screen.Element{}
	for _, e := range s.Elements {
		byLabel[e.Label] = e
	}
	e := byLabel["Connected devices, Bluetooth, NFC"]
	if want := "application:com.android.settings>FrameLayout>RecyclerView#recycler_view"; e.Path != want {
		t.Errorf("row path = %q, want %q", e.Path, want)
	}
	search := byLabel["Search settings"]
	if search.ID != "com.android.settings:id/search_bar" || search.Path != "application:com.android.settings>FrameLayout" {
		t.Errorf("search bar id %q path %q", search.ID, search.Path)
	}
	for _, e := range s.Elements {
		if e.Source != screen.SourceTree {
			t.Errorf("%q: source %q", e.Label, e.Source)
		}
	}
}

func TestRefsSurviveInsertedSibling(t *testing.T) {
	refs := screen.NewRefs()
	before := settingsTree().Screen(ScreenOptions{})
	refs.Assign(before)

	tr := settingsTree()
	list := tr.Windows[2].Root.Children[1]
	list.Children = append([]*Node{row("Suggestion", "Try dark theme", 240)}, list.Children...)
	after := tr.Screen(ScreenOptions{})
	refs.Assign(after)

	ref := func(s *screen.Screen, label string) int {
		for _, e := range s.Elements {
			if e.Label == label {
				return e.Ref
			}
		}
		t.Fatalf("no element %q", label)
		return 0
	}
	for _, label := range []string{"Connected devices, Bluetooth, NFC", "Wi‑Fi"} {
		if a, b := ref(before, label), ref(after, label); a != b {
			t.Errorf("%q: ref %d became %d after a sibling was inserted", label, a, b)
		}
	}
}

func TestScreenCalculatorLabels(t *testing.T) {
	btn := func(text, desc string, x int) *Node {
		return &Node{Class: "android.widget.Button", Text: text, Desc: desc, Clickable: true, Focusable: true,
			Bounds: Bounds{x, 1000, x + 260, 1160}}
	}
	root := &Node{Class: "android.widget.LinearLayout", Bounds: Bounds{0, 0, screenW, screenH}, Children: []*Node{
		{Class: "android.widget.EditText", ID: "com.example.calc:id/formula", Desc: "No formula", Editable: true, Focused: true,
			Focusable: true, Bounds: Bounds{0, 200, screenW, 400}},
		btn("7", "", 0),
		btn("×", "multiply", 270),
		{Class: "android.widget.ImageButton", Desc: "删除", Clickable: true, Bounds: Bounds{540, 1000, 800, 1160}},
	}}
	tr := &Tree{Display: Display{Width: screenW, Height: screenH}, Windows: []Window{appWindow("com.example.calc", "Calculator", root)}}
	got := lines(tr.Screen(ScreenOptions{}))
	want := []string{
		"screen 1080x1920 · app: Calculator (com.example.calc) · portrait · keyboard hidden · focus [1]",
		`[1] input "No formula" @540,300 focused`,
		`[2] button "7" @130,1080`,
		`[3] button "multiply" @400,1080`,
		`[4] button "删除" @670,1080`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("screen:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestScreenInputsAndPasswords(t *testing.T) {
	root := &Node{Class: "android.widget.FrameLayout", Bounds: Bounds{0, 0, screenW, screenH}, Children: []*Node{
		{Class: "android.widget.EditText", ID: "app:id/name", Hint: "Name", Editable: true, Bounds: Bounds{0, 300, screenW, 400}},
		{Class: "android.widget.EditText", ID: "app:id/note", Hint: "Note", Text: "你好，Pinky", Editable: true, Bounds: Bounds{0, 420, screenW, 520}},
		// The app never sends a password field's text; the client must not
		// show it even if one arrives.
		{Class: "android.widget.EditText", ID: "app:id/pw", Hint: "Password", Text: "hunter2", Password: true, Editable: true, Bounds: Bounds{0, 540, screenW, 640}},
		{Class: "android.widget.EditText", ID: "app:id/off", Hint: "Disabled", Editable: true, Disabled: true, Bounds: Bounds{0, 660, screenW, 760}},
	}}
	tr := &Tree{Display: Display{Width: screenW, Height: screenH}, Windows: []Window{appWindow("app", "App", root)}}
	s := tr.Screen(ScreenOptions{})
	got := lines(s)
	want := []string{
		"screen 1080x1920 · app: App (app) · portrait · keyboard hidden",
		`[1] input "Name" @540,350`,
		`[2] input "` + "你好，Pinky" + `" @540,470`,
		`[3] input "Password" @540,590 password`,
		`[4] input "Disabled" @540,710 disabled`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("screen:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if strings.Contains(s.Render(), "hunter2") {
		t.Fatal("password text leaked into the screen")
	}
}

func TestScreenKeyboardOccludes(t *testing.T) {
	root := &Node{Class: "android.widget.FrameLayout", Bounds: Bounds{0, 0, screenW, screenH}, Children: []*Node{
		{Class: "android.widget.EditText", Text: "hello", Editable: true, Focused: true, Bounds: Bounds{0, 300, screenW, 400}},
		{Class: "android.widget.ListView", Scrollable: true, Bounds: Bounds{0, 600, screenW, 1794}},
		{Class: "android.widget.Button", Text: "Send", Clickable: true, Bounds: Bounds{0, 1500, screenW, 1600}},
	}}
	ime := Window{ID: 9, Type: WindowInputMethod, Layer: 30, Bounds: Bounds{0, 1100, screenW, 1794}}
	tr := &Tree{Display: Display{Width: screenW, Height: screenH},
		Windows: []Window{ime, navBar(), statusBar(), appWindow("app", "App", root)}}
	s := tr.Screen(ScreenOptions{})
	if !s.Keyboard {
		t.Fatal("keyboard not reported")
	}
	got := lines(s)
	want := []string{
		"screen 1080x1920 · app: App (app) · portrait · keyboard shown · focus [1]",
		`[1] input "hello" @540,350 focused`,
		// The list's lower part is under the keyboard; it keeps the rest.
		`[2] list @540,850 scrollable`,
		// "Send" sits under the keyboard and is dropped.
	}
	if !slices.Equal(got, want) {
		t.Fatalf("screen:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// keyboardPanel is a keyboard's own panel over an app that shows nothing
// (Gboard's setup, a Unity game's text box), read with its nodes as the
// driver reads it again (DumpOptions.IME).
func keyboardPanel() *Tree {
	panel := &Node{Class: "android.widget.FrameLayout", Bounds: Bounds{0, 1100, screenW, 1794}, Children: []*Node{
		{Class: "android.widget.Button", Text: "Next", Clickable: true, Bounds: Bounds{600, 1600, 1000, 1700}},
	}}
	ime := Window{ID: 9, Type: WindowInputMethod, Layer: 30, Package: "com.google.android.inputmethod.latin", Bounds: Bounds{0, 1100, screenW, 1794}, Root: panel}
	app := &Node{Class: "android.widget.FrameLayout", Bounds: Bounds{0, 0, screenW, screenH}}
	return &Tree{Display: Display{Width: screenW, Height: screenH},
		Windows: []Window{ime, navBar(), statusBar(), appWindow("com.example.game", "Game", app)}}
}

// The keyboard's frame is where it lies over the app (GitHub issue #71),
// but not when its own nodes are listed: then the agent acts on it. The
// system bars count either way.
func TestScreenKeyboardFrame(t *testing.T) {
	bars := []screen.Rect{{Y: 1794, W: screenW, H: screenH - 1794}, {W: screenW, H: 63}}
	tr := keyboardPanel()
	s := tr.Screen(ScreenOptions{Keyboard: true})
	if !s.Keyboard || !s.KeyboardFrame.Empty() || !slices.Equal(s.Covered, bars) {
		t.Fatalf("with its nodes: keyboard %v at %+v, covered %+v", s.Keyboard, s.KeyboardFrame, s.Covered)
	}
	if got := lines(s); len(got) != 2 || got[1] != `[1] button "Next" @800,1650` {
		t.Fatalf("screen:\n%s", strings.Join(got, "\n"))
	}
	tr.Windows[0].Root = nil
	s = tr.Screen(ScreenOptions{})
	if want := (screen.Rect{Y: 1100, W: screenW, H: 694}); !s.Keyboard || s.KeyboardFrame != want || !slices.Equal(s.Covered, bars) {
		t.Fatalf("over the app: keyboard %v at %+v, covered %+v", s.Keyboard, s.KeyboardFrame, s.Covered)
	}
}

func TestScreenSystemBarsAndOcclusion(t *testing.T) {
	root := &Node{Class: "android.widget.FrameLayout", Bounds: Bounds{0, 0, screenW, screenH}, Children: []*Node{
		tv("Under the status bar", Bounds{0, 0, screenW, 63}),
		tv("Title", Bounds{0, 70, screenW, 200}),
	}}
	bar := statusBar()
	bar.Package = "com.android.systemui"
	bar.Root = &Node{Class: "android.widget.FrameLayout", Bounds: Bounds{0, 0, screenW, 63}, Children: []*Node{tv("12:00", Bounds{40, 0, 160, 63})}}
	tr := &Tree{Display: Display{Width: screenW, Height: screenH}, Windows: []Window{navBar(), bar, appWindow("app", "App", root)}}

	got := lines(tr.Screen(ScreenOptions{}))
	if want := []string{"screen 1080x1920 · app: App (app) · portrait · keyboard hidden", `[1] text "Title" @540,135`}; !slices.Equal(got, want) {
		t.Fatalf("without bars:\n%s", strings.Join(got, "\n"))
	}
	got = lines(tr.Screen(ScreenOptions{SystemBars: true}))
	if !slices.Contains(got, `[1] text "12:00" @100,31`) {
		t.Fatalf("with bars:\n%s", strings.Join(got, "\n"))
	}
}

func TestScreenDialogAndShade(t *testing.T) {
	app := appWindow("com.example", "Example", &Node{Class: "android.widget.FrameLayout", Bounds: Bounds{0, 0, screenW, screenH},
		Children: []*Node{tv("Behind", Bounds{0, 100, screenW, 200})}})
	app.Active, app.Focused = false, false
	dialog := Window{ID: 5, Type: WindowApplication, Layer: 5, Bounds: Bounds{100, 700, 980, 1200}, Active: true, Focused: true,
		Package: "com.android.permissioncontroller", App: "Permissions",
		Root: &Node{Class: "android.widget.FrameLayout", Bounds: Bounds{100, 700, 980, 1200}, Children: []*Node{
			tv("Allow access?", Bounds{140, 740, 940, 820}),
			{Class: "android.widget.Button", Text: "Allow", Clickable: true, Bounds: Bounds{600, 1080, 940, 1180}},
		}}}
	tr := &Tree{Display: Display{Width: screenW, Height: screenH}, Windows: []Window{dialog, app}}
	s := tr.Screen(ScreenOptions{})
	if s.App.ID != "com.android.permissioncontroller" {
		t.Errorf("foreground = %q, want the dialog's package", s.App.ID)
	}
	got := lines(s)
	want := []string{
		"screen 1080x1920 · app: Permissions (com.android.permissioncontroller) · portrait · keyboard hidden",
		`[1] text "Allow access?" @540,780`,
		`[2] button "Allow" @770,1130`,
		`[3] text "Behind" @540,150`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("screen:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	shade := Window{ID: 7, Type: WindowSystem, Layer: 40, Bounds: Bounds{0, 0, screenW, screenH}, Focused: true, Package: "com.android.systemui",
		Root: &Node{Class: "android.widget.FrameLayout", Bounds: Bounds{0, 0, screenW, screenH}, Children: []*Node{
			{Class: "android.widget.FrameLayout", Desc: "Wi‑Fi", Clickable: true, Bounds: Bounds{0, 100, 270, 300}},
		}}}
	tr = &Tree{Display: Display{Width: screenW, Height: screenH}, Windows: []Window{shade, appWindow("com.example", "Example", nil)}}
	if s := tr.Screen(ScreenOptions{}); s.App.ID != "com.android.systemui" || len(s.Elements) != 1 {
		t.Errorf("shade: app %q, %d elements", s.App.ID, len(s.Elements))
	}
}

func TestScreenClickableContainers(t *testing.T) {
	var texts []*Node
	for i := 0; i < 3; i++ {
		texts = append(texts, tv(strings.Repeat("x", i+1), Bounds{0, 300 + i*100, screenW, 380 + i*100}))
	}
	many := &Node{Class: "android.widget.LinearLayout", Clickable: true, Bounds: Bounds{0, 1000, screenW, 1200}}
	for i := 0; i < maxMergeChildren+1; i++ {
		many.Children = append(many.Children, tv("item", Bounds{0, 1000 + i*10, 100, 1010 + i*10}))
	}
	root := &Node{Class: "android.widget.FrameLayout", Clickable: true, Bounds: Bounds{0, 0, screenW, screenH}, Children: []*Node{
		{Class: "android.widget.LinearLayout", Bounds: Bounds{0, 300, screenW, 600}, Children: texts},
		{Class: "android.widget.FrameLayout", Desc: "Play", Clickable: true, Bounds: Bounds{0, 700, 400, 900}, Children: []*Node{
			tv("Play now", Bounds{20, 720, 380, 880}),
		}},
		{Class: "android.widget.ImageButton", Clickable: true, Bounds: Bounds{900, 700, 1000, 800}},
		many,
	}}
	tr := &Tree{Display: Display{Width: screenW, Height: screenH}, Windows: []Window{appWindow("app", "App", root)}}
	s := tr.Screen(ScreenOptions{})
	got := lines(s)
	// The full-screen clickable root takes no label and is left out; its
	// texts stay separate. A labeled target keeps its children's text as
	// their own lines. A small unlabeled button is listed. A target with too
	// many texts does not merge them.
	if got[1] != `[1] text "x" @540,340` || !slices.Contains(got, `[4] button "Play" @200,800`) ||
		!slices.Contains(got, `[5] text "Play now" @200,800`) || !slices.Contains(got, `[6] button @950,750`) {
		t.Fatalf("screen:\n%s", strings.Join(got, "\n"))
	}
	n := 0
	for _, e := range s.Elements {
		if e.Label == "item" {
			n++
		}
	}
	if n != maxMergeChildren+1 {
		t.Errorf("unmerged items listed %d times, want %d", n, maxMergeChildren+1)
	}
}

func TestScreenNestedListsAndPartlyVisibleRows(t *testing.T) {
	list := &Node{Class: "androidx.recyclerview.widget.RecyclerView", Scrollable: true, Bounds: Bounds{0, 231, screenW, 1794},
		Children: []*Node{
			row("Visible", "Fully on screen", 240),
			// Only 40 of its 188 pixels are on screen; its texts are below.
			{Class: "android.widget.LinearLayout", Clickable: true, Bounds: Bounds{0, 1754, screenW, 1942}},
			// Newer Android reports only the visible sliver of such a row.
			{Class: "android.widget.LinearLayout", Clickable: true, Bounds: Bounds{0, 1778, screenW, 1794}},
		}}
	root := &Node{Class: "android.widget.FrameLayout", Bounds: Bounds{0, 0, screenW, screenH}, Children: []*Node{
		{Class: "androidx.core.widget.NestedScrollView", Scrollable: true, Bounds: Bounds{0, 231, screenW, 1794}, Children: []*Node{list}},
	}}
	w := appWindow("app", "App", root)
	w.Bounds = Bounds{0, 0, screenW, 1794}
	tr := &Tree{Display: Display{Width: screenW, Height: screenH, DensityDPI: 420}, Windows: []Window{w}}
	got := lines(tr.Screen(ScreenOptions{}))
	want := []string{
		"screen 1080x1920 · app: App (app) · portrait · keyboard hidden",
		`[1] list @540,1012 scrollable`,
		`[2] button "Visible, Fully on screen" @540,334`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("screen:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestRoleOf(t *testing.T) {
	tests := []struct {
		n    Node
		want string
	}{
		{Node{Class: "android.widget.EditText"}, screen.RoleInput},
		{Node{Class: "android.view.View", Editable: true}, screen.RoleInput},
		{Node{Class: "android.widget.Switch", Checkable: true}, screen.RoleSwitch},
		{Node{Class: "androidx.appcompat.widget.SwitchCompat", Checkable: true}, screen.RoleSwitch},
		{Node{Class: "android.widget.ToggleButton", Checkable: true}, screen.RoleSwitch},
		{Node{Class: "android.widget.CheckBox", Checkable: true}, screen.RoleCheckbox},
		{Node{Class: "android.widget.RadioButton", Checkable: true}, screen.RoleCheckbox},
		{Node{Class: "android.widget.CheckedTextView", Checkable: true}, screen.RoleCheckbox},
		{Node{Class: "com.google.android.material.tabs.TabLayout$TabView", Clickable: true}, screen.RoleTab},
		{Node{Class: "android.app.ActionBar$Tab", Clickable: true}, screen.RoleTab},
		{Node{Class: "android.view.View", Role: "Tab", Clickable: true}, screen.RoleTab},
		{Node{Class: "android.view.View", Role: "Switch", Clickable: true}, screen.RoleSwitch},
		{Node{Class: "android.view.View", Role: "link", Clickable: true}, screen.RoleLink},
		{Node{Class: "androidx.recyclerview.widget.RecyclerView", Scrollable: true}, screen.RoleList},
		{Node{Class: "android.widget.ScrollView"}, screen.RoleList},
		{Node{Class: "android.widget.ImageView"}, screen.RoleImage},
		{Node{Class: "android.widget.ImageView", Clickable: true}, screen.RoleButton},
		{Node{Class: "android.widget.TextView", Clickable: true}, screen.RoleButton},
		{Node{Class: "android.widget.Button", Disabled: true}, screen.RoleButton},
		{Node{Class: "android.widget.SeekBar"}, screen.RoleOther},
		{Node{Class: "android.widget.TextView"}, screen.RoleText},
	}
	for _, tt := range tests {
		var check *Node
		if tt.n.Checkable {
			check = &tt.n
		}
		if got := roleOf(&tt.n, check); got != tt.want {
			t.Errorf("roleOf(%s role=%q) = %s, want %s", tt.n.Class, tt.n.Role, got, tt.want)
		}
	}
}

func TestStatesOrder(t *testing.T) {
	n := &Node{Focused: true, Selected: true, Checkable: true, Checked: true, Disabled: true, Scrollable: true, Editable: true, Password: true}
	got := states(n, screen.RoleList, n)
	want := []string{"focused", "selected", "checked", "disabled", "scrollable", "editable", "password"}
	if !slices.Equal(got, want) {
		t.Errorf("states = %v, want %v", got, want)
	}
	if got := states(n, screen.RoleInput, n); slices.Contains(got, "editable") {
		t.Errorf("inputs should not repeat editable: %v", got)
	}
}

func TestCleanLabels(t *testing.T) {
	if got := clean("  Hello\n\n  world\t "); got != "Hello world" {
		t.Errorf("clean = %q", got)
	}
	long := strings.Repeat("字", maxLabel+50)
	got := clean(long)
	if r := []rune(got); len(r) != maxLabel || r[len(r)-1] != '…' {
		t.Errorf("long label: %d runes, ends %q", len(r), string(r[len(r)-1]))
	}
}

func TestLandscapeAndSideNavBar(t *testing.T) {
	tr := &Tree{
		Display: Display{Width: screenH, Height: screenW, Rotation: 90},
		Windows: []Window{
			{ID: 3, Type: WindowSystem, Layer: 3, Bounds: Bounds{screenH - 126, 0, screenH, screenW}},
			appWindow("app", "App", &Node{Class: "android.widget.FrameLayout", Bounds: Bounds{0, 0, screenH, screenW},
				Children: []*Node{tv("Wide", Bounds{0, 100, screenH - 126, 200})}}),
		},
	}
	s := tr.Screen(ScreenOptions{})
	if s.Orientation != "landscape" || len(s.Elements) != 1 || s.Width != screenH {
		t.Fatalf("landscape: %s", s.Render())
	}
	if !tr.isSystemBar(tr.Windows[0]) {
		t.Error("side navigation bar not recognized")
	}
}

// The wire format as the app writes it.
const sampleTree = `{"display":{"id":0,"width":1080,"height":1920,"density_dpi":420,"rotation":0},"locked":false,"secure_locked":false,
"windows":[{"id":44,"type":"system","layer":3,"title":"Navigation bar","bounds":[0,1794,1080,1920],"root_ms":0,"walk_ms":0,"nodes":0},
{"id":41,"type":"application","layer":1,"title":"Pinky Companion","bounds":[0,0,1080,1920],"active":true,"focused":true,
"package":"app.pinky.companion","app":"Pinky Companion","root":{"class":"android.widget.FrameLayout","bounds":[0,0,1080,1920],"node":101,
"children":[{"class":"android.widget.EditText","text":"你好 🎉","hint":"Text input","id":"app.pinky.companion:id/debug_input","bounds":[63,1252,1017,1340],"clickable":true,"long_clickable":true,"editable":true,"focusable":true,"node":4242},
{"class":"android.widget.EditText","hint":"Password field","id":"app.pinky.companion:id/debug_password","bounds":[63,1465,1017,1552],"editable":true,"focusable":true,"password":true,"node":4243}]},
"root_ms":12,"walk_ms":3,"nodes":3}],"windows_ms":1,"nodes":3,"time":1793985948420,"took_ms":20}`

func TestDecodeWireTree(t *testing.T) {
	var tr Tree
	if err := json.Unmarshal([]byte(sampleTree), &tr); err != nil {
		t.Fatal(err)
	}
	if tr.Windows[1].Root.Children[0].Handle != 4242 || tr.Windows[1].RootMS != 12 || tr.TookMS != 20 {
		t.Fatalf("decoded: %+v", tr.Windows[1])
	}
	got := lines(tr.Screen(ScreenOptions{}))
	want := []string{
		"screen 1080x1920 · app: Pinky Companion (app.pinky.companion) · portrait · keyboard hidden",
		`[1] input "` + "你好 \U0001f389" + `" @540,1296`,
		`[2] input "Password field" @540,1508 password`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("screen:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Gboard's stylus toolbar on Android 16: a window over the lower half of
// the screen whose touchable region is only the toolbar and a strip.
func TestScreenOccludesByRegion(t *testing.T) {
	root := &Node{Class: "android.widget.FrameLayout", Bounds: Bounds{0, 0, screenW, screenH}, Children: []*Node{
		{Class: "android.widget.EditText", Text: "pinky", Editable: true, Focused: true, Bounds: Bounds{126, 128, screenW, 317}},
		{Class: "android.widget.Button", Text: "Pinky Companion", Clickable: true, Bounds: Bounds{0, 1000, screenW, 1150}},
		{Class: "android.widget.Button", Text: "Under the toolbar", Clickable: true, Bounds: Bounds{21, 1200, 169, 1300}},
	}}
	toolbar := Window{ID: 9, Type: WindowInputMethod, Layer: 30, Bounds: Bounds{0, 896, screenW, screenH},
		Region: []Bounds{{21, 896, 169, 1586}, {0, 1880, screenW, screenH}}}
	tr := &Tree{Display: Display{Width: screenW, Height: screenH},
		Windows: []Window{toolbar, appWindow("app", "App", root)}}
	got := lines(tr.Screen(ScreenOptions{}))
	want := []string{
		"screen 1080x1920 · app: App (app) · portrait · keyboard shown · focus [1]",
		`[1] input "pinky" @603,222 focused`,
		`[2] button "Pinky Companion" @540,1075`,
		// The button under the toolbar itself is hidden.
	}
	if !slices.Equal(got, want) {
		t.Fatalf("screen:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A keyboard sheet that covers the app (Gboard's stylus tutorial): the
// app sends the sheet's nodes without being asked, and they are what the
// person sees.
func TestScreenKeyboardSheet(t *testing.T) {
	app := &Node{Class: "android.widget.FrameLayout", Bounds: Bounds{0, 0, screenW, screenH}, Children: []*Node{
		{Class: "android.widget.EditText", Text: "Search settings", Editable: true, Focused: true, Invisible: true, Bounds: Bounds{126, 128, screenW, 317}},
	}}
	sheet := &Node{Class: "android.widget.FrameLayout", Bounds: Bounds{0, 128, screenW, screenH}, Children: []*Node{
		tv("Try out your stylus", Bounds{100, 1200, 980, 1300}),
		{Class: "android.widget.Button", Text: "Cancel", Clickable: true, Bounds: Bounds{500, 1800, 700, 1880}},
	}}
	ime := Window{ID: 9, Type: WindowInputMethod, Layer: 30, Bounds: Bounds{0, 128, screenW, screenH}, Root: sheet}
	tr := &Tree{Display: Display{Width: screenW, Height: screenH},
		Windows: []Window{ime, appWindow("app", "App", app)}}
	got := lines(tr.Screen(ScreenOptions{}))
	want := []string{
		"screen 1080x1920 · app: App (app) · portrait · keyboard shown",
		`[1] text "Try out your stylus" @540,1250`,
		`[2] button "Cancel" @600,1840`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("screen:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	var w Window
	if err := json.Unmarshal([]byte(`{"id":9,"type":"input_method","bounds":[0,896,1080,2400],"region":[[21,896,169,1586],[0,2337,1080,2400]]}`), &w); err != nil || len(w.Region) != 2 || w.Region[1] != (Bounds{0, 2337, 1080, 2400}) {
		t.Errorf("region from the wire: %+v, %v", w.Region, err)
	}
}

package android

import (
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

// Fixtures are hand-written in uiautomator's format; they are not dumps of
// real devices. {NAME} placeholders stand for non-ASCII test strings.
var fixtureText = strings.NewReplacer(
	"{SETTINGS}", "\u8bbe\u7f6e", // Settings
	"{DISPLAY}", "\u663e\u793a", // Display
	"{DARK}", "\u6df1\u8272\u4e3b\u9898", // Dark theme
	"{WIFI_ON}", "\u5df2\u8fde\u63a5", // Connected
	"{SEARCH}", "\u641c\u7d22\u8bbe\u7f6e", // Search settings
	"{DOT}", " \u00b7 ", // the separator of merged labels
)

func fixture(xml string) []byte {
	return []byte(fixtureText.Replace(xml))
}

const settingsFixture = `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation="0">
<node index="0" text="" resource-id="" class="android.widget.FrameLayout" package="com.android.settings" content-desc="" checkable="false" checked="false" clickable="false" enabled="true" focusable="false" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[0,0][1080,2340]">
 <node index="0" text="" resource-id="com.android.settings:id/search_action_bar" class="android.widget.LinearLayout" package="com.android.settings" content-desc="" checkable="false" checked="false" clickable="true" enabled="true" focusable="true" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[42,140][1038,282]">
  <node index="0" text="" resource-id="" class="android.widget.ImageView" package="com.android.settings" content-desc="" checkable="false" checked="false" clickable="false" enabled="true" focusable="false" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[84,180][144,240]" />
  <node index="1" text="{SEARCH}" resource-id="com.android.settings:id/search_action_bar_title" class="android.widget.TextView" package="com.android.settings" content-desc="" checkable="false" checked="false" clickable="false" enabled="true" focusable="false" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[180,180][600,240]" />
 </node>
 <node index="1" text="" resource-id="com.android.settings:id/recycler_view" class="androidx.recyclerview.widget.RecyclerView" package="com.android.settings" content-desc="" checkable="false" checked="false" clickable="false" enabled="true" focusable="true" focused="false" scrollable="true" long-clickable="false" password="false" selected="false" bounds="[0,300][1080,2340]">
  <node index="0" text="" resource-id="" class="android.widget.LinearLayout" package="com.android.settings" content-desc="" checkable="false" checked="false" clickable="true" enabled="true" focusable="true" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[0,300][1080,500]">
   <node index="0" text="{DISPLAY}" resource-id="android:id/title" class="android.widget.TextView" package="com.android.settings" content-desc="" checkable="false" checked="false" clickable="false" enabled="true" focusable="false" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[180,330][600,390]" />
   <node index="1" text="{DARK}" resource-id="android:id/summary" class="android.widget.TextView" package="com.android.settings" content-desc="" checkable="false" checked="false" clickable="false" enabled="true" focusable="false" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[180,400][700,450]" />
  </node>
  <node index="1" text="" resource-id="" class="android.widget.LinearLayout" package="com.android.settings" content-desc="" checkable="false" checked="false" clickable="true" enabled="true" focusable="true" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[0,500][1080,700]">
   <node index="0" text="Wi-Fi" resource-id="android:id/title" class="android.widget.TextView" package="com.android.settings" content-desc="" checkable="false" checked="false" clickable="false" enabled="true" focusable="false" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[180,530][600,590]" />
   <node index="1" text="{WIFI_ON}" resource-id="android:id/summary" class="android.widget.TextView" package="com.android.settings" content-desc="" checkable="false" checked="false" clickable="false" enabled="true" focusable="false" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[180,600][700,650]" />
   <node index="2" text="" resource-id="android:id/switch_widget" class="android.widget.Switch" package="com.android.settings" content-desc="" checkable="true" checked="true" clickable="false" enabled="true" focusable="false" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[900,560][1020,640]" />
  </node>
  <node index="2" text="" resource-id="" class="android.widget.LinearLayout" package="com.android.settings" content-desc="" checkable="false" checked="false" clickable="true" enabled="false" focusable="true" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[0,700][1080,900]">
   <node index="0" text="Bluetooth" resource-id="android:id/title" class="android.widget.TextView" package="com.android.settings" content-desc="" checkable="false" checked="false" clickable="false" enabled="false" focusable="false" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[180,730][600,790]" />
  </node>
  <node index="3" text="Off screen" resource-id="" class="android.widget.TextView" package="com.android.settings" content-desc="" checkable="false" checked="false" clickable="false" enabled="true" focusable="false" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[0,2400][1080,2500]" />
 </node>
</node>
</hierarchy>UI hierchary dumped to: /dev/tty`

func TestParseUIDump(t *testing.T) {
	tree, err := parseUIDump(fixture(settingsFixture))
	if err != nil {
		t.Fatal(err)
	}
	if tree.Rotation != 0 || len(tree.Roots) != 1 || tree.Package() != "com.android.settings" {
		t.Fatalf("tree = %+v", tree)
	}
	root := tree.Roots[0]
	if len(root.Children) != 2 || root.Bounds != (screen.Rect{X: 0, Y: 0, W: 1080, H: 2340}) {
		t.Fatalf("root = %+v", root)
	}
	sw := root.Children[1].Children[1].Children[2]
	if sw.Class != "android.widget.Switch" || !sw.Checkable || !sw.Checked || sw.Clickable {
		t.Errorf("switch = %+v", sw)
	}

	if _, err := parseUIDump([]byte("ERROR: could not get idle state.")); err != errNoHierarchy {
		t.Errorf("no hierarchy: %v", err)
	}
	empty, err := parseUIDump([]byte(`<?xml version='1.0' ?><hierarchy rotation="1" />`))
	if err != nil || empty.Rotation != 1 || len(empty.Roots) != 0 {
		t.Errorf("empty = %+v, %v", empty, err)
	}
	// Control characters some apps put in text are dropped, not fatal.
	bad := []byte("<hierarchy rotation=\"0\"><node text=\"a\x01b\x00\" class=\"android.widget.TextView\" bounds=\"[0,0][10,10]\" /></hierarchy>")
	tr, err := parseUIDump(bad)
	if err != nil || tr.Roots[0].Text != "ab" {
		t.Errorf("sanitized = %+v, %v", tr, err)
	}
}

func lines(els []screen.Element) []string {
	var out []string
	for _, e := range els {
		out = append(out, e.Line())
	}
	return out
}

func TestConvertSettings(t *testing.T) {
	tree, err := parseUIDump(fixture(settingsFixture))
	if err != nil {
		t.Fatal(err)
	}
	els := convertTree(tree, 1080, 2340)
	got := strings.Join(lines(els), "\n")
	want := fixtureText.Replace(strings.Join([]string{
		`[0] button "{SEARCH}" @540,211`,
		`[0] list "recycler_view" @540,1320 scrollable`,
		`[0] button "{DISPLAY}{DOT}{DARK}" @540,400`,
		`[0] switch "Wi-Fi{DOT}{WIFI_ON}" @540,600 checked`,
		`[0] button "Bluetooth" @540,800 disabled`,
	}, "\n"))
	if got != want {
		t.Errorf("elements:\n%s\nwant:\n%s", got, want)
	}
	// Paths hold ancestor classes and ids, not the element itself.
	if els[2].Path != "FrameLayout/RecyclerView#recycler_view" {
		t.Errorf("path = %q", els[2].Path)
	}
	if els[0].ID != "com.android.settings:id/search_action_bar" {
		t.Errorf("id = %q", els[0].ID)
	}
	for _, e := range els {
		if e.Source != screen.SourceTree {
			t.Errorf("source = %q", e.Source)
		}
	}
}

const inputFixture = `<hierarchy rotation="0">
<node text="" class="android.widget.FrameLayout" package="org.example.notes" content-desc="" clickable="false" enabled="true" bounds="[0,0][1080,1920]">
 <node text="Search" hint="Search" resource-id="org.example.notes:id/query" class="android.widget.EditText" package="org.example.notes" content-desc="" clickable="true" focusable="true" focused="true" enabled="true" bounds="[40,100][1040,200]" />
 <node text="hello  world" hint="Title" resource-id="org.example.notes:id/title" class="android.widget.EditText" package="org.example.notes" content-desc="" clickable="true" focusable="true" enabled="true" bounds="[40,220][1040,320]" />
 <node text="&#8226;&#8226;&#8226;&#8226;" hint="Password" resource-id="org.example.notes:id/pw" class="android.widget.EditText" package="org.example.notes" content-desc="" clickable="true" focusable="true" password="true" enabled="true" bounds="[40,340][1040,440]" />
 <node text="" resource-id="org.example.notes:id/fab" class="com.google.android.material.floatingactionbutton.FloatingActionButton" package="org.example.notes" content-desc="New note" clickable="true" enabled="true" bounds="[880,1700][1040,1860]" />
 <node text="" resource-id="org.example.notes:id/menu" class="android.widget.ImageButton" package="org.example.notes" content-desc="" clickable="true" enabled="true" bounds="[980,20][1060,80]" />
 <node text="" resource-id="" class="android.widget.ImageView" package="org.example.notes" content-desc="Logo" clickable="false" enabled="true" bounds="[40,20][120,80]" />
 <node text="Done" resource-id="" class="android.widget.CheckBox" package="org.example.notes" content-desc="" checkable="true" checked="true" clickable="true" enabled="true" bounds="[40,500][400,560]" />
 <node text="Today" resource-id="" class="com.google.android.material.tabs.TabLayout$TabView" package="org.example.notes" content-desc="" clickable="true" selected="true" enabled="true" bounds="[0,600][540,700]" />
 <node text="" resource-id="" class="android.view.View" package="org.example.notes" content-desc="" clickable="false" enabled="true" bounds="[0,800][1080,900]" />
 <node text="Half off" resource-id="" class="android.widget.TextView" package="org.example.notes" content-desc="" clickable="false" enabled="true" bounds="[-100,950][100,1000]" />
</node>
</hierarchy>`

func TestConvertInputsAndRoles(t *testing.T) {
	tree, err := parseUIDump([]byte(inputFixture))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(lines(convertTree(tree, 1080, 1920)), "\n")
	want := strings.Join([]string{
		`[0] input "Search" @540,150 focused editable`,    // empty field: its hint
		`[0] input "hello world" @540,270 editable`,       // content, whitespace collapsed
		`[0] input "Password" @540,390 editable password`, // never the content
		`[0] button "New note" @960,1780`,
		`[0] button "menu" @1020,50`, // only a resource id
		`[0] image "Logo" @80,50`,
		`[0] checkbox "Done" @220,530 checked`,
		`[0] tab "Today" @270,650 selected`,
		`[0] text "Half off" @50,975`, // clipped to the screen
	}, "\n")
	if got != want {
		t.Errorf("elements:\n%s\nwant:\n%s", got, want)
	}
}

func TestConvertMergeLimits(t *testing.T) {
	// A clickable container covering most of the screen keeps its children
	// separate, so a full-screen tap target does not swallow the page.
	big := `<hierarchy rotation="0"><node text="" class="android.widget.FrameLayout" package="p" clickable="true" enabled="true" bounds="[0,0][1000,1000]">
	<node text="One" class="android.widget.TextView" package="p" enabled="true" bounds="[0,0][100,100]" />
	<node text="Two" class="android.widget.TextView" package="p" enabled="true" bounds="[0,100][100,200]" />
	</node></hierarchy>`
	tree, _ := parseUIDump([]byte(big))
	got := lines(convertTree(tree, 1000, 1000))
	if len(got) != 3 || got[0] != `[0] button @500,500` {
		t.Errorf("big container: %q", got)
	}
	// A clickable row with a child that repeats its description drops the child.
	dup := `<hierarchy rotation="0"><node text="" content-desc="Camera" class="android.widget.FrameLayout" package="p" clickable="true" enabled="true" bounds="[0,0][100,100]">
	<node text="Camera" class="android.widget.TextView" package="p" enabled="true" bounds="[0,60][100,100]" />
	</node></hierarchy>`
	tree, _ = parseUIDump([]byte(dup))
	got = lines(convertTree(tree, 1000, 1000))
	if len(got) != 1 || got[0] != `[0] button "Camera" @50,50` {
		t.Errorf("duplicate child: %q", got)
	}
	// Long labels are cut.
	long := `<hierarchy rotation="0"><node text="` + strings.Repeat("word ", 100) + `" class="android.widget.TextView" package="p" enabled="true" bounds="[0,0][100,100]" /></hierarchy>`
	tree, _ = parseUIDump([]byte(long))
	els := convertTree(tree, 1000, 1000)
	if n := len([]rune(els[0].Label)); n != maxLabelRunes || !strings.HasSuffix(els[0].Label, "\u2026") {
		t.Errorf("long label has %d runes: %q", n, els[0].Label)
	}
}

func TestRefsStayStable(t *testing.T) {
	// The same screen observed twice gets the same refs, since paths carry
	// no sibling indices.
	tree, _ := parseUIDump(fixture(settingsFixture))
	a := &screen.Screen{Elements: convertTree(tree, 1080, 2340)}
	b := &screen.Screen{Elements: convertTree(tree, 1080, 2340)}
	refs := screen.NewRefs()
	refs.Assign(a)
	refs.Assign(b)
	for i := range a.Elements {
		if a.Elements[i].Ref != b.Elements[i].Ref || a.Elements[i].Ref == 0 {
			t.Errorf("element %d: refs %d and %d", i, a.Elements[i].Ref, b.Elements[i].Ref)
		}
	}
}

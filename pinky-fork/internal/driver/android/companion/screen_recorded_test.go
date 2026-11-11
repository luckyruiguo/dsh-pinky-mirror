package companion

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

// Trees recorded on our Android 16 emulator with Gboard's stylus mode
// (testdata/README.md). Each screen lists what a person sees there and can
// tap: the keyboard's sheet and menus, and the app only where the
// keyboard's window lets touches through.

func recordedTree(t *testing.T, name string) *Tree {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "real-android16-emulator-gboard-stylus-"+name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var tr Tree
	if err := json.Unmarshal(data, &tr); err != nil {
		t.Fatal(err)
	}
	return &tr
}

func TestScreenRecordedStylusToolbar(t *testing.T) {
	tr := recordedTree(t, "toolbar")
	if ime := tr.Windows[1]; ime.Type != WindowInputMethod || len(ime.Region) != 2 || ime.Root != nil {
		t.Fatalf("keyboard window %+v", ime)
	}
	got := lines(tr.Screen(ScreenOptions{}))
	want := []string{
		"screen 1080x2400 · app: Settings Services (com.google.android.settings.intelligence) · portrait · keyboard shown · focus [2]",
		`[1] button "Back" @63,222`,
		`[2] input "display" @529,222 focused`,
		`[3] button "Clear text" @1006,222`,
		`[4] list @540,1360 scrollable`,
		`[5] button "Display size, Display & touch, Display size and text" @540,431`,
		`[6] button "Display & touch, Display & touch" @540,654`,
		`[7] button "Display size and text, Display & touch" @540,877`,
		// The keyboard's window spans these, but only its strip on the
		// left and the one at the bottom take touches.
		`[8] button "Display over other apps, Apps, Special app access" @540,1100`,
		`[9] button "Lock screen, Display & touch" @540,1323`,
		`[10] button "Bedtime mode, Digital Wellbeing > Bedtime mode" @540,1546`,
		`[11] button "Search Support for \"display\"" @540,1737`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("screen:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Where touches go to the keyboard or the status bar: the keyboard is
	// its window's larger strip, its other strip counts as covered, and
	// the window's empty middle, over the results, is neither.
	s := tr.Screen(ScreenOptions{})
	if want := (screen.Rect{X: 21, Y: 896, W: 148, H: 690}); s.KeyboardFrame != want {
		t.Errorf("keyboard frame %+v, want %+v", s.KeyboardFrame, want)
	}
	wantCovered := []screen.Rect{{W: 1080, H: 128}, {Y: 2337, W: 1080, H: 63}}
	if !slices.Equal(s.Covered, wantCovered) {
		t.Errorf("covered %+v, want %+v", s.Covered, wantCovered)
	}
}

func TestScreenRecordedStylusMenu(t *testing.T) {
	got := lines(recordedTree(t, "menu").Screen(ScreenOptions{}))
	want := []string{
		"screen 1080x2400 · app: Settings Services (com.google.android.settings.intelligence) · portrait · keyboard shown",
		`[1] button "Settings" @543,832`,
		`[2] button "Try handwriting demo" @543,969`,
		`[3] button "Show clipboard" @543,1106`,
		`[4] button "Show translate" @543,1243`,
		`[5] button "Show on-screen keyboard" @543,1380`,
		`[6] button "Switch to horizontal toolbar" @543,1517`,
		`[7] button "Use voice typing" @95,969`,
		`[8] button "Delete" @95,1105`,
		`[9] button "Search" @95,1241`,
		`[10] button "Show emoji keyboard" @95,1377`,
		`[11] button "More stylus options" @95,1513`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("screen:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestScreenRecordedStylusSheet is Gboard's "Try out your stylus" over the
// search results: the screen is the sheet's, as the person sees it. The
// app sends none of its nodes under the sheet; when its nodes do come (an
// older Android, or an app that walks hidden nodes), what lies under the
// sheet is left out all the same.
func TestScreenRecordedStylusSheet(t *testing.T) {
	want := []string{
		"screen 1080x2400 · app: Settings Services (com.google.android.settings.intelligence) · portrait · keyboard shown · focus [8]",
		`[1] text "Try out your stylus" @540,1252`,
		`[2] list @540,1419 scrollable`,
		`[3] button "Write" @210,1419 disabled`,
		`[4] button "Delete" @442,1419`,
		`[5] button "Select" @674,1419`,
		`[6] button "Insert" @888,1419`,
		`[7] text "Use your stylus and your handwriting is automatically converted to text" @540,1596`,
		`[8] input @540,1902 focused`,
		`[9] button "Reset" @220,2274`,
		`[10] button "Cancel" @599,2274`,
		`[11] button "Next" @859,2274`,
	}
	tr := recordedTree(t, "sheet")
	if app := tr.Windows[2]; app.Type != WindowApplication || app.Root != nil {
		t.Fatalf("app window %+v", app)
	}
	got := lines(tr.Screen(ScreenOptions{}))
	if !slices.Equal(got, want) {
		t.Fatalf("screen:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// The search page as the toolbar's tree has it, every node visible,
	// under the sheet.
	tr.Windows[2].Root = recordedTree(t, "toolbar").Windows[2].Root
	got = lines(tr.Screen(ScreenOptions{}))
	if !slices.Equal(got, want) {
		t.Fatalf("with the app's nodes under the sheet:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

//go:build device

package gateway

// Real-phone tests: the gateway in-process with the real provider, driven
// through a Session the way an agent would, approvals answered with Decide.
// They use only the home screen, the Calculator and Settings (read-only:
// every approval for a setting is denied, and guardDevice refuses taps on
// switches whatever the gateway does). They never lock or unlock a phone,
// and stop without input if the phone shows anything unexpected.
//
// Run one phone at a time, holding its lock only for the run:
//
//	go test -c -tags device -o /tmp/pinky-gw/gateway.test ./internal/gateway
//	scripts/devlock.sh 3e8be48e env PINKY_DEVICE=3e8be48e /tmp/pinky-gw/gateway.test -test.run TestDevice -test.v
//
// Screenshots go to a temporary folder under /tmp/pinky-gw-*, removed at
// the end. With PINKY_RECORD=1 the task is also recorded (into the same
// folder), and the recording is checked with ffprobe.

import (
	"context"
	"fmt"
	"image/jpeg"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android"
	"github.com/DIYgod/pinky/internal/driver/ios"
	"github.com/DIYgod/pinky/internal/screen"
)

// Settings rows the tests may open, in English and Chinese.
var navRows = []string{
	"Battery", "电池", "Display", "显示", "Storage", "存储", "General", "通用",
	"Display & Brightness", "显示与亮度", "Sound & vibration", "提示音和振动", "Sounds & Haptics", "声音与触感",
	// The top of a small iPhone's list, under its banners.
	"Bluetooth", "蓝牙",
}

func isIOSID(id string) bool { return strings.Contains(id, "-") && len(id) >= 24 }

type deviceRun struct {
	t     *testing.T
	g     *Gateway
	s     *Session
	prov  *guardProvider
	id    string
	acts  []actTiming
	diffs int
	fulls int
}

type actTiming struct {
	what   string
	tool   string
	took   time.Duration
	status string
	settle string
}

func (r *deviceRun) guard() *guardDevice { return r.prov.Guard() }

// act runs one action, paced like a person, and stops the test on a guard
// refusal or an unexpected app.
func (r *deviceRun) act(what string, a Action) *Result {
	r.t.Helper()
	time.Sleep(400 * time.Millisecond)
	start := time.Now()
	res := r.s.Act(context.Background(), Call{Device: r.id}, a)
	took := time.Since(start)
	r.acts = append(r.acts, actTiming{what: what, tool: a.Tool, took: took, status: string(res.Status) + string(res.Error), settle: res.Settle})
	if res.Screen != nil {
		if res.Full {
			r.fulls++
		} else {
			r.diffs++
		}
	}
	r.t.Logf("%-28s %-18s %6.2fs %s", what, string(res.Status)+string(res.Error), took.Seconds(), res.Settle)
	r.check(res)
	return res
}

func (r *deviceRun) check(res *Result) {
	r.t.Helper()
	if ref := r.guard().Refused(); len(ref) > 0 {
		r.t.Fatalf("the guard refused input the gateway sent: %q", ref)
	}
	if res.Screen != nil && !res.Screen.Locked && res.Rule == "" && !allowedApp(res.Screen.App) {
		r.t.Fatalf("unexpected app in front (%s); leaving the phone alone", res.Screen.App.ID)
	}
}

func (r *deviceRun) observe() *Result {
	r.t.Helper()
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	res := r.s.Observe(context.Background(), ObserveRequest{Call: Call{Device: r.id}})
	r.t.Logf("%-28s %-18s %6.2fs (%d elements)", "observe", res.Status, time.Since(start).Seconds(), len(screenOf(res).Elements))
	if res.IsError() {
		r.t.Fatalf("observe: %s %s", res.Error, res.Message)
	}
	r.check(res)
	return res
}

func screenOf(res *Result) *screen.Screen {
	if res.Screen == nil {
		return &screen.Screen{}
	}
	return res.Screen
}

// find returns the first element whose label equals one of the names,
// ignoring case.
func find(s *screen.Screen, names ...string) (screen.Element, bool) {
	for _, n := range names {
		for _, e := range s.Elements {
			if e.Ref != 0 && !e.Bounds.Empty() && strings.EqualFold(strings.TrimSpace(e.Label), n) {
				return e, true
			}
		}
	}
	return screen.Element{}, false
}

func (r *deviceRun) launch(names ...string) *Result {
	r.t.Helper()
	apps := r.s.Apps(context.Background(), AppsRequest{Call: Call{Device: r.id}})
	for _, n := range names {
		for _, a := range apps.Apps {
			if a.ID == n || strings.EqualFold(a.Name, n) {
				return r.act("launch "+a.ID, Action{Tool: "launch", App: a.ID})
			}
		}
	}
	r.t.Fatalf("none of %v is installed", names)
	return nil
}

func TestDevice(t *testing.T) {
	id := os.Getenv("PINKY_DEVICE")
	if id == "" {
		t.Skip("set PINKY_DEVICE to a phone id and run through scripts/devlock.sh")
	}
	data, err := os.MkdirTemp("/tmp", "pinky-gw-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(data)

	var inner driver.Provider
	if isIOSID(id) {
		inner = ios.NewProvider()
	} else {
		inner = android.NewProvider(android.Config{})
	}
	prov := &guardProvider{Provider: inner, id: id, navRows: navRows}
	log := slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	// Preset 1 (testPolicy): the run checks that a switch in Settings asks.
	g, err := New(Options{Providers: []driver.Provider{prov}, DataDir: data, TrustNewAgents: true, Policy: testPolicy(t),
		ApprovalTimeout: 60 * time.Second, MaxBlock: 30 * time.Second, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	record := os.Getenv("PINKY_RECORD") != ""
	g.SetRecording(record)

	deadline := time.Now().Add(60 * time.Second)
	for !deviceOnline(g, id) {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not come online: %+v", id, g.Devices())
		}
		time.Sleep(500 * time.Millisecond)
	}
	r := &deviceRun{t: t, g: g, prov: prov, id: id}
	lockStart := time.Now()
	locked, err := prov.Guard().Locked(context.Background())
	t.Logf("lock state: locked=%v err=%v in %.0f ms", locked, err, float64(time.Since(lockStart).Microseconds())/1000)
	if locked {
		t.Skip("the phone is locked; left as it is")
	}
	r.s = g.OpenSession(AgentInfo{Name: "Pinky device test"})
	defer r.s.Close()

	start := time.Now()
	res := r.s.Connect(context.Background(), ConnectRequest{Call: Call{Device: id}, Task: "Device test: Calculator and Settings"})
	t.Logf("connect: %s %s in %.2fs, %s, %d elements", res.Status, res.Error, time.Since(start).Seconds(), screenOf(res).App.ID, len(screenOf(res).Elements))
	switch {
	case res.Status == StatusLocked || screenOf(res).Locked:
		t.Skip("the phone is locked; left as it is")
	case res.IsError() || res.Status != StatusSettled:
		t.Fatalf("connect: %+v", res)
	case !allowedApp(screenOf(res).App):
		t.Fatalf("the phone shows %s, not the home screen, Settings or the Calculator; left as it is", screenOf(res).App.ID)
	}

	task := taskID(t, g, r.s)
	t.Run("Calculator", r.calculator)
	t.Run("Settings", r.settings)
	t.Run("RefsStable", r.refsStable)
	t.Run("TakeOverHandBackStop", r.takeOverAndStop)
	t.Run("History", r.history)
	r.report()
	if record {
		r.checkRecording(t, task)
	}
}

// checkRecording checks the task's recording with ffprobe: the phone's
// codec and size, and frames from the whole task.
func (r *deviceRun) checkRecording(t *testing.T, task string) {
	g := r.g
	g.Flush()
	rec, err := g.Recording(context.Background(), task)
	if err != nil {
		t.Fatalf("recording: %v", err)
	}
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-count_frames",
		"-show_entries", "stream=codec_name,width,height,nb_read_frames", "-of", "csv=p=0", rec.Path).CombinedOutput()
	fi, _ := os.Stat(rec.Path)
	var size int64
	if fi != nil {
		size = fi.Size()
	}
	t.Logf("recording: %s, %v long, %d marks, %d KB; ffprobe: %s %v", rec.Path, rec.Length.Round(time.Millisecond),
		len(rec.Marks), size/1000, strings.TrimSpace(string(out)), err)
	if err != nil {
		t.Errorf("ffprobe: %v", err)
	}
	if rec.Length < 5*time.Second || len(rec.Marks) < 5 {
		t.Errorf("a short recording: %v, %d marks", rec.Length, len(rec.Marks))
	}
}

func deviceOnline(g *Gateway, id string) bool {
	for _, d := range g.Devices() {
		if d.Info.ID == id && d.Status != DeviceOffline {
			return true
		}
	}
	return false
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func (r *deviceRun) calculator(t *testing.T) {
	r.t = t
	res := r.launch("com.android.calculator2", "com.google.android.calculator", "com.apple.calculator", "Calculator", "计算器")
	if res.Status != StatusSettled {
		t.Fatalf("launch: %+v", res)
	}
	cur := r.observe()
	// "=" first ends whatever the display held, so 6 starts a new number.
	equals := []string{"=", "equals", "Equals", "等于"}
	keys := [][]string{
		equals,
		{"6"},
		{"×", "*", "x", "multiply", "Multiply", "乘", "乘以"},
		{"7"},
		equals,
	}
	for _, names := range keys {
		var res *Result
		if e, ok := find(cur.Screen, names...); ok {
			res = r.act("tap "+e.Label, Action{Tool: "tap", Ref: e.Ref})
		} else if p, ok := calcKey(cur.Screen, names[0]); ok {
			// iOS places elements by OCR, so keys that show a symbol (×, =)
			// have no position: tap where the digit keys say they are.
			res = r.act(fmt.Sprintf("tap %s at %d,%d", names[0], p.X, p.Y), Action{Tool: "tap", Point: &p})
		} else {
			t.Fatalf("no %v key on the Calculator:\n%s", names, cur.Screen.Render())
		}
		if res.Status != StatusSettled {
			t.Fatalf("tap %v: %+v", names, res)
		}
		cur = r.observe()
	}
	found := false
	for _, e := range cur.Screen.Elements {
		l := strings.TrimSpace(e.Label)
		if l == "42" || strings.HasSuffix(l, "= 42") || strings.HasSuffix(l, ", 42") || strings.HasPrefix(l, "42,") ||
			strings.HasSuffix(l, "value:42") {
			found = true
		}
	}
	if !found {
		t.Errorf("42 is not on the screen:\n%s", cur.Screen.Render())
	}

	// The same keys as one phone_batch: steps after the first start from
	// the read that ended the step before.
	var steps []Action
	for _, names := range keys {
		if e, ok := find(cur.Screen, names...); ok {
			steps = append(steps, Action{Tool: "tap", Ref: e.Ref})
		} else if p, ok := calcKey(cur.Screen, names[0]); ok {
			steps = append(steps, Action{Tool: "tap", Point: &p})
		}
	}
	time.Sleep(400 * time.Millisecond)
	start := time.Now()
	res = r.s.Batch(context.Background(), BatchRequest{Call: Call{Device: r.id}, Steps: steps})
	took := time.Since(start)
	t.Logf("batch of %d taps: %s in %.2fs (%.2fs a step)", len(steps), res.Status, took.Seconds(), took.Seconds()/float64(len(steps)))
	r.check(res)
	if res.Status != StatusSettled || len(res.Steps) != len(steps) {
		t.Fatalf("batch: %+v", res)
	}
	r.act("home", Action{Tool: "key", Key: "home"})
}

// calcKey finds a key of a standard portrait calculator from whichever
// digit keys have a position: digits 1 to 9 sit in a 3x3 grid (7 8 9 on
// top), × ends the top row and = ends the row three rows below it.
func calcKey(s *screen.Screen, key string) (screen.Point, bool) {
	colX := map[int][]int{}
	rowY := map[int][]int{}
	for d := 1; d <= 9; d++ {
		e, ok := find(s, fmt.Sprint(d))
		if !ok {
			continue
		}
		c := e.Bounds.Center()
		col, row := (d-1)%3, 2-(d-1)/3
		colX[col] = append(colX[col], c.X)
		rowY[row] = append(rowY[row], c.Y)
	}
	avg := func(v []int) int {
		sum := 0
		for _, x := range v {
			sum += x
		}
		return sum / len(v)
	}
	step := func(m map[int][]int) (int, int, bool) {
		// The position of index 0 and the spacing, from any two indexes.
		var idx []int
		for i := range m {
			idx = append(idx, i)
		}
		if len(idx) < 2 {
			return 0, 0, false
		}
		sort.Ints(idx)
		a, b := idx[0], idx[len(idx)-1]
		sp := (avg(m[b]) - avg(m[a])) / (b - a)
		return avg(m[a]) - a*sp, sp, true
	}
	x0, dx, okX := step(colX)
	y0, dy, okY := step(rowY)
	if !okX || !okY {
		return screen.Point{}, false
	}
	switch key {
	case "×":
		return screen.Point{X: x0 + 3*dx, Y: y0}, true
	case "=":
		return screen.Point{X: x0 + 3*dx, Y: y0 + 3*dy}, true
	case "6":
		return screen.Point{X: x0 + 2*dx, Y: y0 + dy}, true
	case "7":
		return screen.Point{X: x0, Y: y0}, true
	}
	return screen.Point{}, false
}

func (r *deviceRun) settings(t *testing.T) {
	r.t = t
	res := r.launch("com.android.settings", "com.apple.Preferences", "Settings", "设置")
	if res.Status != StatusSettled || !isSettings(screenOf(res).App) {
		t.Fatalf("launch Settings: %+v", res)
	}
	top := r.observe()
	// Settings may resume on a sub-page: go back to its main page, which
	// has the search field, without leaving Settings.
	for i := 0; i < 3 && !hasSearch(top.Screen); i++ {
		res = r.act("back to the main page", Action{Tool: "key", Key: "back"})
		if !isSettings(screenOf(res).App) {
			res = r.launch("com.android.settings", "com.apple.Preferences")
		}
		top = r.observe()
	}
	t.Logf("Settings: %d elements, roles %v", len(top.Screen.Elements), roles(top.Screen))
	switchChecked := false
	if sw, ok := firstToggle(top.Screen); ok {
		r.denySwitch(t, sw)
		switchChecked = true
	}

	// Find an allowed row: Settings may open where it was left, so look at
	// this screen, then scroll towards the top, then down. Read until two
	// reads agree each time: on the iPhone, rows lost their positions while
	// the list still moved.
	cur := r.waitStill()
	visible := func(s *screen.Screen) bool {
		for _, e := range s.Elements {
			c := e.Bounds.Center()
			if !e.Bounds.Empty() && e.Role == screen.RoleButton && matchesRow(e, navRows) && c.Y > s.Height*12/100 && c.Y < s.Height*88/100 {
				return true
			}
		}
		return false
	}
	for _, dir := range []string{"down", "down", "up", "up", "up"} {
		if visible(cur.Screen) {
			break
		}
		res = r.act("swipe "+dir, Action{Tool: "swipe", Direction: dir})
		if res.Status != StatusSettled {
			t.Fatalf("scroll %s: %+v", dir, res)
		}
		cur = r.waitStill()
	}

	// Open a sub-page, Battery first: it has a switch for the next check.
	// Only rows in the middle of the screen. On iOS, positions come from OCR
	// and a row placed in one read may have none in the next (not_found):
	// then read again and try the next row.
	mid := func(s *screen.Screen, e screen.Element) bool {
		c := e.Bounds.Center()
		return c.Y > s.Height*12/100 && c.Y < s.Height*88/100
	}
	logRows := func(s *screen.Screen) {
		var out []string
		for _, e := range s.Elements {
			if matchesRow(e, navRows) {
				out = append(out, fmt.Sprintf("[%d] %s %q %v", e.Ref, e.Role, e.Label, e.Bounds))
			}
		}
		t.Logf("allowed rows in this read: %v", out)
		var placed []string
		for _, e := range s.Elements {
			if !e.Bounds.Empty() && e.Source != screen.SourceOCR {
				name, _, _ := strings.Cut(e.Label, ", ")
				placed = append(placed, fmt.Sprintf("%s@%d", name, e.Bounds.Center().Y))
			}
		}
		t.Logf("placed elements: %v", placed)
	}
	logRows(cur.Screen)
	tried := map[string]bool{}
	opened := false
	for attempt := 0; attempt < 4 && !opened; attempt++ {
		var row screen.Element
		ok := false
		for _, want := range navRows {
			for _, e := range cur.Screen.Elements {
				if !ok && !tried[e.Label] && e.Role == screen.RoleButton && e.Ref != 0 && !e.Bounds.Empty() && mid(cur.Screen, e) && matchesRow(e, []string{want}) {
					row, ok = e, true
				}
			}
		}
		if !ok {
			t.Fatalf("none of the allowed rows is on the screen (roles %v)", roles(cur.Screen))
		}
		res = r.act("open "+strings.SplitN(row.Label, " · ", 2)[0], Action{Tool: "tap", Ref: row.Ref})
		switch {
		case res.Error == ErrNotFound:
			t.Logf("%s had a position in one read and none in the next; reading again. Last reads: %q", row.Label, r.guard().lastStats(4))
			tried[row.Label] = true
			cur = r.observe()
			logRows(cur.Screen)
		case res.Status != StatusSettled || !res.Full && len(res.Diff) == 0:
			t.Fatalf("open the row: %+v", res)
		default:
			opened = true
		}
	}
	if !opened {
		t.Fatal("could not open a Settings row")
	}
	page := r.observe()

	// A switch asks the person, who says no: nothing changes.
	if !switchChecked {
		sw, found := firstToggle(page.Screen)
		if !found {
			res = r.act("swipe up", Action{Tool: "swipe", Direction: "up"})
			page = r.observe()
			sw, found = firstToggle(page.Screen)
		}
		if found {
			r.denySwitch(t, sw)
		} else {
			t.Errorf("no switch on Settings or the sub-page (roles %v); the switch check did not run", roles(page.Screen))
		}
	}

	res = r.act("back", Action{Tool: "key", Key: "back"})
	if res.Status != StatusSettled {
		t.Fatalf("back: %+v", res)
	}

	// Scroll down and back up.
	res = r.act("swipe up", Action{Tool: "swipe", Direction: "up"})
	if res.Status != StatusSettled {
		t.Fatalf("scroll: %+v", res)
	}
	res = r.act("swipe down", Action{Tool: "swipe", Direction: "down"})
	if res.Status != StatusSettled {
		t.Fatalf("scroll back: %+v", res)
	}
	r.act("home", Action{Tool: "key", Key: "home"})
}

// waitStill reads the screen until two reads in a row agree, at most six
// times, and logs how many it took.
func (r *deviceRun) waitStill() *Result {
	r.t.Helper()
	prev := r.observe()
	for i := 1; i < 6; i++ {
		cur := r.observe()
		if body(cur.Screen) == body(prev.Screen) {
			r.t.Logf("the screen was still after %d reads", i+1)
			return cur
		}
		prev = cur
	}
	r.t.Logf("the screen kept changing over six reads")
	return prev
}

// hasSearch reports whether Settings shows its main page, by its search
// field.
func hasSearch(s *screen.Screen) bool {
	for _, e := range s.Elements {
		l := strings.ToLower(e.Label)
		if strings.Contains(l, "search") || strings.Contains(e.Label, "搜索") {
			return true
		}
	}
	return false
}

func firstToggle(s *screen.Screen) (screen.Element, bool) {
	for _, e := range s.Elements {
		if (e.Role == screen.RoleSwitch || e.Role == screen.RoleCheckbox) && e.Ref != 0 && !e.Bounds.Empty() {
			return e, true
		}
	}
	return screen.Element{}, false
}

func (r *deviceRun) denySwitch(t *testing.T, sw screen.Element) {
	before := sw.Has(screen.StateChecked)
	// Only input from here on counts: an earlier subtest's tap (a
	// Calculator key) can fall inside where the switch is now.
	earlier := len(r.guard().Inputs())
	done := make(chan *Result, 1)
	start := time.Now()
	go func() {
		done <- r.s.Act(context.Background(), Call{Device: r.id, Intent: "device test: must be denied"}, Action{Tool: "tap", Ref: sw.Ref})
	}()
	var need *NeedView
	deadline := time.Now().Add(30 * time.Second)
	for need == nil && time.Now().Before(deadline) {
		for _, n := range r.g.Needs() {
			if n.Kind == NeedApproval {
				n := n
				need = &n
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if need == nil {
		res := <-done
		t.Fatalf("tapping a switch in Settings did not ask: %+v (guard refused %q)", res, r.guard().Refused())
	}
	t.Logf("switch asked after %.2fs: rule %q, title %q, screenshot %v", time.Since(start).Seconds(), need.Rule, need.Title, need.Screenshot != nil)
	if need.Rule != "Settings changes" {
		t.Errorf("rule %q, want Settings changes", need.Rule)
	}
	if err := r.g.Decide(need.ID, Deny, "device test"); err != nil {
		t.Fatal(err)
	}
	res := <-done
	if res.Status != StatusDenied {
		t.Fatalf("denied switch: %+v", res)
	}
	page := r.observe()
	// Compare the same element: same ref, or else the same role and label.
	var same []screen.Element
	for _, e := range page.Screen.Elements {
		if e.Ref == sw.Ref || (e.Role == sw.Role && e.Label == sw.Label) {
			same = append(same, e)
		}
	}
	var lines []string
	for _, e := range same {
		lines = append(lines, e.Line())
	}
	t.Logf("switch before: %s; after: %v", sw.Line(), lines)
	for _, e := range same {
		if e.Has(screen.StateChecked) != before {
			t.Fatalf("the switch changed after Deny: %s, then %s", sw.Line(), e.Line())
		}
	}
	inputs := r.guard().Inputs()
	for _, in := range inputs[earlier:] {
		if strings.HasPrefix(in, "tap ") && sw.Bounds.Contains(parsePoint(in)) {
			t.Fatalf("a tap reached the switch: %s (input since the approval: %q)", in, inputs[earlier:])
		}
	}
}

func parsePoint(in string) screen.Point {
	var p screen.Point
	fmt.Sscanf(strings.TrimPrefix(in, "tap "), "%d,%d", &p.X, &p.Y)
	return p
}

func roles(s *screen.Screen) map[string]int {
	m := map[string]int{}
	for _, e := range s.Elements {
		m[e.Role]++
	}
	return m
}

// refsStable reads the same screen twice and compares refs.
func (r *deviceRun) refsStable(t *testing.T) {
	r.t = t
	a := r.observe()
	b := r.observe()
	refs := map[string]int{}
	for _, e := range a.Screen.Elements {
		if e.Ref != 0 {
			refs[e.Role+"\x00"+e.Label] = e.Ref
		}
	}
	same, moved := 0, 0
	for _, e := range b.Screen.Elements {
		if want, ok := refs[e.Role+"\x00"+e.Label]; ok {
			if want == e.Ref {
				same++
			} else {
				moved++
			}
		}
	}
	t.Logf("refs on two reads of the home screen: %d kept, %d changed", same, moved)
	if moved > 0 {
		t.Errorf("%d refs changed between two reads of the same screen", moved)
	}
}

func (r *deviceRun) takeOverAndStop(t *testing.T) {
	r.t = t
	if err := r.g.TakeOver(r.id); err != nil {
		t.Fatal(err)
	}
	res := r.act("home after take over", Action{Tool: "key", Key: "home"})
	if res.Status != StatusTookOver {
		t.Fatalf("after TakeOver: %+v", res)
	}
	if res = r.act("home while paused", Action{Tool: "key", Key: "home"}); res.Status != StatusPaused {
		t.Fatalf("paused: %+v", res)
	}
	r.g.HandBack(r.id, "device test")
	if res = r.act("home after hand back", Action{Tool: "key", Key: "home"}); res.Status != StatusSettled {
		t.Fatalf("after HandBack: %+v", res)
	}

	// Stop while an action is still reading the screen: nothing reaches the
	// phone. The guard holds the read, since a read can take 50 ms.
	before := len(r.guard().Inputs())
	done := make(chan *Result, 1)
	r.guard().setHoldRead(time.Second)
	go func() { done <- r.s.Act(context.Background(), Call{Device: r.id}, Action{Tool: "key", Key: "home"}) }()
	time.Sleep(200 * time.Millisecond)
	if err := r.g.Stop(r.id); err != nil {
		t.Fatal(err)
	}
	res = <-done
	r.guard().setHoldRead(0)
	t.Logf("stop mid-action: %s %q", res.Status, res.Message)
	if res.Status != StatusStopped {
		t.Fatalf("stop: %+v", res)
	}
	if n := len(r.guard().Inputs()); n != before {
		t.Errorf("input reached the phone after Stop: %v", r.guard().Inputs()[before:])
	}
	if res := r.s.Connect(context.Background(), ConnectRequest{Call: Call{Device: r.id}, Task: "again"}); res.Status != StatusStopped {
		t.Errorf("the stopped session reconnected: %+v", res)
	}
}

func (r *deviceRun) history(t *testing.T) {
	r.g.Flush()
	tasks, err := r.g.History(context.Background(), HistoryQuery{})
	if err != nil || len(tasks) == 0 {
		t.Fatalf("history: %v %v", tasks, err)
	}
	shots, steps := 0, 0
	for _, st := range tasks[0].Steps {
		steps++
		for _, id := range []string{st.Before, st.After} {
			if id == "" {
				continue
			}
			p, err := r.g.MediaPath(context.Background(), id)
			if err != nil {
				t.Errorf("step %d media %s: %v", st.Index, id, err)
				continue
			}
			f, err := os.Open(p)
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := jpeg.DecodeConfig(f)
			f.Close()
			if err != nil || cfg.Width == 0 {
				t.Errorf("step %d media %s: %v", st.Index, id, err)
			}
			shots++
		}
	}
	acts, befores, afters := 0, 0, 0
	for _, st := range tasks[0].Steps {
		switch st.Tool {
		case "phone_tap", "phone_swipe", "phone_key", "phone_launch":
			if st.Status != string(StatusSettled) {
				continue
			}
			acts++
			if st.Before != "" {
				befores++
			}
			if st.After != "" {
				afters++
			}
		}
	}
	t.Logf("history: task %q %s, %d steps, %d screenshot references; %d settled actions, %d with a before picture, %d with an after picture",
		tasks[0].Title, tasks[0].State, steps, shots, acts, befores, afters)
	if shots == 0 {
		t.Error("no screenshots in history")
	}
}

func (r *deviceRun) report() {
	t := r.t
	g := r.guard()
	g.mu.Lock()
	defer g.mu.Unlock()
	var lines []string
	for what, ds := range g.timings {
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		lines = append(lines, fmt.Sprintf("%-12s n=%-3d min %.2fs median %.2fs max %.2fs", what, len(ds),
			ds[0].Seconds(), ds[len(ds)/2].Seconds(), ds[len(ds)-1].Seconds()))
	}
	sort.Strings(lines)
	// What the agent waits for, per tool: settled actions only, so an
	// approval's wait does not count.
	byTool := map[string][]time.Duration{}
	for _, a := range r.acts {
		if a.status == string(StatusSettled) {
			byTool["action"] = append(byTool["action"], a.took)
			byTool["action "+a.tool] = append(byTool["action "+a.tool], a.took)
		}
	}
	var kinds []string
	for k := range byTool {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		ds := byTool[k]
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		var sum time.Duration
		for _, d := range ds {
			sum += d
		}
		lines = append(lines, fmt.Sprintf("%-12s n=%-3d min %.2fs median %.2fs mean %.2fs max %.2fs", k, len(ds),
			ds[0].Seconds(), ds[len(ds)/2].Seconds(), (sum/time.Duration(len(ds))).Seconds(), ds[len(ds)-1].Seconds()))
	}
	lines = append(lines, fmt.Sprintf("settled %d, settle timeouts %d; results as diffs %d, whole screens %d; inputs %d, refused %d",
		g.settleOK, g.settleTO, r.diffs, r.fulls, len(g.inputs), len(g.refused)))
	t.Logf("timings on %s:\n%s", r.id, strings.Join(lines, "\n"))
}

package telemetry

import (
	"context"
	"encoding/json"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/mcp"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// views is a gateway.API whose views a test sets.
type views struct {
	mu      sync.Mutex
	devices []gateway.DeviceView
	tasks   []gateway.TaskView
	needs   []gateway.NeedView
	events  chan gateway.Event
}

func (v *views) Devices() []gateway.DeviceView {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.devices
}

func (v *views) Tasks() []gateway.TaskView {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.tasks
}

func (v *views) Needs() []gateway.NeedView {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.needs
}

func (v *views) set(fn func()) {
	v.mu.Lock()
	fn()
	v.mu.Unlock()
	v.events <- gateway.Event{Kind: gateway.EventTasks}
}

func (v *views) Subscribe(ctx context.Context) <-chan gateway.Event { return v.events }
func (v *views) History(context.Context, gateway.HistoryQuery) ([]gateway.TaskView, error) {
	return nil, nil
}
func (v *views) Decide(string, gateway.Decision, string) error { return nil }
func (v *views) Unlock(string) error                           { return nil }
func (v *views) TakeOver(string) error                         { return nil }
func (v *views) HandBack(string, string) error                 { return nil }
func (v *views) Stop(string) error                             { return nil }
func (v *views) StopAll() error                                { return nil }
func (v *views) Stream(context.Context, string, driver.StreamOptions) (driver.Stream, error) {
	return nil, driver.ErrUnsupported
}
func (v *views) Touch(string, driver.TouchAction, screen.Point) error { return nil }
func (v *views) Key(string, driver.Key) error                         { return nil }
func (v *views) Text(string, string) error                            { return nil }

// waitCount waits until a counter reaches want.
func waitCount(t *testing.T, c *Client, name string, want int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.Report().Counts[name] >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s = %d, want %d; counts %v", name, c.Report().Counts[name], want, c.Report().Counts)
}

// TestObserve checks what one observer counts from the views, reading
// them itself, as Observe does when they change. (Reading them in the test
// keeps it from looking at the counts halfway through a read.)
func TestObserve(t *testing.T) {
	c := openTest(t, t.TempDir(), &fakeBackend{}, newClock(), true)
	v := &views{events: make(chan gateway.Event, 16)}
	pixel := driver.Info{ID: "FA6C20303860", Platform: driver.Android, Name: "Pixel", Model: "Pixel 4", OSVersion: "10",
		Connection: "usb", Backend: "scrcpy", State: driver.StateOnline}
	mi := driver.Info{ID: "adb-3e8be48e-kIY2HE._adb-tls-connect._tcp", Platform: driver.Android, Name: "Mi 10 Pro",
		OSVersion: "17", Connection: "wifi", Backend: "scrcpy", State: driver.StateOnline}
	iphone := driver.Info{ID: "00008110-001E30C9262A801E", Platform: driver.IOS, Name: "Ada’s iPhone", OSVersion: "27.1",
		Connection: "usb", State: driver.StateNeedsSetup}
	v.devices = []gateway.DeviceView{{Info: pixel, Status: gateway.DeviceIdle}, {Info: mi, Status: gateway.DeviceIdle},
		{Info: iphone, Status: gateway.DeviceIdle}}
	o := newObserver(c)
	o.read(v)

	v.devices[1].StatusLine = "Claude Code · Reorder lunch · in the background"
	v.tasks = []gateway.TaskView{
		{ID: "t1", DeviceID: mi.ID, Agent: "Claude Code", Title: "Reorder lunch", State: gateway.TaskWorking},
		{ID: "t2", DeviceID: pixel.ID, Agent: "Ada's script (remote)", Title: "Check mail", State: gateway.TaskWorking, Ended: time.Now()},
	}
	v.needs = []gateway.NeedView{
		{ID: "ap_1", Kind: gateway.NeedApproval, Rule: "Payments", Title: "Pay $12.80?"},
		{ID: "ap_2", Kind: gateway.NeedApproval, Rule: "My bank rule"},
		{ID: "ap_3", Kind: gateway.NeedApproval, Rule: "New agent"},
	}
	o.read(v)
	o.read(v) // nothing new: nothing counts twice

	c.markDecided("ap_1") // the person answered it in Pinky (Gateway.Decide)
	v.tasks[0].State = gateway.TaskDone
	v.tasks[0].Ended = time.Now()
	v.tasks[1].State = gateway.TaskFailed
	v.needs = nil
	o.read(v)

	got := c.Report().Counts
	want := map[string]int64{
		"phones.android.usb": 1, "phones.android.wifi": 1, "phones.android.os.10": 1, "phones.android.os.17": 1,
		"phones.android.backend.scrcpy": 2, "phones.ios.state.needs_setup": 1, "phones.max_online": 2,
		"runs.started": 2, "runs.agent.claude_code": 1, "runs.agent.other": 1, "runs.remote": 1,
		"runs.background": 1, "runs.done": 1, "runs.failed": 1,
		"needs.approval.asked": 2, "needs.new_agent.asked": 1, "needs.approval.rule.payments": 1,
		"needs.approval.rule.custom": 1, "needs.approval.unanswered": 1, "needs.new_agent.unanswered": 1,
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("%s = %d, want %d", k, got[k], n)
		}
	}
	if len(got) != len(want) {
		t.Errorf("counted %v, want only %v", got, want)
	}
	// Nothing about the phones or tasks themselves. The random install id
	// is left out of the search: its hex may spell "ada" or "e48e".
	r := c.Report()
	r.InstallID = ""
	data, _ := json.Marshal(r)
	for _, s := range []string{"FA6C2030", "3e8be48e", "001E30C9", "Ada", "lunch", "$12.80", "my bank", "check mail"} {
		if strings.Contains(strings.ToLower(string(data)), strings.ToLower(s)) {
			t.Errorf("the report has %q: %s", s, data)
		}
	}
}

// TestObserveFollowsEvents: Observe reads the views at the start and after
// changes, and stops with its context.
func TestObserveFollowsEvents(t *testing.T) {
	old := observeEvery
	observeEvery = 5 * time.Millisecond
	t.Cleanup(func() { observeEvery = old })

	c := openTest(t, t.TempDir(), &fakeBackend{}, newClock(), true)
	v := &views{events: make(chan gateway.Event, 16)}
	v.devices = []gateway.DeviceView{{Info: driver.Info{ID: "FA6C20303860", Platform: driver.Android, Connection: "usb",
		State: driver.StateOnline}, Status: gateway.DeviceIdle}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.Observe(ctx, v)
		close(done)
	}()
	waitCount(t, c, "phones.android.usb", 1)
	// Asked needs are the last thing a read counts, so once this one is,
	// that read is over.
	v.set(func() { v.needs = []gateway.NeedView{{ID: "ap_1", Kind: gateway.NeedApproval, Rule: "Payments"}} })
	waitCount(t, c, "needs.approval.asked", 1)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Observe did not stop with its context")
	}
}

// TestGatewayKeepsMethods: the client finds the gateway's extras with
// type assertions, so the wrapper must keep every method of
// *gateway.Gateway with the same signature.
func TestGatewayKeepsMethods(t *testing.T) {
	gt := reflect.TypeOf((*gateway.Gateway)(nil))
	wt := reflect.TypeOf((*Gateway)(nil))
	for i := 0; i < gt.NumMethod(); i++ {
		m := gt.Method(i)
		wm, ok := wt.MethodByName(m.Name)
		if !ok {
			t.Errorf("Gateway lacks %s", m.Name)
			continue
		}
		if !sameSignature(m.Type, wm.Type) {
			t.Errorf("%s: gateway %v, wrapper %v", m.Name, m.Type, wm.Type)
		}
	}
}

// sameSignature compares two method types without their receivers.
func sameSignature(a, b reflect.Type) bool {
	if a.NumIn() != b.NumIn() || a.NumOut() != b.NumOut() || a.IsVariadic() != b.IsVariadic() {
		return false
	}
	for i := 1; i < a.NumIn(); i++ {
		if a.In(i) != b.In(i) {
			return false
		}
	}
	for i := 0; i < a.NumOut(); i++ {
		if a.Out(i) != b.Out(i) {
			return false
		}
	}
	return true
}

// TestCountsAgentsAndPerson runs a real gateway on a demo phone with the
// MCP server, as Pinky does: an agent pays, the person denies through the
// wrapped gateway, and the counters say so, by name only.
func TestCountsAgentsAndPerson(t *testing.T) {
	old := observeEvery
	observeEvery = 5 * time.Millisecond
	t.Cleanup(func() { observeEvery = old })

	dev := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	pol := policy.Default()
	for cat, e := range demo.PolicyApps() {
		pol.AddApps(cat, e)
	}
	g, err := gateway.New(gateway.Options{Providers: []driver.Provider{demo.NewProvider(driver.Android, dev)}, Policy: pol,
		TrustNewAgents: true, NoScreenshots: true, SettleMax: 200 * time.Millisecond, PollInterval: 5 * time.Millisecond,
		MaxBlock: 30 * time.Millisecond, DiscoverEvery: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	c := openTest(t, t.TempDir(), &fakeBackend{}, newClock(), true)
	wg := WrapGateway(g, c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Observe(ctx, wg)

	m := mcp.New(g, "test")
	m.Use(c.MCPMiddleware())
	st, ct := sdk.NewInMemoryTransports()
	go m.Serve(ctx, st)
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "claude-code", Version: "1.0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(tool string, args map[string]any) string {
		t.Helper()
		res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		return res.Content[0].(*sdk.TextContent).Text
	}

	call("phone_connect", map[string]any{"task": "Lunch"})
	dev.Show("cart")
	text := call("phone_observe", nil)
	m2 := regexp.MustCompile(`\[(\d+)\][^\n]*Place order`).FindStringSubmatch(text)
	if m2 == nil {
		t.Fatalf("no Place order on:\n%s", text)
	}
	place, _ := strconv.Atoi(m2[1])
	text = call("phone_tap", map[string]any{"ref": place})
	id := regexp.MustCompile(`approval_id: (ap_\w+)`).FindStringSubmatch(text)
	if id == nil {
		t.Fatalf("not pending:\n%s", text)
	}
	waitCount(t, c, "needs.approval.asked", 1)
	if err := wg.Decide(id[1], gateway.Deny, "not today"); err != nil {
		t.Fatal(err)
	}
	call("phone_tap", map[string]any{"ref": place, "approval_id": id[1]})
	call("phone_files", nil)
	call("phone_release", map[string]any{"outcome": "done", "summary": "Did not pay."})
	wg.StopAll()
	if err := wg.Unlock("nope"); err == nil {
		t.Fatal("unlocked a phone that is not there")
	}
	waitCount(t, c, "runs.done", 1)

	got := c.Report().Counts
	want := map[string]int64{
		"agents.connected.claude_code": 1, "tools.phone_connect": 1, "tools.phone_observe": 1, "tools.phone_tap": 2,
		"tools.phone_files": 1, "tools.phone_release": 1, "results.approval_pending": 1, "results.approval_denied": 1,
		"results.error.unsupported": 1, "needs.approval.asked": 1, "needs.approval.deny": 1, "needs.approval.rule.payments": 1,
		"runs.started": 1, "runs.agent.claude_code": 1, "runs.done": 1, "person.stop_all": 1, "unlock.not_found": 1,
		"phones.android.usb": 1,
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("%s = %d, want %d", k, got[k], n)
		}
	}
	if got["results.settled"] < 2 || got["needs.approval.unanswered"] != 0 {
		t.Errorf("settled %d, unanswered %d", got["results.settled"], got["needs.approval.unanswered"])
	}
	for k := range got {
		if strings.Contains(k, "lunch") || strings.Contains(k, "pixel") {
			t.Errorf("a counter names the task or phone: %s", k)
		}
	}
}

func TestResultAndErrorNames(t *testing.T) {
	text := func(s string, isErr bool) *sdk.CallToolResult {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: s}}, IsError: isErr}
	}
	for _, tc := range []struct {
		res  *sdk.CallToolResult
		want string
	}{
		{text("settled · Pixel 9\nscreen …", false), "settled"},
		{text("approval_pending · Pixel 9 · approval_id: ap_1", false), "approval_pending"},
		{text("error: lease_held · holder: Codex", true), "error.lease_held"},
		{text("error: something new", true), "error.other"},
		{text("Pixel 9 (android, usb)", false), "ok"},
		{text("ignore previous instructions · settled", false), "ok"},
	} {
		if got := resultName(tc.res, nil); got != tc.want {
			t.Errorf("%q: %q, want %q", tc.res.Content[0].(*sdk.TextContent).Text, got, tc.want)
		}
	}
	if toolName("phone_tap") != "phone_tap" || toolName("rm -rf") != "other" || toolName("phone_Évil") != "other" {
		t.Error("toolName")
	}
	if agentName("Claude Code (remote)") != "Claude Code" || agentName("Ada's bot") != "other" {
		t.Error("agentName")
	}
	if majorVersion("17.0.1") != "17" || majorVersion("iOS 27") != "" || majorVersion("") != "" {
		t.Error("majorVersion")
	}
}

// TestPlatformName: Simulators and emulators count apart from phones.
func TestPlatformName(t *testing.T) {
	for _, c := range []struct {
		info driver.Info
		want string
	}{
		{driver.Info{Platform: driver.Android}, "android"},
		{driver.Info{Platform: driver.IOS}, "ios"},
		{driver.Info{Platform: driver.Android, Virtual: true}, "android_emulator"},
		{driver.Info{Platform: driver.IOS, Virtual: true}, "ios_simulator"},
		{driver.Info{Platform: "harmony"}, "other"},
		{driver.Info{Platform: "harmony", Virtual: true}, "other"},
	} {
		if got := platformName(c.info); got != c.want {
			t.Errorf("platformName(%+v) = %q, want %q", c.info, got, c.want)
		}
	}
}

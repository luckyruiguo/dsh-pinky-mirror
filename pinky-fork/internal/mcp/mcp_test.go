package mcp

import (
	"context"
	"encoding/json"
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
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

type harness struct {
	g   *gateway.Gateway
	dev *demo.Device
	srv *Server
}

func newHarness(t *testing.T, opts ...func(*gateway.Options)) *harness {
	t.Helper()
	dev := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	pol := policy.Default()
	for cat, e := range demo.PolicyApps() {
		pol.AddApps(cat, e)
	}
	o := gateway.Options{Providers: []driver.Provider{demo.NewProvider(driver.Android, dev)}, Policy: pol,
		TrustNewAgents: true, NoScreenshots: true, SettleMax: 200 * time.Millisecond, PollInterval: 5 * time.Millisecond,
		MaxBlock: 2 * time.Second, DiscoverEvery: 20 * time.Millisecond}
	for _, f := range opts {
		f(&o)
	}
	g, err := gateway.New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return &harness{g: g, dev: dev, srv: New(g, "test")}
}

type client struct {
	cs       *sdk.ClientSession
	mu       sync.Mutex
	progress []string
	done     chan error
}

// connect starts an MCP session over the SDK's in-memory transport.
func (h *harness) connect(t *testing.T, name string) *client {
	t.Helper()
	st, ct := sdk.NewInMemoryTransports()
	c := &client{done: make(chan error, 1)}
	go func() { c.done <- h.srv.Serve(context.Background(), st) }()
	cl := sdk.NewClient(&sdk.Implementation{Name: name, Version: "1.0"}, &sdk.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *sdk.ProgressNotificationClientRequest) {
			c.mu.Lock()
			c.progress = append(c.progress, req.Params.Message)
			c.mu.Unlock()
		},
	})
	cs, err := cl.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.cs = cs
	t.Cleanup(func() { cs.Close() })
	return c
}

func (c *client) call(t *testing.T, tool string, args map[string]any) (string, bool) {
	t.Helper()
	return c.callWith(t, &sdk.CallToolParams{Name: tool, Arguments: args})
}

func (c *client) callWith(t *testing.T, p *sdk.CallToolParams) (string, bool) {
	t.Helper()
	res, err := c.cs.CallTool(context.Background(), p)
	if err != nil {
		t.Fatalf("%s: %v", p.Name, err)
	}
	var texts []string
	for _, ct := range res.Content {
		if tc, ok := ct.(*sdk.TextContent); ok {
			texts = append(texts, tc.Text)
		}
	}
	return strings.Join(texts, "\n"), res.IsError
}

var refRe = regexp.MustCompile(`\[(\d+)\] \w+ "([^"]*)"`)

// refOf finds the ref of a label in a result's text.
func refOf(t *testing.T, text, label string) int {
	t.Helper()
	for _, m := range refRe.FindAllStringSubmatch(text, -1) {
		if m[2] == label {
			n, _ := strconv.Atoi(m[1])
			return n
		}
	}
	t.Fatalf("%q not in:\n%s", label, text)
	return 0
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

func TestToolList(t *testing.T) {
	h := newHarness(t)
	c := h.connect(t, "claude-code")
	res, err := c.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"phone_connect", "phone_release", "phone_observe", "phone_tap", "phone_type", "phone_swipe", "phone_pinch", "phone_drag", "phone_key",
		"phone_launch", "phone_apps", "phone_batch", "phone_wait_for", "phone_files", "phone_app", "phone_logs", "phone_device", "phone_request_approval", "phone_handoff"}
	got := map[string]*sdk.Tool{}
	for _, tool := range res.Tools {
		got[tool.Name] = tool
	}
	if len(got) != len(want) {
		t.Fatalf("%d tools, want %d", len(got), len(want))
	}
	for _, name := range want {
		tool := got[name]
		if tool == nil || tool.Description == "" {
			t.Fatalf("tool %s missing or undocumented", name)
		}
	}
	schema, _ := json.Marshal(got["phone_key"].InputSchema)
	for _, s := range []string{`"enum"`, `"recents"`, `"approval_id"`, `"intent"`, `"device"`} {
		if !strings.Contains(string(schema), s) {
			t.Fatalf("phone_key schema misses %s: %s", s, schema)
		}
	}
	// Claude Code shows a property typed ["null", ...] with its
	// description alone: agents then guessed phone_batch's step shape.
	for _, name := range want {
		if schema, _ := json.Marshal(got[name].InputSchema); strings.Contains(string(schema), `"null"`) {
			t.Fatalf("%s schema advertises null types: %s", name, schema)
		}
	}
	steps := got["phone_batch"].InputSchema.(map[string]any)["properties"].(map[string]any)["steps"].(map[string]any)
	if steps["type"] != "array" || steps["items"].(map[string]any)["type"] != "object" {
		t.Fatalf("phone_batch steps: %v", steps)
	}
	apps := got["phone_connect"].InputSchema.(map[string]any)["properties"].(map[string]any)["apps"].(map[string]any)
	if apps["type"] != "array" || apps["items"].(map[string]any)["type"] != "string" {
		t.Fatalf("phone_connect apps: %v", apps)
	}
	schema, _ = json.Marshal(got["phone_batch"].InputSchema)
	if !strings.Contains(string(schema), `"maxItems":50`) || !strings.Contains(string(schema), `"launch"`) {
		t.Fatalf("phone_batch schema: %s", schema)
	}
	schema, _ = json.Marshal(got["phone_device"].InputSchema)
	for _, s := range []string{`"reverse_landscape"`, `"dark"`, `"latitude"`, `"maximum":180`, `"all"`} {
		if !strings.Contains(string(schema), s) {
			t.Fatalf("phone_device schema misses %s: %s", s, schema)
		}
	}
	schema, _ = json.Marshal(got["phone_launch"].InputSchema)
	if !strings.Contains(string(schema), `"language"`) || !strings.Contains(string(schema), `"region"`) {
		t.Fatalf("phone_launch schema: %s", schema)
	}
	if c.cs.InitializeResult().Instructions == "" {
		t.Fatal("no server instructions")
	}
}

func TestPhoneDevice(t *testing.T) {
	h := newHarness(t)
	c := h.connect(t, "claude-code")
	c.call(t, "phone_connect", map[string]any{"task": "Test dark mode"})
	text, isErr := c.call(t, "phone_device", nil)
	if isErr || !strings.Contains(text, "\nphone: Pixel 9 · Android 16\nbattery: 82%, 30 °C\nstorage: 41.3 GB free of 128 GB\nrotation: portrait\nappearance: light\nlocation: none") {
		t.Fatalf("read:\n%s", text)
	}
	// A real phone asks first.
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if needs := h.g.Needs(); len(needs) > 0 {
				h.g.Decide(needs[0].ID, gateway.AllowTask, "")
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	text, isErr = c.call(t, "phone_device", map[string]any{"appearance": "dark", "location": map[string]any{"latitude": 35.6812, "longitude": 139.7671}})
	if isErr || !strings.HasPrefix(text, "settled") || !strings.Contains(text, "appearance: dark (was light)") ||
		!strings.Contains(text, "location: 35.6812,139.7671 (was none)") {
		t.Fatalf("set:\n%s", text)
	}
	text, isErr = c.call(t, "phone_device", map[string]any{"reset": []string{"location"}})
	if isErr || !strings.Contains(text, "Stopped the simulated location.") || !strings.Contains(text, "location: none (was 35.6812,139.7671)") {
		t.Fatalf("reset:\n%s", text)
	}
	if text, isErr = c.call(t, "phone_device", map[string]any{"rotation": "sideways"}); !isErr {
		t.Fatalf("a bad rotation:\n%s", text)
	}
	text, isErr = c.call(t, "phone_launch", map[string]any{"app": "Settings", "language": "ja"})
	if isErr || !strings.HasPrefix(text, "settled") || h.dev.AppLocale("com.android.settings") != "ja" {
		t.Fatalf("launch in Japanese:\n%s", text)
	}
	c.call(t, "phone_release", nil)
	h.g.Flush()
	if h.dev.SwitchValue(driver.SwitchAppearance) != "light" || h.dev.AppLocale("com.android.settings") != "" {
		t.Fatal("not put back after the task")
	}
}

func TestFlowWithApproval(t *testing.T) {
	h := newHarness(t)
	c := h.connect(t, "claude-code")
	text, isErr := c.call(t, "phone_connect", map[string]any{"task": "Reorder last week's lunch"})
	if isErr || !strings.HasPrefix(text, "settled · Pixel 9") || !strings.Contains(text, "screen 1080x2400") {
		t.Fatalf("connect:\n%s", text)
	}
	if tasks := h.g.Tasks(); len(tasks) != 1 || tasks[0].Agent != "Claude Code" {
		t.Fatalf("agent name: %+v", tasks)
	}
	text, _ = c.call(t, "phone_launch", map[string]any{"app": "Dinely"})
	text, _ = c.call(t, "phone_tap", map[string]any{"ref": refOf(t, text, "Orders")})
	text, _ = c.call(t, "phone_tap", map[string]any{"ref": refOf(t, text, "Reorder · Chicken rice bowl")})
	if !strings.Contains(text, `toast: "Added to cart"`) || !strings.Contains(text, `+ [`) {
		t.Fatalf("diff after tap:\n%s", text)
	}
	text, _ = c.call(t, "phone_tap", map[string]any{"ref": refOf(t, text, "Cart · 1")})
	place := refOf(t, text, "Place order · $12.80")

	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if needs := h.g.Needs(); len(needs) > 0 {
				h.g.Decide(needs[0].ID, gateway.AllowOnce, "ok")
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	p := &sdk.CallToolParams{Name: "phone_tap", Arguments: map[string]any{"ref": place, "intent": "Pay $12.80 for lunch"}}
	p.SetProgressToken("tap-1")
	text, isErr = c.callWith(t, p)
	if isErr || !strings.HasPrefix(text, "settled") || !strings.Contains(text, `note: "ok"`) || !strings.Contains(text, "Order placed") {
		t.Fatalf("approved tap:\n%s", text)
	}
	// The client handles notifications on a goroutine of their own, which
	// may run after the call's result came in.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		c.mu.Lock()
		progress := append([]string(nil), c.progress...)
		c.mu.Unlock()
		if len(progress) > 0 && strings.Contains(progress[0], "Waiting for the person") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no progress notification: %q", progress)
		}
	}

	text, _ = c.call(t, "phone_release", map[string]any{"outcome": "done", "summary": "Ordered lunch."})
	if !strings.HasPrefix(text, "settled") {
		t.Fatalf("release:\n%s", text)
	}
}

func TestPendingAndErrors(t *testing.T) {
	h := newHarness(t, func(o *gateway.Options) { o.MaxBlock = 30 * time.Millisecond })
	c := h.connect(t, "codex")
	text, _ := c.call(t, "phone_connect", map[string]any{"task": "Lunch"})
	h.dev.Show("cart")
	text, _ = c.call(t, "phone_observe", nil)
	place := refOf(t, text, "Place order · $12.80")
	text, isErr := c.call(t, "phone_tap", map[string]any{"ref": place})
	if isErr || !strings.HasPrefix(text, "approval_pending · Pixel 9 · approval_id: ap_") || !strings.Contains(text, "rule: Payments") {
		t.Fatalf("pending:\n%s", text)
	}
	id := regexp.MustCompile(`approval_id: (ap_\w+)`).FindStringSubmatch(text)[1]
	h.g.Decide(id, gateway.Deny, "not today")
	text, _ = c.call(t, "phone_tap", map[string]any{"ref": place, "approval_id": id})
	if !strings.HasPrefix(text, "approval_denied") || !strings.Contains(text, `note: "not today"`) {
		t.Fatalf("denied:\n%s", text)
	}

	// A second agent gets lease_held, as an error result.
	other := h.connect(t, "cursor")
	text, isErr = other.call(t, "phone_connect", map[string]any{"task": "Something"})
	if !isErr || !strings.HasPrefix(text, "error: lease_held") || !strings.Contains(text, "Codex · Lunch") {
		t.Fatalf("lease_held:\n%s", text)
	}
	// Bad arguments are caught.
	if text, isErr = c.call(t, "phone_tap", map[string]any{"x": 3}); !isErr || !strings.Contains(text, "x and y") {
		t.Fatalf("x without y:\n%s", text)
	}
	if text, isErr = c.call(t, "phone_key", map[string]any{"key": "power"}); !isErr {
		t.Fatalf("power key accepted:\n%s", text)
	}
	if text, isErr = c.call(t, "phone_files", nil); !isErr || !strings.HasPrefix(text, "error: unsupported") {
		t.Fatalf("phone_files:\n%s", text)
	}
	// Typing into a password field is refused.
	h.dev.Show("account")
	text, _ = c.call(t, "phone_observe", nil)
	text, _ = c.call(t, "phone_type", map[string]any{"ref": refOf(t, text, "Password"), "text": "hunter2"})
	if !strings.HasPrefix(text, "blocked_by_policy") || !strings.Contains(text, "rule: credentials") {
		t.Fatalf("password:\n%s", text)
	}
}

func TestSessionCloseReleasesLease(t *testing.T) {
	h := newHarness(t)
	c := h.connect(t, "claude-code")
	c.call(t, "phone_connect", map[string]any{"task": "Lunch"})
	c.cs.Close()
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after the client closed")
	}
	other := h.connect(t, "codex")
	if text, isErr := other.call(t, "phone_connect", map[string]any{"task": "Next"}); isErr {
		t.Fatalf("lease not released:\n%s", text)
	}
}

// A client on this computer that drops its session and starts a new one,
// as Claude Desktop does after a call times out, carries on with its task.
func TestNewSessionContinuesTask(t *testing.T) {
	h := newHarness(t)
	c := h.connect(t, "claude-ai")
	if text, isErr := c.call(t, "phone_connect", map[string]any{"task": "Lunch"}); isErr {
		t.Fatalf("connect:\n%s", text)
	}
	c.cs.Close()
	<-c.done
	again := h.connect(t, "claude-ai")
	if text, isErr := again.call(t, "phone_key", map[string]any{"key": "home"}); isErr {
		t.Fatalf("the new session did not continue the task:\n%s", text)
	}
}

func TestBatchAndScreenshot(t *testing.T) {
	h := newHarness(t)
	c := h.connect(t, "claude-code")
	text, _ := c.call(t, "phone_connect", map[string]any{"task": "Calc"})
	text, _ = c.call(t, "phone_batch", map[string]any{"steps": []map[string]any{
		{"tool": "launch", "app": "Dinely"},
		{"tool": "phone_key", "key": "back"},
		{"tool": "key", "key": "home"},
	}})
	if !strings.HasPrefix(text, "settled") || !strings.Contains(text, "step 3: settled") {
		t.Fatalf("batch:\n%s", text)
	}
	res, err := c.cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "phone_observe", Arguments: map[string]any{"screenshot": true}})
	if err != nil {
		t.Fatal(err)
	}
	var img *sdk.ImageContent
	for _, ct := range res.Content {
		if ic, ok := ct.(*sdk.ImageContent); ok {
			img = ic
		}
	}
	if img == nil || img.MIMEType != "image/jpeg" || len(img.Data) < 100 {
		t.Fatalf("no screenshot: %+v", res.Content)
	}
}

func TestHandoffOverMCP(t *testing.T) {
	h := newHarness(t)
	c := h.connect(t, "claude-code")
	c.call(t, "phone_connect", map[string]any{"task": "Sign in"})
	go func() {
		for i := 0; i < 1000; i++ {
			for _, n := range h.g.Needs() {
				if n.Kind == gateway.NeedHandoff {
					h.g.TakeOver("pixel")
					h.g.HandBack("pixel", "signed in for you")
					return
				}
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	text, isErr := c.call(t, "phone_handoff", map[string]any{"reason": "Please sign in"})
	if isErr || !strings.HasPrefix(text, "settled") || !strings.Contains(text, "signed in for you") {
		t.Fatalf("handoff:\n%s", text)
	}
}

func TestDisplayName(t *testing.T) {
	for in, want := range map[string]string{"claude-code": "Claude Code", "codex-mcp-client": "Codex", "my-agent": "My Agent", "": "Agent",
		"local-agent-mode-pinky": "Claude Cowork", "local-agent-mode-phone": "Claude Cowork"} {
		if got := DisplayName(in, ""); got != want {
			t.Errorf("DisplayName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := DisplayName("x-client", "X Client Pro"); got != "X Client Pro" {
		t.Errorf("title not used: %q", got)
	}
}

func TestStatusLine(t *testing.T) {
	m := true
	got := StatusLine(&gateway.Result{Status: gateway.StatusPending, DeviceName: "Pixel 9", ApprovalID: "ap_1", Rule: "Payments", Matched: &m})
	if got != "approval_pending · Pixel 9 · approval_id: ap_1 · rule: Payments · matched: true" {
		t.Fatal(got)
	}
	if got := firstLine(StatusLine(&gateway.Result{Error: gateway.ErrLeaseHeld, Holder: "Codex · Lunch"})); got != "error: lease_held · holder: Codex · Lunch" {
		t.Fatal(got)
	}
	if got := StatusLine(&gateway.Result{Status: gateway.StatusNeedsSetup, DeviceName: "iPhone 16", Setup: "trust"}); got != "device_needs_setup · iPhone 16 · setup: trust" {
		t.Fatal(got)
	}
}

// Every action tool and batch step takes settle_ms, from 0 to 3000; 0 reads
// the screen without waiting for it to settle, which the result says.
func TestSettleMS(t *testing.T) {
	h := newHarness(t)
	c := h.connect(t, "claude-code")
	res, err := c.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	acting := map[string]bool{"phone_tap": true, "phone_type": true, "phone_swipe": true, "phone_pinch": true, "phone_drag": true, "phone_key": true, "phone_launch": true, "phone_batch": true}
	for _, tool := range res.Tools {
		schema, _ := json.Marshal(tool.InputSchema)
		has := strings.Contains(string(schema), `"settle_ms":{`)
		if has != acting[tool.Name] {
			t.Fatalf("%s: settle_ms in schema %v: %s", tool.Name, has, schema)
		}
		if has && !strings.Contains(string(schema), `"maximum":3000,"minimum":0,"type":"integer"`) {
			t.Fatalf("%s: settle_ms not an integer from 0 to 3000: %s", tool.Name, schema)
		}
	}

	c.call(t, "phone_connect", map[string]any{"task": "Calc"})
	text, isErr := c.call(t, "phone_swipe", map[string]any{"direction": "up"})
	if isErr || strings.Contains(firstLine(text), "settle: timeout") {
		t.Fatalf("swipe:\n%s", text)
	}
	// The screen did not change, but a read made early by settle_ms proves
	// nothing: no "no change", and the message says why.
	text, isErr = c.call(t, "phone_swipe", map[string]any{"direction": "down", "settle_ms": 0})
	if isErr || firstLine(text) != "settled · Pixel 9 · settle: timeout" || !strings.Contains(text, "Read before the screen settled") {
		t.Fatalf("swipe with settle_ms 0:\n%s", text)
	}
	if text, isErr = c.call(t, "phone_tap", map[string]any{"x": 10, "y": 10, "settle_ms": 3001}); !isErr {
		t.Fatalf("settle_ms 3001 accepted:\n%s", text)
	}
	text, isErr = c.call(t, "phone_batch", map[string]any{"steps": []map[string]any{
		{"tool": "swipe", "direction": "up", "settle_ms": 0},
		{"tool": "swipe", "direction": "down", "settle_ms": 500},
	}})
	if isErr || !strings.Contains(text, "step 2: settled") {
		t.Fatalf("batch with settle_ms:\n%s", text)
	}
}

// phone_wait_for takes several texts (any) and a part of the screen (area);
// text is no longer required, since any can stand alone.
func TestWaitForAnyAndArea(t *testing.T) {
	h := newHarness(t)
	c := h.connect(t, "claude-code")
	res, err := c.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var schema string
	for _, tool := range res.Tools {
		if tool.Name == "phone_wait_for" {
			b, _ := json.Marshal(tool.InputSchema)
			schema = string(b)
		}
	}
	for _, want := range []string{
		`"maxItems":10,"type":"array"`,
		`"required":["x","y","width","height"]`,
	} {
		if !strings.Contains(schema, want) {
			t.Fatalf("phone_wait_for schema lacks %s: %s", want, schema)
		}
	}
	for _, bad := range []string{`"required":["text"`, `"null"`} {
		if strings.Contains(schema, bad) {
			t.Fatalf("phone_wait_for schema has %s: %s", bad, schema)
		}
	}

	text, _ := c.call(t, "phone_connect", map[string]any{"task": "Calc"})
	if !strings.Contains(text, `"Calculator"`) {
		t.Fatalf("no Calculator on the home screen:\n%s", text)
	}
	text, isErr := c.call(t, "phone_wait_for", map[string]any{"any": []string{"No such app", "Calculator"}, "timeout_ms": 2000})
	if isErr || firstLine(text) != "settled · Pixel 9 · matched: true · no change" || !strings.Contains(text, "“Calculator” appeared after ") {
		t.Fatalf("any:\n%s", text)
	}
	// An area the size of one pixel in the corner holds no text.
	text, isErr = c.call(t, "phone_wait_for", map[string]any{"text": "Calculator", "area": map[string]any{"x": 0, "y": 0, "width": 1, "height": 1}, "timeout_ms": 100})
	if isErr || !strings.HasPrefix(firstLine(text), "settled · Pixel 9 · matched: false") || !strings.Contains(text, "has not appeared in the area") {
		t.Fatalf("area:\n%s", text)
	}
	if text, isErr = c.call(t, "phone_wait_for", map[string]any{"timeout_ms": 100}); !isErr || !strings.Contains(text, "invalid_argument") {
		t.Fatalf("neither text nor any:\n%s", text)
	}
	if text, isErr = c.call(t, "phone_wait_for", map[string]any{"text": "Calculator", "area": map[string]any{"x": 0, "y": 0, "width": 0, "height": 10}}); !isErr || !strings.Contains(text, "invalid_argument") {
		t.Fatalf("empty area:\n%s", text)
	}
}

// A new agent's phone_connect waits for the person like any other approval:
// the agent repeats the call with the approval_id it was given.
func TestConnectWaitsWithApprovalID(t *testing.T) {
	h := newHarness(t, func(o *gateway.Options) { o.TrustNewAgents = false; o.MaxBlock = 30 * time.Millisecond })
	c := h.connect(t, "codex")
	text, isErr := c.call(t, "phone_connect", map[string]any{"task": "Lunch"})
	if isErr || !strings.HasPrefix(text, "approval_pending") || !strings.Contains(text, "phone_connect with approval_id") {
		t.Fatalf("first connection:\n%s", text)
	}
	id := regexp.MustCompile(`approval_id: (ap_\w+)`).FindStringSubmatch(text)[1]
	// Still waiting: the same approval, not a second card.
	text, _ = c.call(t, "phone_connect", map[string]any{"task": "Lunch", "approval_id": id})
	if !strings.HasPrefix(text, "approval_pending") || !strings.Contains(text, id) || len(h.g.Needs()) != 1 {
		t.Fatalf("waiting again:\n%s\nneeds: %+v", text, h.g.Needs())
	}
	h.g.Decide(id, gateway.AllowOnce, "")
	text, isErr = c.call(t, "phone_connect", map[string]any{"task": "Lunch", "approval_id": id})
	if isErr || !strings.HasPrefix(text, "settled") {
		t.Fatalf("after the person allowed it:\n%s", text)
	}
}

// TestPhoneDrag drags through MCP: the schema bounds the rests and the
// waypoints, and the drag reaches the phone with its stops, alone and as
// a batch step.
func TestPhoneDrag(t *testing.T) {
	h := newHarness(t)
	c := h.connect(t, "claude-code")
	res, err := c.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Name != "phone_drag" && tool.Name != "phone_batch" {
			continue
		}
		schema, _ := json.Marshal(tool.InputSchema)
		for _, s := range []string{`"hold_ms"`, `"pause_ms"`, `"via"`, `"maxItems":8`, `"maximum":5000`} {
			if !strings.Contains(string(schema), s) {
				t.Fatalf("%s schema misses %s: %s", tool.Name, s, schema)
			}
		}
		if tool.Name == "phone_drag" && (strings.Contains(string(schema), `"null"`) || !strings.Contains(string(schema), `"required":["to"]`)) {
			t.Fatalf("phone_drag schema: %s", schema)
		}
	}

	text, _ := c.call(t, "phone_connect", map[string]any{"task": "Tidy the home screen"})
	calc := refOf(t, text, "Calculator")
	text, isErr := c.call(t, "phone_drag", map[string]any{"ref": calc, "hold_ms": 800,
		"via": []any{map[string]any{"x": 1070, "y": 1200, "pause_ms": 1000}}, "to": map[string]any{"x": 540, "y": 1700}})
	if isErr || !strings.HasPrefix(text, "settled") {
		t.Fatalf("drag:\n%s", text)
	}
	in := h.dev.Inputs()[len(h.dev.Inputs())-1]
	if in.Kind != "drag" || in.To != (screen.Point{X: 540, Y: 1700}) {
		t.Fatalf("input %+v", in)
	}
	if text, isErr = c.call(t, "phone_drag", map[string]any{"ref": calc}); !isErr || !strings.Contains(text, "to") {
		t.Fatalf("a drag without to:\n%s", text)
	}
	text, isErr = c.call(t, "phone_batch", map[string]any{"steps": []any{
		map[string]any{"tool": "phone_drag", "from": map[string]any{"x": 540, "y": 648}, "to": map[string]any{"x": 540, "y": 1500}, "pause_ms": 0},
	}})
	if isErr || !strings.HasPrefix(text, "settled") {
		t.Fatalf("batch:\n%s", text)
	}
	if in := h.dev.Inputs()[len(h.dev.Inputs())-1]; in.Kind != "drag" || in.P != (screen.Point{X: 540, Y: 648}) {
		t.Fatalf("batch input %+v", in)
	}
}

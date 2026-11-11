// Package pilot is Ask Pinky: Pinky's own agent, which does one task on
// one phone with a model the person chose. It is for simple tasks, for
// people who use no other agent: one task, one phone, no skills, nothing
// remembered between tasks.
//
// It works through Pinky's own MCP server, like every agent: an MCP
// session over the SDK's in-memory transports, the phone_* tools with
// their descriptions, rendered screens and diffs, and a gateway session
// behind every call. Rules, approvals, read-only, blocked apps, Take over
// and Hand back, Stop all, recordings and pausing a phone apply as they
// are; nothing here calls a driver. The session's agent is named Pinky
// (mcp.BuiltinName) and needs no New agent approval: the person started
// it from Pinky's window.
package pilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DIYgod/pinky/internal/mcp"
	"github.com/DIYgod/pinky/internal/secret"
)

// Providers: the API a model is reached through.
const (
	// Anthropic is the Messages API: Anthropic's own, or a service that
	// speaks it at an address of the person's.
	Anthropic = "anthropic"
	// OpenAI is an OpenAI-compatible Chat Completions API.
	OpenAI = "openai"
)

// Defaults of a Config.
const (
	DefaultModel     = "claude-opus-5-5"
	DefaultMaxSteps  = 100
	DefaultMaxTime   = 20 * time.Minute
	DefaultMaxCost   = 2.0
	DefaultOpenAIURL = "https://api.openai.com/v1"
)

// Config is how a task runs: the model and the limits.
type Config struct {
	Provider string // Anthropic (default) or OpenAI
	// Model is the model's name; empty is DefaultModel with Anthropic. An
	// OpenAI-compatible API needs one.
	Model string
	// BaseURL is the API's address; empty is the provider's own. For the
	// Messages API it is what ANTHROPIC_BASE_URL is to Claude Code (the
	// path ends before /v1/messages); for an OpenAI-compatible API, the
	// address that /chat/completions is added to.
	BaseURL string
	// The limits; the first one reached ends the task. Steps are the
	// phone tools the model calls. Time leaves out what the task waits
	// for the person (a handoff, an approval). Cost is in US dollars,
	// from the usage the API reports; with a model whose prices Pinky
	// does not know, it is a budget of tokens at US$1 a million.
	MaxSteps int
	MaxTime  time.Duration
	MaxCost  float64
}

// Normalize fills in the defaults.
func (c Config) Normalize() Config {
	c.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	if c.Provider != OpenAI {
		c.Provider = Anthropic
	}
	c.Model = strings.TrimSpace(c.Model)
	if c.Model == "" && c.Provider == Anthropic {
		c.Model = DefaultModel
	}
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if c.MaxSteps <= 0 {
		c.MaxSteps = DefaultMaxSteps
	}
	if c.MaxTime <= 0 {
		c.MaxTime = DefaultMaxTime
	}
	if c.MaxCost <= 0 {
		c.MaxCost = DefaultMaxCost
	}
	return c
}

// Priced reports whether Pinky knows the model's prices, so that the
// cost limit is in dollars; otherwise it is a budget of tokens.
func (c Config) Priced() bool {
	_, ok := priceOf(c)
	return ok
}

// TokenLimit is the cost limit in tokens, for a model whose prices Pinky
// does not know: US$1 a million.
func (c Config) TokenLimit() int64 { return int64(c.MaxCost * tokensPerDollar) }

// End is how a task ended.
type End string

const (
	Running   End = ""        // it has not ended
	Done      End = "done"    // the model did the task; Summary says what it did
	Failed    End = "failed"  // the model could not do it; Summary says why
	Stopped   End = "stopped" // the person stopped it
	StepLimit End = "steps"   // it reached Config.MaxSteps
	TimeLimit End = "time"    // it reached Config.MaxTime
	CostLimit End = "cost"    // it reached Config.MaxCost
	Refused   End = "refused" // the model declined the task
	NoKey     End = "no_key"  // no API key is saved for the provider
	BadKey    End = "bad_key" // the API did not accept the key; Summary has its words
	Error     End = "error"   // the API or the phone failed; Summary says how
)

// Status is a task of Pinky's own agent, running or ended.
type Status struct {
	Device  string // the phone's device id
	Task    string // the person's words
	Config  Config
	Started time.Time
	Ended   time.Time // zero while it runs
	End     End
	Summary string
	Steps   int
	// Cost is in US dollars, zero when the model's prices are unknown;
	// Tokens counts every token sent and received.
	Cost   float64
	Tokens int64
	// Priced says Cost is known; without it the cost limit is in tokens.
	Priced bool
}

// Running reports whether the task has not ended.
func (s Status) Running() bool { return s.Ended.IsZero() }

// Errors of Start.
var (
	ErrNoTask  = errors.New("pilot: no task")
	ErrNoModel = errors.New("pilot: no model")
	ErrBusy    = errors.New("pilot: Pinky is already doing a task on this phone")
	ErrClosed  = errors.New("pilot: closed")
)

// Options set up a Runner.
type Options struct {
	// Server is Pinky's MCP server, which every call goes through.
	Server *mcp.Server
	// Version is Pinky's version, for the MCP client's name.
	Version string
	// Key returns the API key saved for a provider, secret.ErrNotFound
	// when there is none. The Runner asks for it as each task starts and
	// keeps it no longer than the task.
	Key func(ctx context.Context, provider string) (string, error)
	// Notify is called, from any goroutine, when a task's status changes.
	Notify func()
	// Describe writes the line Pinky gives a task it ends itself (a
	// limit, an error) as the summary in Activity; nil is Describe, in
	// English.
	Describe func(Status) string
	// HTTPClient makes the API requests; nil is http.DefaultClient.
	HTTPClient *http.Client
}

// Runner runs Pinky's own agent, one task a phone at a time.
type Runner struct {
	opt    Options
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	runs   map[string]*run // by device id, the running and the last ended
	closed bool
}

// New returns a Runner.
func New(o Options) *Runner {
	if o.Describe == nil {
		o.Describe = Describe
	}
	if o.HTTPClient == nil {
		o.HTTPClient = http.DefaultClient
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Runner{opt: o, ctx: ctx, cancel: cancel, runs: map[string]*run{}}
}

// Start starts a task on a phone. It returns at once; Status follows it.
func (r *Runner) Start(cfg Config, device, task string) error {
	task = strings.TrimSpace(task)
	if task == "" || device == "" {
		return ErrNoTask
	}
	cfg = cfg.Normalize()
	if cfg.Model == "" {
		return ErrNoModel
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrClosed
	}
	if ru := r.runs[device]; ru != nil && ru.status().Running() {
		r.mu.Unlock()
		return ErrBusy
	}
	ctx, cancel := context.WithCancel(r.ctx)
	ru := &run{r: r, cancel: cancel, st: Status{Device: device, Task: task, Config: cfg, Started: time.Now()}}
	_, ru.st.Priced = priceOf(cfg)
	r.runs[device] = ru
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		defer cancel()
		end, summary := ru.work(ctx)
		ru.mu.Lock()
		ru.st.End, ru.st.Summary, ru.st.Ended = end, summary, time.Now()
		ru.mu.Unlock()
		r.notify()
	}()
	r.notify()
	return nil
}

// Stop ends the task on a phone at once, cancelling a model request in
// flight. Stop the gateway's task first (gateway.API.Stop): a task whose
// MCP session just goes away counts as done in Activity.
func (r *Runner) Stop(device string) {
	r.mu.Lock()
	ru := r.runs[device]
	r.mu.Unlock()
	if ru != nil {
		ru.stop()
	}
}

// StopAll stops every task, as Stop does.
func (r *Runner) StopAll() {
	r.mu.Lock()
	var rs []*run
	for _, ru := range r.runs {
		rs = append(rs, ru)
	}
	r.mu.Unlock()
	for _, ru := range rs {
		ru.stop()
	}
}

// Status returns the task on a phone: the running one, or the last that
// ended until Dismiss.
func (r *Runner) Status(device string) (Status, bool) {
	r.mu.Lock()
	ru := r.runs[device]
	r.mu.Unlock()
	if ru == nil {
		return Status{}, false
	}
	return ru.status(), true
}

// Dismiss forgets the ended task on a phone.
func (r *Runner) Dismiss(device string) {
	r.mu.Lock()
	if ru := r.runs[device]; ru != nil && !ru.status().Running() {
		delete(r.runs, device)
	}
	r.mu.Unlock()
	r.notify()
}

// Close stops every task and waits for them to end.
func (r *Runner) Close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.cancel()
	r.wg.Wait()
}

func (r *Runner) notify() {
	if r.opt.Notify != nil {
		r.opt.Notify()
	}
}

// run is one task.
type run struct {
	r      *Runner
	cancel context.CancelFunc

	mu      sync.Mutex
	st      Status
	stopped bool
	waited  time.Duration // spent waiting for the person
}

func (ru *run) status() Status {
	ru.mu.Lock()
	defer ru.mu.Unlock()
	return ru.st
}

func (ru *run) stop() {
	ru.mu.Lock()
	ru.stopped = true
	ru.mu.Unlock()
	ru.cancel()
}

func (ru *run) update(f func(s *Status)) {
	ru.mu.Lock()
	f(&ru.st)
	ru.mu.Unlock()
	ru.r.notify()
}

// The tools of the MCP server the model does not get: phone_connect and
// phone_release, which the runner calls itself, and those a simple task
// on a phone it already holds does not need.
var leftOut = map[string]bool{
	"phone_connect": true, "phone_release": true, "phone_app": true, "phone_device": true,
	"phone_files": true, "phone_logs": true,
}

// waits are the tools that wait for the person: their time does not count
// against Config.MaxTime.
var waits = map[string]bool{"phone_handoff": true, "phone_request_approval": true}

// work runs the task to its end.
func (ru *run) work(ctx context.Context) (End, string) {
	cfg := ru.st.Config
	key, err := ru.r.opt.Key(ctx, cfg.Provider)
	switch {
	case ctx.Err() != nil:
		return Stopped, ""
	case errors.Is(err, secret.ErrNotFound) || err == nil && strings.TrimSpace(key) == "":
		return NoKey, ""
	case err != nil:
		return Error, err.Error()
	}
	key = strings.TrimSpace(key)

	// The MCP session lives as long as the task, also after a Stop
	// cancelled ctx, so that it can still end the task.
	sctx, scancel := context.WithCancel(context.Background())
	defer scancel()
	st, ct := sdk.NewInMemoryTransports()
	if err := ru.r.opt.Server.ConnectBuiltin(sctx, st); err != nil {
		return Error, err.Error()
	}
	cl := sdk.NewClient(&sdk.Implementation{Name: "pinky-agent", Title: mcp.BuiltinName, Version: ru.r.opt.Version}, nil)
	cs, err := cl.Connect(sctx, ct, nil)
	if err != nil {
		return Error, err.Error()
	}
	defer cs.Close()

	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		return ru.ended(ctx, cs, err)
	}
	var tools []tool
	for _, t := range list.Tools {
		if leftOut[t.Name] {
			continue
		}
		s, _ := t.InputSchema.(map[string]any)
		if s == nil {
			b, _ := json.Marshal(t.InputSchema)
			json.Unmarshal(b, &s)
		}
		tools = append(tools, tool{name: t.Name, description: t.Description, schema: s})
	}
	tools = append(tools, finishTool)

	task := ru.st.Task
	res := ru.call(ctx, cs, "phone_connect", map[string]any{"device": ru.st.Device, "task": oneLine(task)})
	switch {
	case ctx.Err() != nil || res.status == "stopped":
		return Stopped, ""
	case res.isError:
		return Error, res.message()
	}

	m, err := newModel(cfg, key, ru.r.opt.HTTPClient, systemPrompt(cfg), tools)
	if err != nil {
		return ru.end(cs, Error, err.Error())
	}
	text := firstMessage(task, ru.st.Device, res.text, time.Now())
	var results []result
	nudged := false
	for {
		if end := ru.limit(); end != Running {
			return ru.end(cs, end, "")
		}
		t, err := m.send(ctx, results, text)
		results, text = nil, ""
		if t != nil {
			ru.count(t.usage, cfg)
		}
		if err != nil {
			return ru.ended(ctx, cs, redact(err, key))
		}
		if t.refused {
			return ru.end(cs, Refused, "")
		}
		if len(t.calls) == 0 {
			if nudged {
				return ru.end(cs, Failed, strings.TrimSpace(t.text))
			}
			// It wrote to the person, who cannot answer: once, ask it to
			// go on or finish.
			nudged = true
			text = nudge
			continue
		}
		for _, c := range t.calls {
			switch {
			case c.name == finishName:
				return ru.finish(ctx, cs, c)
			case c.bad != "":
				results = append(results, result{id: c.id, text: "error: invalid_argument\n" + c.bad, isError: true})
				continue
			case leftOut[c.name] || !has(tools, c.name):
				results = append(results, result{id: c.id, text: "error: unknown_tool\nThere is no tool " + c.name + ".", isError: true})
				continue
			}
			if end := ru.limit(); end != Running {
				return ru.end(cs, end, "")
			}
			ru.update(func(s *Status) { s.Steps++ })
			res := ru.call(ctx, cs, c.name, c.args)
			if ctx.Err() != nil || res.status == "stopped" {
				return Stopped, ""
			}
			results = append(results, result{id: c.id, text: res.text, images: res.images, isError: res.isError})
		}
	}
}

// ended ends the task after err, or as stopped when the person stopped it.
func (ru *run) ended(ctx context.Context, cs *sdk.ClientSession, err error) (End, string) {
	if ctx.Err() != nil {
		return Stopped, ""
	}
	var ae *apiError
	if errors.As(err, &ae) && ae.status == http.StatusUnauthorized {
		return ru.end(cs, BadKey, ae.message)
	}
	return ru.end(cs, Error, err.Error())
}

// finish ends the task with the model's own outcome and summary.
func (ru *run) finish(ctx context.Context, cs *sdk.ClientSession, c call) (End, string) {
	outcome, _ := c.args["outcome"].(string)
	summary, _ := c.args["summary"].(string)
	summary = strings.TrimSpace(summary)
	end := Failed
	if strings.EqualFold(strings.TrimSpace(outcome), "done") {
		end = Done
	}
	res := ru.call(ctx, cs, "phone_release", map[string]any{"outcome": string(end), "summary": summary})
	if ctx.Err() != nil || res.status == "stopped" {
		return Stopped, ""
	}
	return end, summary
}

// end ends a task Pinky stops itself, with its line in Activity.
func (ru *run) end(cs *sdk.ClientSession, end End, summary string) (End, string) {
	ru.mu.Lock()
	stopped := ru.stopped
	st := ru.st
	ru.mu.Unlock()
	if stopped {
		return Stopped, ""
	}
	st.End, st.Summary = end, summary
	// A context of its own: the task's may be cancelled by now.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ru.call(ctx, cs, "phone_release", map[string]any{"outcome": "failed", "summary": ru.r.opt.Describe(st)})
	return end, summary
}

// limit reports the limit the task has reached, if any.
func (ru *run) limit() End {
	ru.mu.Lock()
	defer ru.mu.Unlock()
	s := ru.st
	switch {
	case s.Steps >= s.Config.MaxSteps:
		return StepLimit
	case time.Since(s.Started)-ru.waited >= s.Config.MaxTime:
		return TimeLimit
	case s.Priced && s.Cost >= s.Config.MaxCost,
		!s.Priced && s.Tokens >= s.Config.TokenLimit():
		return CostLimit
	}
	return Running
}

// tokensPerDollar is the budget of a model whose prices Pinky does not
// know.
const tokensPerDollar = 1_000_000

func (ru *run) count(u usage, cfg Config) {
	ru.update(func(s *Status) {
		s.Tokens += u.input + u.output + u.cacheRead + u.cacheWrite
		if p, ok := priceOf(cfg); ok {
			s.Cost += p.cost(u)
		}
	})
}

// phoneResult is a tool's result as the model gets it, with its status.
type phoneResult struct {
	text       string
	images     []image
	isError    bool
	status     string // settled, approval_pending, …, or the error's code
	approvalID string
}

// message is what a result says after its status line.
func (p phoneResult) message() string {
	_, rest, _ := strings.Cut(p.text, "\n")
	if rest = strings.TrimSpace(rest); rest != "" {
		line, _, _ := strings.Cut(rest, "\n")
		return line
	}
	return p.status
}

// call calls a tool, waiting on by itself while the person decides on an
// approval or a handoff: the result's approval_id goes back with the same
// call, as the server's instructions tell agents. phone_batch is left to
// the model, whose approval_id goes in the batch's step.
func (ru *run) call(ctx context.Context, cs *sdk.ClientSession, name string, args map[string]any) phoneResult {
	start := time.Now()
	waited := waits[name]
	defer func() {
		if waited {
			ru.mu.Lock()
			ru.waited += time.Since(start)
			ru.mu.Unlock()
		}
	}()
	for {
		res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			return phoneResult{text: "error: internal\n" + err.Error(), isError: true, status: "internal"}
		}
		p := parse(res)
		if p.status != "approval_pending" {
			return p
		}
		waited = true
		if p.approvalID == "" || name == "phone_batch" {
			return p
		}
		a := make(map[string]any, len(args)+1)
		for k, v := range args {
			a[k] = v
		}
		a["approval_id"] = p.approvalID
		args = a
	}
}

func parse(res *sdk.CallToolResult) phoneResult {
	var p phoneResult
	var texts []string
	for _, c := range res.Content {
		switch c := c.(type) {
		case *sdk.TextContent:
			texts = append(texts, c.Text)
		case *sdk.ImageContent:
			p.images = append(p.images, image{data: c.Data, mime: c.MIMEType})
		}
	}
	p.text = strings.Join(texts, "\n")
	p.isError = res.IsError
	first, _, _ := strings.Cut(p.text, "\n")
	parts := strings.Split(first, " · ")
	p.status = strings.TrimSpace(parts[0])
	if code, ok := strings.CutPrefix(p.status, "error: "); ok {
		p.status, p.isError = code, true
	}
	for _, part := range parts[1:] {
		if id, ok := strings.CutPrefix(part, "approval_id: "); ok {
			p.approvalID = strings.TrimSpace(id)
		}
	}
	return p
}

func has(tools []tool, name string) bool {
	for _, t := range tools {
		if t.name == name {
			return true
		}
	}
	return false
}

// oneLine makes the person's words the task's one-line name.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 120 {
		s = string(r[:119]) + "…"
	}
	return s
}

// redact keeps the API key out of an error, should the API repeat it.
func redact(err error, key string) error {
	if key == "" || !strings.Contains(err.Error(), key) {
		return err
	}
	var ae *apiError
	if errors.As(err, &ae) {
		c := *ae
		c.message = strings.ReplaceAll(c.message, key, "…")
		return &c
	}
	return errors.New(strings.ReplaceAll(err.Error(), key, "…"))
}

// Describe is the line, in English, Pinky gives a task it ends itself.
func Describe(s Status) string {
	switch s.End {
	case StepLimit:
		return fmt.Sprintf("Stopped: reached the limit of %d steps.", s.Config.MaxSteps)
	case TimeLimit:
		return fmt.Sprintf("Stopped: reached the limit of %d minutes.", int(s.Config.MaxTime.Minutes()))
	case CostLimit:
		if s.Priced {
			return fmt.Sprintf("Stopped: reached the limit of US$%.2f.", s.Config.MaxCost)
		}
		return fmt.Sprintf("Stopped: reached the limit of %d tokens.", s.Config.TokenLimit())
	case Refused:
		return "Couldn't finish: the model declined this task."
	case BadKey:
		return "Couldn't start: the API did not accept the key."
	case Failed:
		if s.Summary != "" {
			return "Couldn't finish: " + s.Summary
		}
		return "Couldn't finish."
	case Error:
		return "Couldn't finish: " + s.Summary
	}
	return s.Summary
}

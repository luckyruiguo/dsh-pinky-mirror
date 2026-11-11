package pilot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// anthropicURL is the address of Anthropic's own API; tests change it.
var anthropicURL = "https://api.anthropic.com"

// anthropicVersion is the version of the Messages API Pinky speaks.
const anthropicVersion = "2026-10-21"

// claude is a conversation over the Messages API, in plain HTTP: Pinky
// needs one endpoint, and Anthropic's Go SDK would add about 19 MB to it.
//
// On Anthropic's own API it uses what the API offers an agent like this:
// the prompt cache (the system prompt and tools never change during a
// task, and the history only grows), effort medium, context editing to
// clear old screens (clear_tool_uses, which is not an edit of the history,
// so the cache and the model's thinking stay valid) and, for the models
// that support it, server-side fallbacks, which give a request the model
// declines to another model rather than ending the task.
//
// At an address of the person's, a service that speaks the Messages API
// may know none of the betas, so it gets the plain API with the cache
// markers alone; Pinky clears old screens itself, unless the model is a
// Claude model, whose thinking an edited history would lose.
type claude struct {
	url      string
	key      string
	official bool
	hc       *http.Client
	req      anRequest // all but the messages
	betas    []string
	history  []anMessage
	trim     bool
}

// fallbackModels are the models that take server-side fallbacks.
var fallbackModels = []string{"claude-opus-5-5", "claude-opus-5", "claude-sonnet-5-5", "claude-fable-5-2", "claude-fable-5-1"}

// The request and the blocks Pinky sends.
type (
	anRequest struct {
		Model             string      `json:"model"`
		MaxTokens         int         `json:"max_tokens"`
		System            []anText    `json:"system"`
		Tools             []anTool    `json:"tools,omitempty"`
		Messages          []anMessage `json:"messages"`
		CacheControl      *anCache    `json:"cache_control,omitempty"`
		OutputConfig      *anOutput   `json:"output_config,omitempty"`
		ContextManagement *anContext  `json:"context_management,omitempty"`
		Fallbacks         string      `json:"fallbacks,omitempty"`
	}
	anCache struct {
		Type string `json:"type"`
	}
	anOutput struct {
		Effort string `json:"effort"`
	}
	anTool struct {
		Name         string         `json:"name"`
		Description  string         `json:"description,omitempty"`
		InputSchema  map[string]any `json:"input_schema"`
		CacheControl *anCache       `json:"cache_control,omitempty"`
	}
	anContext struct {
		Edits []anEdit `json:"edits"`
	}
	anEdit struct {
		Type         string   `json:"type"`
		Trigger      anAmount `json:"trigger"`
		Keep         anAmount `json:"keep"`
		ClearAtLeast anAmount `json:"clear_at_least"`
	}
	anAmount struct {
		Type  string `json:"type"`
		Value int    `json:"value"`
	}

	// anMessage is a turn of the conversation. Its blocks are anText and
	// *anToolResult in the person's turns, and the model's own blocks,
	// as json.RawMessage, in its turns.
	anMessage struct {
		Role    string `json:"role"`
		Content []any  `json:"content"`
	}
	anText struct {
		Type         string   `json:"type"`
		Text         string   `json:"text"`
		CacheControl *anCache `json:"cache_control,omitempty"`
	}
	anImage struct {
		Type   string        `json:"type"`
		Source anImageSource `json:"source"`
	}
	anImageSource struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	}
	anToolResult struct {
		Type      string `json:"type"`
		ToolUseID string `json:"tool_use_id"`
		Content   []any  `json:"content"` // anText and anImage
		IsError   bool   `json:"is_error,omitempty"`
	}
)

var ephemeral = &anCache{Type: "ephemeral"}

// anResponse is the part of the API's answer Pinky reads.
type anResponse struct {
	Content    []json.RawMessage `json:"content"`
	StopReason string            `json:"stop_reason"`
	Usage      struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

func newClaude(cfg Config, key string, hc *http.Client, system string, tools []tool) *claude {
	official := cfg.BaseURL == "" || cfg.BaseURL == strings.TrimRight(anthropicURL, "/")
	base := cfg.BaseURL
	if base == "" {
		base = anthropicURL
	}
	c := &claude{url: strings.TrimRight(base, "/") + "/v1/messages", key: key, official: official, hc: hc}
	r := anRequest{
		Model:     cfg.Model,
		MaxTokens: 16000,
		System:    []anText{{Type: "text", Text: system, CacheControl: ephemeral}},
	}
	for i, t := range tools {
		at := anTool{Name: t.name, Description: t.description, InputSchema: parameters(t.schema)}
		if i == len(tools)-1 {
			at.CacheControl = ephemeral
		}
		r.Tools = append(r.Tools, at)
	}
	if official {
		// The conversation itself: the cache moves to its last block.
		r.CacheControl = ephemeral
		if _, known := priceOf(cfg); known {
			r.OutputConfig = &anOutput{Effort: "medium"}
		}
		r.ContextManagement = &anContext{Edits: []anEdit{{
			Type:         "clear_tool_uses_20250919",
			Trigger:      anAmount{Type: "input_tokens", Value: keepChars / 4},
			Keep:         anAmount{Type: "tool_uses", Value: keepResults},
			ClearAtLeast: anAmount{Type: "input_tokens", Value: keepChars / 4 / 2},
		}}}
		c.betas = []string{"context-management-2025-06-27"}
		if slices.ContainsFunc(fallbackModels, func(m string) bool { return cfg.Model == m || strings.HasPrefix(cfg.Model, m+"-2") }) {
			r.Fallbacks = "default"
			c.betas = append(c.betas, "server-side-fallback-2026-07-01")
		}
	} else {
		c.trim = !strings.HasPrefix(strings.ToLower(cfg.Model), "claude")
	}
	c.req = r
	return c
}

// header is what each request carries besides its body. The key goes as
// a bearer token, as the API's version asks; a service of the person's
// gets it in X-Api-Key too, as Claude Code's ANTHROPIC_AUTH_TOKEN and
// ANTHROPIC_API_KEY send it.
func (c *claude) header() http.Header {
	h := http.Header{}
	h.Set("Anthropic-Version", anthropicVersion)
	h.Set("Authorization", "Bearer "+c.key)
	if !c.official {
		h.Set("X-Api-Key", c.key)
	}
	if len(c.betas) > 0 {
		h.Set("Anthropic-Beta", strings.Join(c.betas, ","))
	}
	return h
}

func (c *claude) send(ctx context.Context, results []result, text string) (*turn, error) {
	var blocks []any
	for _, r := range results {
		blocks = append(blocks, toolResult(r))
	}
	if text != "" {
		blocks = append(blocks, anText{Type: "text", Text: text})
	}
	c.history = append(c.history, anMessage{Role: "user", Content: blocks})
	if c.trim {
		c.clearOld()
	}
	r := c.req
	r.Messages = c.history
	body, err := marshal(r)
	if err != nil {
		return nil, err
	}
	data, err := post(ctx, c.hc, c.url, c.header(), body)
	if err != nil {
		return nil, err
	}
	var resp anResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("the API's answer is not JSON: %w", err)
	}
	if content := echo(resp.Content); len(content) > 0 {
		c.history = append(c.history, anMessage{Role: "assistant", Content: content})
	}

	t := &turn{usage: usage{
		input:      resp.Usage.InputTokens,
		output:     resp.Usage.OutputTokens,
		cacheRead:  resp.Usage.CacheReadInputTokens,
		cacheWrite: resp.Usage.CacheCreationInputTokens,
	}}
	switch resp.StopReason {
	case "refusal":
		t.refused = true
		return t, nil
	case "model_context_window_exceeded":
		return t, errors.New("the task outgrew what the model can read at once")
	}
	var texts []string
	for _, raw := range resp.Content {
		var b struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if json.Unmarshal(raw, &b) != nil {
			continue
		}
		switch b.Type {
		case "text":
			texts = append(texts, b.Text)
		case "tool_use":
			cl := call{id: b.ID, name: b.Name}
			if len(b.Input) > 0 {
				if err := json.Unmarshal(b.Input, &cl.args); err != nil {
					cl.bad = "The arguments are not a JSON object: " + err.Error()
				}
			}
			t.calls = append(t.calls, cl)
		}
	}
	t.text = strings.Join(texts, "\n")
	return t, nil
}

// echo is the model's content as the next request sends it back: each
// block as it came, less its null fields. After a fallback in the middle
// of the output, the blocks before the last fallback block are the
// declining model's, and only their text goes back, with the fallback
// blocks where they were.
func echo(content []json.RawMessage) []any {
	typ := func(raw json.RawMessage) string {
		var b struct {
			Type string `json:"type"`
		}
		json.Unmarshal(raw, &b)
		return b.Type
	}
	last := -1
	for i, raw := range content {
		if typ(raw) == "fallback" {
			last = i
		}
	}
	out := make([]any, 0, len(content))
	for i, raw := range content {
		if t := typ(raw); i < last && t != "text" && t != "fallback" {
			continue
		}
		out = append(out, withoutNulls(raw))
	}
	return out
}

// withoutNulls is a block without its fields whose value is null, which
// a response has and a request may not take.
func withoutNulls(raw json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	n := len(m)
	for k, v := range m {
		if string(v) == "null" {
			delete(m, k)
		}
	}
	if len(m) == n {
		return raw
	}
	b, err := marshal(m)
	if err != nil {
		return raw
	}
	return b
}

func toolResult(r result) *anToolResult {
	tr := &anToolResult{Type: "tool_result", ToolUseID: r.id, IsError: r.isError}
	if r.text != "" || len(r.images) == 0 {
		tr.Content = append(tr.Content, anText{Type: "text", Text: r.text})
	}
	for _, im := range r.images {
		mt := im.mime
		if mt == "" {
			mt = "image/jpeg"
		}
		tr.Content = append(tr.Content, anImage{Type: "image", Source: anImageSource{
			Type: "base64", MediaType: mt, Data: base64.StdEncoding.EncodeToString(im.data),
		}})
	}
	return tr
}

// clearOld clears old tool results once the history grows past keepChars.
func (c *claude) clearOld() {
	var trs []*anToolResult
	size := 0
	for _, m := range c.history {
		if m.Role != "user" {
			continue
		}
		for _, b := range m.Content {
			tr, ok := b.(*anToolResult)
			if !ok {
				continue
			}
			trs = append(trs, tr)
			for _, p := range tr.Content {
				switch p := p.(type) {
				case anText:
					size += len(p.Text)
				case anImage:
					size += imageChars
				}
			}
		}
	}
	if size <= keepChars {
		return
	}
	for _, tr := range trs[:max(0, len(trs)-keepResults)] {
		tr.Content = []any{anText{Type: "text", Text: cleared}}
	}
}

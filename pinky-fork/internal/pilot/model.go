package pilot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// tool is a tool the model may call, with its JSON Schema.
type tool struct {
	name        string
	description string
	schema      map[string]any
}

// call is a tool call of the model's. bad says why its arguments could
// not be read; the call then goes back to the model as an error.
type call struct {
	id   string
	name string
	args map[string]any
	bad  string
}

// image is a picture in a tool's result: its bytes and their type.
type image struct {
	data []byte
	mime string
}

// result is a tool's result for the model.
type result struct {
	id      string
	text    string
	images  []image
	isError bool
}

// usage is what a request used, in tokens.
type usage struct {
	input      int64 // input not read from or written to the cache
	output     int64
	cacheRead  int64
	cacheWrite int64
}

// turn is the model's answer to a request.
type turn struct {
	text    string // what it wrote, if anything
	calls   []call
	refused bool
	usage   usage
}

// model is a conversation with a model over one API. It keeps the
// history: each send adds the results of the last turn's calls, then the
// text, if any, as the person's message.
type model interface {
	send(ctx context.Context, results []result, text string) (*turn, error)
}

// newModel starts a conversation with the model of cfg.
func newModel(cfg Config, key string, hc *http.Client, system string, tools []tool) (model, error) {
	if cfg.Provider == OpenAI {
		return newOpenAI(cfg, key, hc, system, tools), nil
	}
	return newClaude(cfg, key, hc, system, tools), nil
}

// Old screens leave the conversation when it grows past keepChars: the
// results of all but the last keepResults calls become cleared, all at
// once, so that a prompt cache keeps working until the next time. It is
// for APIs that do not clear them themselves (Anthropic's own API does,
// with context editing). About 4 characters make a token, so this keeps
// a conversation near 40k tokens.
const (
	keepChars   = 160_000
	keepResults = 5
	cleared     = "[Removed to save space. Call phone_observe to see the screen again.]"
)

// imageChars is what a screenshot counts as against keepChars: about the
// 1,500 tokens a phone's screenshot takes.
const imageChars = 6000

// apiError is an error the API answered with.
type apiError struct {
	status     int
	message    string
	retryAfter time.Duration
}

func (e *apiError) Error() string {
	s := fmt.Sprintf("the API answered %d %s", e.status, http.StatusText(e.status))
	if e.message != "" {
		s += ": " + e.message
	}
	return s
}

// post sends a request's body to an API and returns the answer's body,
// trying twice more after a rate limit, a server's error or a dropped
// connection.
func post(ctx context.Context, hc *http.Client, url string, header http.Header, body []byte) ([]byte, error) {
	wait := time.Second
	for try := 0; ; try++ {
		data, retry, err := postOnce(ctx, hc, url, header, body)
		if err == nil || !retry || try == 2 {
			return data, err
		}
		var ae *apiError
		if errors.As(err, &ae) && ae.retryAfter > 0 && ae.retryAfter <= 30*time.Second {
			wait = ae.retryAfter
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		wait *= 3
	}
}

func postOnce(ctx context.Context, hc *http.Client, url string, header http.Header, body []byte) ([]byte, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	req.Header = header.Clone()
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, ctx.Err() == nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, true, err
	}
	if resp.StatusCode/100 != 2 {
		ae := &apiError{status: resp.StatusCode, message: errorMessage(data)}
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
			ae.retryAfter = time.Duration(s) * time.Second
		}
		return nil, resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500, ae
	}
	return data, false, nil
}

// marshal is v in JSON, with <, > and & as they are: screens are full of
// them, and escaped they only cost tokens.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// errorMessage is the message of an error's body: error.message, or the
// start of the body.
func errorMessage(data []byte) string {
	var e struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && len(e.Error) > 0 {
		var m struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(e.Error, &m) == nil && m.Message != "" {
			return m.Message
		}
		var s string
		if json.Unmarshal(e.Error, &s) == nil && s != "" {
			return s
		}
	}
	s := strings.TrimSpace(string(data))
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300]) + "…"
	}
	return s
}

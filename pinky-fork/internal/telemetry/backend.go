package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"
)

// Backend receives Pinky's reports. HTTP is the one Pinky uses, with the
// Worker in worker/; a hosted service would be another. Feedback does not
// go through it: it goes to GitHub (IssueURL).
type Backend interface {
	Usage(ctx context.Context, u Usage) error
	Crash(ctx context.Context, r CrashReport) error
}

// Usage is a usage report: counters for a period, with what they ran on.
type Usage struct {
	InstallID   string           `json:"install_id"`
	Version     string           `json:"version"`
	OS          string           `json:"os"`
	OSVersion   string           `json:"os_version,omitempty"`
	Arch        string           `json:"arch"`
	PeriodStart time.Time        `json:"period_start"`
	PeriodEnd   time.Time        `json:"period_end"`
	Counts      map[string]int64 `json:"counts"`
}

// Limits on what is kept and sent, in bytes.
const (
	maxDiagnostics = 256 << 10
	maxStack       = 256 << 10
)

// Paths under the backend's URL.
const (
	PathUsage = "/v1/usage"
	PathCrash = "/v1/crash"
)

// HTTP posts reports as JSON to URL + PathUsage and PathCrash.
type HTTP struct {
	URL     string
	Version string       // for the User-Agent
	Client  *http.Client // nil: a 30-second timeout
}

// Usage posts a usage report.
func (h *HTTP) Usage(ctx context.Context, u Usage) error { return h.post(ctx, PathUsage, u) }

// Crash posts a crash report.
func (h *HTTP) Crash(ctx context.Context, r CrashReport) error { return h.post(ctx, PathCrash, r) }

func (h *HTTP) post(ctx context.Context, path string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return &permanentError{err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(h.URL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return &permanentError{err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("Pinky/%s (%s; %s)", nonEmpty(h.Version, DevVersion), runtime.GOOS, runtime.GOARCH))
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode/100 == 2 {
		return nil
	}
	err = fmt.Errorf("%s: %s %s", path, resp.Status, strings.TrimSpace(string(msg)))
	// The backend refused this report (too large, malformed): sending it
	// again would not help. Too many requests and server errors may pass
	// later.
	if resp.StatusCode/100 == 4 && resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusRequestTimeout {
		return &permanentError{err}
	}
	return err
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// permanentError is a report the backend will never take.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent reports whether err means the report should be dropped
// rather than sent again.
func Permanent(err error) bool {
	var p *permanentError
	return errors.As(err, &p)
}

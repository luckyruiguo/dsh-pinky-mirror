package companion

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/screen"
)

// EventKind is the kind of a companion event.
type EventKind string

// Event kinds. Subscribing with no kinds gets window, toast and
// notification events.
const (
	EventWindow       EventKind = "window"       // a new activity, dialog or pane
	EventWindows      EventKind = "windows"      // a window was added, removed, moved or focused
	EventContent      EventKind = "content"      // content changed or scrolled; at most one per 100 ms
	EventToast        EventKind = "toast"        // a toast appeared
	EventNotification EventKind = "notification" // a notification was posted
	EventDropped      EventKind = "dropped"      // the app dropped Count events for a slow reader
)

// Event is something that happened on the phone.
type Event struct {
	Kind    EventKind
	Time    time.Time // when it happened, on the host's clock
	Package string    // the app it came from
	// Display is the display the event came from, or -1 when that is not
	// known: notifications, which belong to the phone; a window that has
	// only just opened (Android 11 and 12); every event from apps built
	// before round three below Android 13, which leave the field out.
	// Never read -1 as the main display.
	Display  int
	Class    string   // window: the activity or dialog class
	Title    string   // window: its title; notification: the title
	Text     string   // toast and notification text
	Category string   // notification category, such as "msg"
	Window   int      // window and windows: the window id
	Changes  []string // windows: added, removed, title, bounds, layer, active, focused, ...
	Count    int      // content: events folded into this one; dropped: events lost
}

// ScreenEvent converts a toast or notification into the screen model's
// event. Other kinds return false.
func (e Event) ScreenEvent() (screen.Event, bool) {
	switch e.Kind {
	case EventToast:
		return screen.Event{Kind: screen.EventToast, Text: e.Text}, true
	case EventNotification:
		text := e.Text
		switch {
		case e.Title != "" && e.Text != "":
			text = e.Title + ": " + e.Text
		case e.Title != "":
			text = e.Title
		}
		return screen.Event{Kind: screen.EventNotification, App: e.Package, Text: text}, true
	}
	return screen.Event{}, false
}

type wireEvent struct {
	Event    string   `json:"event"`
	Time     int64    `json:"time"`
	Package  string   `json:"package"`
	Display  *int     `json:"display"`
	Class    string   `json:"class"`
	Title    string   `json:"title"`
	Text     string   `json:"text"`
	Category string   `json:"category"`
	Window   int      `json:"window"`
	Changes  []string `json:"changes"`
	Count    int      `json:"count"`
}

// Subscription delivers events on its own connection until it is closed,
// its context ends or the service goes away.
type Subscription struct {
	client *Client
	conn   net.Conn
	c      chan Event
	done   chan struct{}
	once   sync.Once

	mu  sync.Mutex
	err error
}

// Events subscribes to events of the given kinds.
func (c *Client) Events(ctx context.Context, kinds ...EventKind) (*Subscription, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	req := map[string]any{"id": 1, "cmd": "events"}
	if len(kinds) > 0 {
		req["kinds"] = kinds
	}
	line, _ := json.Marshal(req)
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	rd := bufio.NewReaderSize(conn, 64<<10)
	if _, err := conn.Write(append(line, '\n')); err != nil {
		conn.Close()
		return nil, c.connError(ctx, err)
	}
	raw, err := rd.ReadBytes('\n')
	if err != nil {
		conn.Close()
		return nil, c.connError(ctx, err)
	}
	var resp response
	if err := json.Unmarshal(raw, &resp); err != nil {
		conn.Close()
		return nil, fmt.Errorf("companion: bad events reply: %w", err)
	}
	if !resp.OK {
		conn.Close()
		if resp.Error != nil {
			return nil, resp.Error
		}
		return nil, fmt.Errorf("companion: events failed")
	}
	_ = conn.SetDeadline(time.Time{})

	s := &Subscription{client: c, conn: conn, c: make(chan Event, 256), done: make(chan struct{})}
	c.subMu.Lock()
	c.subs[s] = struct{}{}
	c.subMu.Unlock()
	go s.read(ctx, rd)
	return s, nil
}

// C returns the channel events arrive on. It is closed when the
// subscription ends; Err then says why.
func (s *Subscription) C() <-chan Event { return s.c }

// Err returns why the subscription ended, or nil if it was closed or its
// context ended.
func (s *Subscription) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Close ends the subscription.
func (s *Subscription) Close() error {
	s.once.Do(func() {
		close(s.done)
		_ = s.conn.Close()
	})
	return nil
}

func (s *Subscription) read(ctx context.Context, rd *bufio.Reader) {
	defer close(s.c)
	defer func() {
		s.client.subMu.Lock()
		delete(s.client.subs, s)
		s.client.subMu.Unlock()
	}()
	stop := context.AfterFunc(ctx, func() { s.Close() })
	defer stop()
	defer s.Close()
	for {
		raw, err := rd.ReadBytes('\n')
		if err != nil {
			select {
			case <-s.done:
			default:
				s.mu.Lock()
				s.err = fmt.Errorf("%w: %v", ErrNotRunning, err)
				s.mu.Unlock()
			}
			return
		}
		var w wireEvent
		if json.Unmarshal(raw, &w) != nil || w.Event == "" {
			continue
		}
		display := -1 // left out: not known
		if w.Display != nil {
			display = *w.Display
		}
		ev := Event{
			Kind:     EventKind(w.Event),
			Time:     s.client.HostTime(w.Time),
			Package:  w.Package,
			Display:  display,
			Class:    w.Class,
			Title:    w.Title,
			Text:     w.Text,
			Category: w.Category,
			Window:   w.Window,
			Changes:  w.Changes,
			Count:    w.Count,
		}
		select {
		case s.c <- ev:
		case <-s.done:
			return
		}
	}
}

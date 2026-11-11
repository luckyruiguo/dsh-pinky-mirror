package android

import (
	"bufio"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The person's own input is read with `getevent` from the phone's input
// devices. Input that Pinky injects through scrcpy goes to the input
// dispatcher directly and never passes through /dev/input, so it is not
// seen here.

const (
	// personMinGap keeps PersonInput to a few signals a second.
	personMinGap = 300 * time.Millisecond
	// personRetryMax caps the wait before watching again after an error.
	personRetryMax = 30 * time.Second
)

// Linux input event types and codes (linux/input-event-codes.h).
const (
	evKey = 0x01
	evAbs = 0x03
)

// Keys that mean someone pressed a button on the phone.
var personKeys = []string{"KEY_POWER", "KEY_VOLUMEUP", "KEY_VOLUMEDOWN", "KEY_HOME", "KEY_BACK", "KEY_MENU", "KEY_APPSELECT"}

// inputDevices are the device nodes watched, by kind.
type inputDevices struct {
	touch map[string]bool // touchscreens: any touch event counts
	keys  map[string]bool // hardware keys: a key press counts
}

// classifyInputDevices reads `getevent -lp` and picks the touchscreens
// (multi-touch or single-touch axes on a direct input device) and the
// devices with hardware keys. Devices that user space created with uinput
// (fingerprint gestures, injection tools) are left out: they can report
// keys nobody pressed. A phone without any direct touch device (the
// emulator's virtio touchscreens have no input properties; an .idc file
// makes them touchscreens) uses its multi-touch devices instead.
func classifyInputDevices(out string) inputDevices {
	devs := inputDevices{touch: map[string]bool{}, keys: map[string]bool{}}
	multi := map[string]bool{} // multi-touch devices that are not direct
	var path, name string
	var caps strings.Builder
	flush := func() {
		if path == "" {
			return
		}
		c := caps.String()
		uinput := strings.HasPrefix(strings.ToLower(name), "uinput")
		direct := strings.Contains(c, "INPUT_PROP_DIRECT")
		axes := strings.Contains(c, "ABS_MT_POSITION_X") || strings.Contains(c, "ABS_X") && strings.Contains(c, "BTN_TOUCH")
		switch {
		case uinput:
		case axes && direct:
			devs.touch[path] = true
		case strings.Contains(c, "ABS_MT_POSITION_X"):
			multi[path] = true
		case hasAnyWord(c, personKeys):
			devs.keys[path] = true
		}
		path, name = "", ""
		caps.Reset()
	}
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		t := strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(t, "add device "); ok {
			flush()
			if i := strings.Index(rest, ": "); i >= 0 {
				path = strings.TrimSpace(rest[i+2:])
			}
			continue
		}
		if n, ok := strings.CutPrefix(t, "name:"); ok && name == "" {
			name = strings.Trim(strings.TrimSpace(n), `"`)
			continue
		}
		caps.WriteString(t)
		caps.WriteByte('\n')
	}
	flush()
	if len(devs.touch) == 0 {
		devs.touch = multi
	}
	return devs
}

// String lists the watched device nodes.
func (devs inputDevices) String() string {
	list := func(m map[string]bool) string {
		var names []string
		for p := range m {
			names = append(names, strings.TrimPrefix(p, "/dev/input/"))
		}
		sort.Strings(names)
		return strings.Join(names, ", ")
	}
	return "touchscreens [" + list(devs.touch) + "] and keys [" + list(devs.keys) + "]"
}

func hasAnyWord(s string, words []string) bool {
	for _, f := range strings.Fields(s) {
		for _, w := range words {
			if f == w {
				return true
			}
		}
	}
	return false
}

// inputEvent is one line of `getevent` output.
type inputEvent struct {
	path      string
	typ, code uint16
	value     int32
}

// parseEventLine parses "[ 123.456] /dev/input/event5: 0003 0035 0000021c";
// the time stamp is optional.
func parseEventLine(line string) (inputEvent, bool) {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "[") {
		if i := strings.IndexByte(line, ']'); i >= 0 {
			line = strings.TrimSpace(line[i+1:])
		}
	}
	path, rest, ok := strings.Cut(line, ": ")
	if !ok || !strings.HasPrefix(path, "/dev/input/") {
		return inputEvent{}, false
	}
	f := strings.Fields(rest)
	if len(f) != 3 {
		return inputEvent{}, false
	}
	typ, err1 := strconv.ParseUint(f[0], 16, 16)
	code, err2 := strconv.ParseUint(f[1], 16, 16)
	val, err3 := strconv.ParseUint(f[2], 16, 32)
	if err1 != nil || err2 != nil || err3 != nil {
		return inputEvent{}, false
	}
	return inputEvent{path: path, typ: uint16(typ), code: uint16(code), value: int32(uint32(val))}, true
}

// personEvent reports whether an event is the person's input: a touch on
// a touchscreen, or a key going down on a key device.
func (devs inputDevices) personEvent(ev inputEvent) bool {
	switch {
	case devs.touch[ev.path]:
		return ev.typ == evAbs || ev.typ == evKey
	case devs.keys[ev.path]:
		return ev.typ == evKey && ev.value == 1
	}
	return false
}

// personWatch runs `getevent` for PersonInput.
type personWatch struct {
	mu     sync.Mutex
	ch     chan struct{}
	cancel context.CancelFunc
	done   chan struct{}
	closed bool
	last   time.Time // the last signal sent

	// Touches on the phone's own touchscreen, for a background display that
	// keeps the phone dark (dark.go): when, and a wake-up for its watch.
	touchAt   atomic.Int64 // Unix nanoseconds
	touchOnce sync.Once
	touchCh   chan struct{}
}

// touched returns a channel that receives after a touch on the phone's own
// touchscreen, while PersonInput is being watched.
func (w *personWatch) touched() <-chan struct{} { return w.touchChan() }

func (w *personWatch) touchChan() chan struct{} {
	w.touchOnce.Do(func() { w.touchCh = make(chan struct{}, 1) })
	return w.touchCh
}

// touchedSince reports whether the touchscreen was touched after t.
func (w *personWatch) touchedSince(t time.Time) bool {
	at := w.touchAt.Load()
	return at != 0 && time.Unix(0, at).After(t)
}

// touch records a touch on the touchscreen.
func (w *personWatch) touch(now time.Time) {
	w.touchAt.Store(now.UnixNano())
	select {
	case w.touchChan() <- struct{}{}:
	default:
	}
}

// PersonInput implements driver.PersonWatcher. The first call starts
// watching the phone's touchscreens and hardware keys with `getevent`;
// the channel is closed when the device closes.
func (d *Device) PersonInput() <-chan struct{} {
	w := &d.person
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ch != nil {
		return w.ch
	}
	w.ch = make(chan struct{}, 1)
	if w.closed {
		close(w.ch)
		return w.ch
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel, w.done = cancel, make(chan struct{})
	go d.watchPerson(ctx)
	return w.ch
}

func (w *personWatch) close() {
	w.mu.Lock()
	w.closed = true
	cancel, done := w.cancel, w.done
	w.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// signal sends one value, at most every personMinGap, never blocking.
func (w *personWatch) signal(now time.Time) {
	if now.Sub(w.last) < personMinGap {
		return
	}
	w.last = now
	select {
	case w.ch <- struct{}{}:
	default:
	}
}

func (d *Device) watchPerson(ctx context.Context) {
	w := &d.person
	defer close(w.done)
	defer close(w.ch)
	wait := 2 * time.Second
	lastErr := ""
	for ctx.Err() == nil {
		start := time.Now()
		err := d.watchPersonOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if msg := fmt.Sprint(err); msg != lastErr {
			lastErr = msg
			d.cfg.logf("android: %s: watching for the person's input: %v; retrying", d.info.ID, err)
		}
		if time.Since(start) > time.Minute {
			wait = 2 * time.Second
		}
		if sleepCtx(ctx, wait) != nil {
			return
		}
		wait = min(wait*2, personRetryMax)
	}
}

// watchPersonOnce lists the input devices and reads their events until the
// stream ends or ctx is done.
func (d *Device) watchPersonOnce(ctx context.Context) error {
	out, err := d.client.Shell(ctx, d.info.ID, "getevent -lp")
	if err != nil {
		return wrapErr(err)
	}
	devs := classifyInputDevices(string(out))
	if len(devs.touch)+len(devs.keys) == 0 {
		return fmt.Errorf("no touchscreen or key device in getevent -lp")
	}
	d.cfg.logf("android: %s: watching the person's input on %s", d.info.ID, devs)
	// The shell prints its pid first, so getevent can be stopped on the
	// phone when the watch ends.
	conn, err := d.client.Dial(ctx, d.info.ID, "shell:echo pid:$$; exec getevent -q")
	if err != nil {
		return wrapErr(err)
	}
	pid := ""
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer func() {
		stop()
		conn.Close()
		if pid != "" {
			kctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _ = d.client.Shell(kctx, d.info.ID, "kill "+pid+" 2>/dev/null")
		}
	}()
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		line := sc.Text()
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "pid:"); ok && pid == "" {
			if _, err := strconv.Atoi(p); err == nil {
				pid = p
			}
			continue
		}
		ev, ok := parseEventLine(line)
		if ok && devs.personEvent(ev) {
			now := time.Now()
			if devs.touch[ev.path] {
				d.person.touch(now)
			}
			d.person.signal(now)
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	return fmt.Errorf("getevent ended")
}

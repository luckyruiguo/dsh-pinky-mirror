package android

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/scrcpy"
)

// Background displays that share the main display's power (Android 17).
//
// scrcpy creates its virtual display with VIRTUAL_DISPLAY_FLAG_OWN_DISPLAY_GROUP,
// and up to Android 16 that gave the display a group of its own: the
// phone's own screen timed out and locked while the display kept running.
// Android 17's separate screen timeouts put every virtual display that does
// not belong to a virtual device into the main display's group instead,
// flag or not (LogicalDisplayMapper.assignDisplayGroupLocked asks
// DisplayGroupAllocator, which picks the primary group). The display then
// follows the main screen's power: once the screen times out or the person
// turns it off, the display is off too, its app stops and the companion
// cannot read it. A display-scoped wake lock does not help. Seen on the Mi
// 10 Pro (LineageOS 24) and on the Android 17 emulator.
//
// So while such a display is open, Pinky keeps the phone awake with its
// panel off ("dark"):
//
//   - When the phone is asleep and locked, Pinky wakes it (it stays locked)
//     and turns the panel off at once with scrcpy's display power control,
//     as scrcpy's --turn-screen-off does. The display power controller
//     still thinks the screen is on, so the group stays awake and the
//     background display runs; nothing lights up.
//   - While dark, a key that does nothing on the background display keeps
//     the group from timing out (the lock screen's 10 s).
//   - A touch on the phone, an unlock, or a call or alarm over the lock
//     screen turns the panel back on: the person gets their screen.
//   - The power key puts the awake phone to sleep. After that, or any sleep
//     other than a timeout, Pinky does not wake the phone again: the
//     display waits until the person has turned the screen on, and the
//     agent is told the phone needs them (driver.ErrLocked). After a
//     timeout Pinky wakes it again, dark, once it has locked. A new task's
//     display may wake it whatever turned it off.
//   - While the person uses the phone, Pinky sends no keep-awake key, so
//     their screen times out as usual. The agent's input on the display
//     counts as activity for the shared group, though, so it keeps their
//     screen on while the agent acts.
//   - When the last such display closes, the keep-awake key stops and the
//     phone times out by itself.
//
// Two details of scrcpy and SurfaceFlinger shape this. The panel is turned
// off and on through control-only scrcpy sessions of their own
// (dark.helpers), never through a session with a display: a server that
// turned the panel off turns it back on when it exits while the phone is
// awake, which would light the lock screen whenever a background display
// closes; the helpers exit only once the phone sleeps. And the phone is woken before the
// display is created: SurfaceFlinger keeps the power mode a virtual display
// had before it got its first surface, and starts it in that mode every
// time scrcpy gives it a new one (each video reset), so a display created
// while the phone sleeps stops drawing at its first reset, even once the
// phone is awake.
//
// What the person sees: an always-on display goes dark while an agent
// works, and waking the phone lights the panel for a frame or two at most.
// The display power controller powers the panel on some 30 ms after the
// wake key, which takes SurfaceFlinger about 110 ms on the Mi 10 Pro, and
// sets the brightness only once that call returns. Each helper's first
// panel-off call runs while the phone still sleeps, since it loads classes
// for up to half a second; then one helper gets the wake key and a train
// of panel-off messages, and the other a train of its own 50 ms later, so
// that a panel-off call is likely to wait right behind the power-on and
// turn the panel off before its brightness is set. Measured on the Mi: the
// panel-off began 0 to 33 ms after the power-on finished, and the
// brightness is set some 12 ms after it.

const (
	// darkPoll is how often the phone's power is read while a display
	// shares it.
	darkPoll = 2 * time.Second
	// darkTrain is the number of panel-off messages after the wake key, for
	// each helper: the display power controller turns the panel on some 35
	// ms after the key on the Mi 10 Pro, a message that changes nothing
	// takes 1 to 3 ms, and the train must still run then, whichever
	// helper's write arrives first.
	darkTrain = 100
	// darkTrainGap is how much later the second helper's train is sent.
	darkTrainGap = 50 * time.Millisecond
	// darkLockWait bounds waiting for the lock screen after a timeout (the
	// phone locks 5 s after the screen goes off by default).
	darkLockWait = 8 * time.Second
	// darkWindDown bounds the wait for the phone to time out after the last
	// display closed, before Pinky stops watching and lets go of the panel.
	darkWindDown = 2 * time.Minute
)

// errNoDark marks a phone whose panel does not turn off on request.
var errNoDark = errors.New("android: the phone's panel did not turn off")

// phonePower is the main display group's state as the lock screen sees it,
// and the built-in panel's power mode.
type phonePower struct {
	known       bool
	interactive bool   // awake or waking up
	locked      bool   // the lock screen shows
	occluded    bool   // an activity shows over the lock screen
	panel       string // SurfaceFlinger's power mode: On, Off, Doze...; "" unknown
}

// powerCmd reads phonePower: the keyguard's state from the window policy
// and the built-in panel's power mode from SurfaceFlinger, both small and
// fast dumps (dumpsys power takes longer).
const powerCmd = `dumpsys window policy | grep -E '^ +(showing|occluded|interactiveState)='; dumpsys SurfaceFlinger --displays | grep -E 'connectionType=|powerMode='`

var (
	kgShowingRE     = regexp.MustCompile(`(?m)^\s+showing=(true|false)`)
	kgOccludedRE    = regexp.MustCompile(`(?m)^\s+occluded=(true|false)`)
	kgInteractiveRE = regexp.MustCompile(`(?m)^\s+interactiveState=INTERACTIVE_STATE_(\w+)`)
	sfDisplayRE     = regexp.MustCompile(`(?m)^\s+(connectionType|powerMode)=(\w+)`)
	displayGroupRE  = regexp.MustCompile(`displayId (\d+), displayGroupId (-?\d+)`)
	sleepReasonRE   = regexp.MustCompile(`(?m)^\s+mLastSleepReason=(\w+)`)
	lastWakeRE      = regexp.MustCompile(`(?m)^\s+mLastWakeTime=(\d+)`)
)

// parsePhonePower parses powerCmd's output.
func parsePhonePower(out string) phonePower {
	var p phonePower
	if m := kgInteractiveRE.FindStringSubmatch(out); m != nil {
		p.known = true
		p.interactive = m[1] == "AWAKE" || m[1] == "WAKING"
	}
	if m := kgShowingRE.FindStringSubmatch(out); m != nil {
		p.locked = m[1] == "true"
	}
	if m := kgOccludedRE.FindStringSubmatch(out); m != nil {
		p.occluded = m[1] == "true"
	}
	// The displays SurfaceFlinger lists as internal are the phone's own
	// panels (a foldable has two); virtual displays have no connection
	// type. One that is on makes the panel on: scrcpy turns them all off.
	internal := false
	for _, m := range sfDisplayRE.FindAllStringSubmatch(out, -1) {
		switch {
		case m[1] == "connectionType":
			internal = m[2] == "Internal"
		case internal && (p.panel == "" || m[2] == "On"):
			p.panel = m[2]
			internal = false
		}
	}
	return p
}

// parseDisplayGroup returns a display's group from `dumpsys display`.
func parseDisplayGroup(out string, id int) (int, bool) {
	for _, m := range displayGroupRE.FindAllStringSubmatch(out, -1) {
		if d, _ := strconv.Atoi(m[1]); d == id {
			g, _ := strconv.Atoi(m[2])
			return g, true
		}
	}
	return 0, false
}

// parseSleepReason returns why the phone last went to sleep, and when it
// last woke up (uptime milliseconds), from `dumpsys power`.
func parseSleepReason(out string) (reason, wokeAt string) {
	if m := sleepReasonRE.FindStringSubmatch(out); m != nil {
		reason = m[1]
	}
	if m := lastWakeRE.FindStringSubmatch(out); m != nil {
		wokeAt = m[1]
	}
	return reason, wokeAt
}

// personSleep reports whether a sleep reason means someone turned the
// screen off, rather than its timeout.
func personSleep(reason string) bool {
	switch reason {
	case "timeout", "inattentive", "":
		return false
	}
	return true
}

// dark keeps one phone awake with its panel off while background displays
// that share the main display's power are open (see the top of this file).
type dark struct {
	d *Device

	// on: the phone is awake with its panel off, by Pinky. used: a display
	// that shares its power is open (users > 0).
	on, used atomic.Bool

	mu      sync.Mutex // one look at, or change of, the phone's power at a time
	users   int        // open displays that share the main display's power
	leftAt  time.Time  // when the last one closed
	running bool       // run is watching the phone
	poke    chan struct{}
	ended   chan struct{}      // closed when run returns
	helpers [2]*scrcpy.Session // control-only sessions that turn the panel off and on
	warm    [2]bool            // each ran a panel-off call
	endedAt time.Time          // when helpers were last let go
	onAt    time.Time          // when Pinky woke the phone dark
	wokeAt  string             // the phone's mLastWakeTime then
	hold    string             // why Pinky does not wake the phone now; "" = it may
	broken  error              // the panel does not turn off: never try again
	last    phonePower         // the last state read
	lastAt  time.Time
}

// darkOf returns the device's dark state, creating it.
func (d *Device) darkOf() *dark {
	d.bgMu.Lock()
	defer d.bgMu.Unlock()
	if d.dk == nil {
		d.dk = &dark{d: d, poke: make(chan struct{}, 1)}
	}
	return d.dk
}

// darkIfAny returns the device's dark state, or nil.
func (d *Device) darkIfAny() *dark {
	d.bgMu.Lock()
	defer d.bgMu.Unlock()
	return d.dk
}

func (k *dark) logf(format string, args ...any) {
	k.d.cfg.logf("android: %s: "+format, append([]any{k.d.info.ID}, args...)...)
}

// acquire counts a display that shares the main display's power and gets
// the phone ready for it: awake, or woken dark when it sleeps, before the
// display is created (see the top of this file). It reports whether it
// woke the phone, and fails when the phone sleeps and cannot be woken dark
// (not locked yet, or its panel does not turn off); then nothing is
// counted.
func (k *dark) acquire(ctx context.Context) (woke bool, err error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.users == 0 {
		k.hold = "" // a new task may wake the phone, whatever turned it off
	}
	k.users++
	k.used.Store(true)
	k.start()
	before := k.onAt
	if err := k.readyLocked(ctx); err != nil {
		k.users--
		if k.users == 0 {
			k.used.Store(false)
			k.leftAt = time.Now()
		}
		k.nudge()
		return false, err
	}
	return k.onAt != before, nil
}

// release uncounts a display; with none left the keep-awake key stops and
// the phone times out by itself.
func (k *dark) release() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.users > 0 {
		k.users--
	}
	if k.users == 0 {
		k.used.Store(false)
		k.leftAt = time.Now()
	}
	k.nudge()
}

// start starts run; k.mu must be held.
func (k *dark) start() {
	if k.running {
		return
	}
	k.running = true
	k.ended = make(chan struct{})
	go k.run(k.ended)
}

func (k *dark) nudge() {
	select {
	case k.poke <- struct{}{}:
	default:
	}
}

// keepAwake reports whether the keep-awake key should run. It does not
// wait for a look at the phone in progress.
func (k *dark) keepAwake() bool { return k.on.Load() && k.used.Load() }

// read reads the phone's power; k.mu must be held.
func (k *dark) read(ctx context.Context) (phonePower, error) {
	out, err := k.d.client.Shell(ctx, k.d.info.ID, powerCmd)
	if err != nil {
		return phonePower{}, wrapErr(err)
	}
	p := parsePhonePower(string(out))
	if !p.known {
		return p, fmt.Errorf("android: cannot read the lock screen's state from %q", firstLine(out))
	}
	k.last, k.lastAt = p, time.Now()
	return p, nil
}

// power reads why the phone last went to sleep and when it last woke up.
func (k *dark) power(ctx context.Context) (reason, wokeAt string) {
	out, err := k.d.client.Shell(ctx, k.d.info.ID, `dumpsys power | grep -E '^  (mLastSleepReason|mLastWakeTime)='`)
	if err != nil {
		return "", ""
	}
	return parseSleepReason(string(out))
}

// run watches the phone's power while displays share it, and after the
// last one closed until the phone sleeps: it wakes the phone dark when it
// may, notices the person, and ends the helper session at the end.
func (k *dark) run(ended chan struct{}) {
	defer close(ended)
	t := time.NewTicker(darkPoll)
	defer t.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for {
		if k.step(ctx) {
			return
		}
		select {
		case <-t.C:
		case <-k.poke:
		case <-k.d.person.touched():
		}
	}
}

// step is one look at the phone; it reports whether run is done.
func (k *dark) step(ctx context.Context) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	p, err := k.read(rctx)
	if err != nil {
		k.logf("reading the phone's power: %v", err)
		if errors.Is(err, driver.ErrOffline) {
			return k.finish(false)
		}
		return false
	}
	switch {
	case !p.interactive && k.on.Load():
		k.on.Store(false)
		reason, _ := k.power(rctx)
		if personSleep(reason) && k.users > 0 {
			k.hold = "the person turned the phone's screen off"
		}
		k.logf("the phone went to sleep while dark (%s)", reason)
	case p.interactive && k.on.Load():
		if why := k.personBack(rctx, p); why != "" {
			k.lighten(why)
		}
	case !p.interactive && k.users > 0 && k.hold == "" && p.locked:
		if err := k.darken(rctx); err != nil {
			k.logf("waking the phone dark: %v", err)
		}
	case p.interactive:
		// The person's screen, or one lit for them: no keep-awake key, and
		// the next sleep decides again.
		k.hold = ""
	}
	if k.users > 0 {
		return false
	}
	switch {
	case !k.on.Load():
		return k.finish(true)
	case time.Since(k.leftAt) > darkWindDown:
		k.logf("the phone did not time out within %v of the last background display closing", darkWindDown)
		return k.finish(false)
	}
	return false
}

// finish ends run: the helper sessions exit (the phone is asleep, or the
// person has its screen, so their exit changes nothing they see). k.mu
// must be held.
func (k *dark) finish(asleep bool) bool {
	for i, h := range k.helpers {
		if h != nil {
			k.helpers[i], k.warm[i] = nil, false
			k.endedAt = time.Now()
			go h.Close()
		}
	}
	if !asleep {
		k.on.Store(false)
	}
	k.running = false
	return true
}

// personBack says why the person needs their screen back while it is
// dark, or "" while they do not. A panel that came on without a wake (a
// scrcpy server that exited) is turned off again instead. k.mu must be
// held.
func (k *dark) personBack(ctx context.Context, p phonePower) string {
	switch {
	case !p.locked:
		return "the phone was unlocked"
	case p.occluded:
		return "something shows over the lock screen"
	case k.d.person.touchedSince(k.onAt):
		return "the person touched the phone"
	case p.panel != "" && p.panel != "Off":
		if _, woke := k.power(ctx); woke != "" && woke == k.wokeAt {
			k.logf("the panel came on without a wake: turning it off again")
			if err := k.send(ctx, 0, darkOffTrain(4)); err != nil {
				k.logf("turning the panel off: %v", err)
			}
			return ""
		}
		return "the person turned the screen on"
	}
	return ""
}

// helper returns control-only session i, which turns the panel off and on,
// starting it; k.mu must be held. There are two, since each server makes
// one panel-off call at a time (see darken).
func (k *dark) helper(ctx context.Context, i int) (*scrcpy.Session, error) {
	if h := k.helpers[i]; h != nil {
		select {
		case <-h.Done():
			k.helpers[i], k.warm[i] = nil, false
		default:
			return h, nil
		}
	}
	h, err := scrcpy.Start(ctx, k.d.client, k.d.info.ID, scrcpy.Options{NoVideo: true})
	if err != nil {
		return nil, wrapErr(err)
	}
	k.helpers[i] = h
	return h, nil
}

// send writes control messages through helper i; k.mu must be held.
func (k *dark) send(ctx context.Context, i int, msg []byte) error {
	h, err := k.helper(ctx, i)
	if err != nil {
		return err
	}
	return h.Send(msg)
}

// warmUp starts the helpers and makes each one's first panel-off call,
// which loads classes for up to half a second, while the panel is off
// anyway (an always-on display goes dark a moment early); k.mu must be
// held. The helpers start at the same time.
func (k *dark) warmUp(ctx context.Context) error {
	start := time.Now()
	errs := make([]error, len(k.helpers))
	var wg sync.WaitGroup
	for i := range k.helpers {
		if k.warm[i] {
			continue
		}
		h := k.helpers[i]
		if h != nil {
			select {
			case <-h.Done():
				h = nil
			default:
			}
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if h == nil {
				var err error
				if h, err = scrcpy.Start(ctx, k.d.client, k.d.info.ID, scrcpy.Options{NoVideo: true}); err != nil {
					errs[i] = wrapErr(err)
					return
				}
			}
			lines, cancel := h.SubscribeLogs()
			defer cancel()
			if err := h.Send(scrcpy.SetDisplayPower(false)); err != nil {
				errs[i] = err
			} else if err := waitLine(ctx, lines, 3*time.Second, isPanelOffLine); err != nil {
				errs[i] = err
			}
			k.helpers[i] = h // k.mu is held by warmUp's caller
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			return fmt.Errorf("android: starting the panel control: %w", err)
		}
		k.warm[i] = true
	}
	k.logf("panel control ready in %v", time.Since(start).Round(time.Millisecond))
	return nil
}

// darken wakes the phone and turns its panel off at once; k.mu must be
// held, and the phone asleep and locked.
func (k *dark) darken(ctx context.Context) error {
	if k.broken != nil {
		return k.broken
	}
	// A server that turned the panel off turns it back on as it exits, if
	// the phone is awake by then: let the last helpers finish exiting.
	if wait := 2*time.Second - time.Since(k.endedAt); wait > 0 {
		if err := sleepCtx(ctx, wait); err != nil {
			return err
		}
	}
	if err := k.warmUp(ctx); err != nil {
		return err
	}
	_, before := k.power(ctx)
	start := time.Now()
	if err := k.send(ctx, 0, darkWakeTrain()); err != nil {
		return err
	}
	// The second train leaves a little later: each server always has a
	// panel-off call waiting, and one queued before the power-on runs before
	// it; one that arrives while the panel powers on (from some 30 ms after
	// the key, for about 100 ms on the Mi 10 Pro) waits right behind it.
	// adb carries both sessions over one connection, so the gap survives
	// unless the network bunches the writes.
	time.Sleep(darkTrainGap)
	if err := k.send(ctx, 1, darkOffTrain(darkTrain)); err != nil {
		return err
	}
	k.onAt = start
	k.on.Store(true)
	for try := 0; ; try++ {
		if err := sleepCtx(ctx, 400*time.Millisecond); err != nil {
			return err
		}
		p, err := k.read(ctx)
		if err != nil {
			return err
		}
		if !p.interactive {
			reason, woke := k.power(ctx)
			switch {
			case woke != before:
				// It woke up and went to sleep again: someone pressed the
				// power key meanwhile, or it timed out.
				k.on.Store(false)
				if personSleep(reason) {
					k.hold = "the person turned the phone's screen off"
				}
				return fmt.Errorf("android: the phone went to sleep again at once (%s)", reason)
			case try < 3:
				continue // still waking up
			}
			k.on.Store(false)
			return errors.New("android: the phone did not wake up")
		}
		switch {
		case p.panel == "Off" || p.panel == "":
			_, k.wokeAt = k.power(ctx)
			k.logf("woke the phone with its panel off (%v)", time.Since(start).Round(time.Millisecond))
			return nil
		case try < 2:
			// The panel came on after the train: turn it off again.
			if err := k.send(ctx, 0, darkOffTrain(darkTrain/4)); err != nil {
				return err
			}
			continue
		}
		// The panel stays on: this phone does not take the request. Let it
		// time out by itself and do not try again.
		k.on.Store(false)
		k.broken = fmt.Errorf("%w (its power mode is %s)", errNoDark, p.panel)
		k.logf("%v; background work needs the phone's screen on", k.broken)
		return k.broken
	}
}

// lighten turns the panel back on and ends the dark state; k.mu must be
// held.
func (k *dark) lighten(why string) {
	k.on.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Through both helpers, so that neither turns the panel on again as it
	// exits.
	for i := range k.helpers {
		if err := k.send(ctx, i, scrcpy.SetDisplayPower(true)); err != nil {
			k.logf("turning the panel on (%s): %v", why, err)
			return
		}
	}
	k.logf("turned the panel on: %s", why)
}

// ready gets the phone ready for an agent's call on a display that shares
// its power: it wakes the phone dark when it sleeps, or says why it does
// not.
func (k *dark) ready(ctx context.Context) error {
	if k.on.Load() {
		return nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.readyLocked(ctx)
}

func (k *dark) readyLocked(ctx context.Context) error {
	if k.on.Load() || (k.last.interactive && time.Since(k.lastAt) < darkPoll) {
		return nil
	}
	deadline := time.Now().Add(darkLockWait)
	for {
		p, err := k.read(ctx)
		if err != nil {
			return err
		}
		switch {
		case p.interactive:
			return nil
		case k.hold != "":
			return fmt.Errorf("android: %s, and on this phone (Android %s) a background display stops with the screen; Pinky does not turn the screen on again by itself, so the agent can go on once the person has turned it on: %w",
				k.hold, k.d.info.OSVersion, driver.ErrLocked)
		case k.broken != nil:
			return fmt.Errorf("android: the phone's screen is off, and on this phone (Android %s) a background display stops with the screen (%v): %w",
				k.d.info.OSVersion, k.broken, driver.ErrLocked)
		case p.locked:
			return k.darken(ctx)
		case time.Now().After(deadline):
			return fmt.Errorf("android: the phone's screen is off and the phone is not locked (it may lock a while after the screen goes off, or have no lock screen); on this phone (Android %s) a background display stops with the screen, and Pinky turns the screen on, dark, only on a locked phone: %w",
				k.d.info.OSVersion, driver.ErrLocked)
		}
		if err := sleepCtx(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
}

// undark gives a dark phone its screen back, for the main screen, and
// reports whether it was dark.
func (k *dark) undark() bool {
	if !k.on.Load() {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.on.Load() {
		return false
	}
	k.lighten("Pinky needs the main screen")
	return true
}

// close waits, when the device closes, until run has let the phone time
// out by itself (the lock screen's 10 s after the last keep-awake key), up
// to 20 s.
func (k *dark) close() {
	k.mu.Lock()
	ended := k.ended
	running := k.running
	k.mu.Unlock()
	if !running {
		return
	}
	k.nudge()
	select {
	case <-ended:
	case <-time.After(20 * time.Second):
		k.logf("the phone did not time out before the device closed")
	}
}

// darkWakeTrain is the wake key followed by darkTrain panel-off messages,
// for one write: the server runs them in order, a few milliseconds apart,
// so one of them runs right after the display power controller turned the
// panel on, which happens some tens of milliseconds after the key.
func darkWakeTrain() []byte {
	var b []byte
	b = append(b, scrcpy.InjectKeycode(scrcpy.ActionDown, keycodeWakeup, 0, 0)...)
	b = append(b, scrcpy.InjectKeycode(scrcpy.ActionUp, keycodeWakeup, 0, 0)...)
	return append(b, darkOffTrain(darkTrain)...)
}

func darkOffTrain(n int) []byte {
	var b []byte
	for range n {
		b = append(b, scrcpy.SetDisplayPower(false)...)
	}
	return b
}

func isPanelOffLine(l string) bool { return strings.Contains(l, "Device display turned off") }

// waitLine waits for a server log line that match accepts.
func waitLine(ctx context.Context, lines <-chan string, max time.Duration, match func(string) bool) error {
	t := time.NewTimer(max)
	defer t.Stop()
	for {
		select {
		case l := <-lines:
			if match(l) {
				return nil
			}
		case <-t.C:
			return errors.New("no answer from the scrcpy server")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// displayGroup returns a display's display group.
func (d *Device) displayGroup(ctx context.Context, display int) (int, bool) {
	out, err := d.client.Shell(ctx, d.info.ID, fmt.Sprintf("dumpsys display | grep -m 2 -E 'displayId %d,'", display))
	if err != nil {
		return 0, false
	}
	return parseDisplayGroup(string(out), display)
}

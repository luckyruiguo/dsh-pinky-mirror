package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/DIYgod/pinky/internal/approval"
	"github.com/DIYgod/pinky/internal/driver"
)

// Phones are never kept awake (round three, decision 3): their own screen
// timeout and lock apply between steps and tasks. Before an agent's call
// reads or acts on the phone's screen, the gateway wakes it (driver.Waker);
// a locked phone then shows its lock screen, and the agent gets
// device_locked as before. Unlocking from the computer (decision 5) enters
// the saved PIN through driver.Unlocker, only when the person asks in the
// client, and leaves a step in the task's history, never the PIN.

// wakeFresh is how long a wake counts: the shortest screen timeouts are 15
// s (Android) and 30 s (iOS), so a phone woken this recently is still on
// unless the person turned it off.
const wakeFresh = 5 * time.Second

// wake turns the phone's screen on when it is off, at most once every
// wakeFresh. Errors are left to the read that follows.
func (g *Gateway) wake(ctx context.Context, d *device) {
	g.mu.Lock()
	dev := d.dev
	fresh := time.Since(d.wokeAt) < wakeFresh
	g.mu.Unlock()
	w, ok := dev.(driver.Waker)
	if !ok || fresh {
		return
	}
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := w.Wake(wctx); err != nil {
		if errors.Is(err, driver.ErrOffline) {
			g.setOffline(d, "")
		}
		g.log.Debug("wake", "device", d.id, "err", err)
		return
	}
	g.mu.Lock()
	d.wokeAt = time.Now()
	g.mu.Unlock()
}

// unlockPINTimeout bounds getting the PIN, which waits for the person to
// confirm with Touch ID; unlockTimeout bounds entering it.
const (
	unlockPINTimeout = 2 * time.Minute
	unlockTimeout    = 30 * time.Second
)

// ErrWrongPIN means the phone was still locked after Pinky entered the
// saved PIN. Pinky does not try again: phones lock out or wipe after
// repeated failures.
var ErrWrongPIN = errors.New("the phone is still locked after entering the saved PIN; check the PIN in Settings and unlock on the phone")

// Unlock implements API: it enters the phone's saved PIN, which
// Options.PIN hands out after the person confirms on the computer. It
// never retries.
func (g *Gateway) Unlock(deviceID string) error {
	d, err := g.device(deviceID)
	if err != nil {
		return err
	}
	if g.opt.PIN == nil {
		return ErrNoUnlock
	}
	dev := g.driverOf(d)
	if dev == nil {
		return driver.ErrOffline
	}
	u, ok := dev.(driver.Unlocker)
	if !ok {
		return fmt.Errorf("%w (%s cannot enter a PIN from the computer)", ErrNoUnlock, d.name())
	}
	pctx, pcancel := context.WithTimeout(g.ctx, unlockPINTimeout)
	pin, err := g.opt.PIN(pctx, deviceID)
	pcancel()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(g.ctx, unlockTimeout)
	defer cancel()
	// The phone's own screen shows the PIN pad: nothing of it is recorded,
	// and no agent action runs meanwhile.
	d.act.Lock()
	g.mu.Lock()
	g.inputLocked(d)
	if l := d.lease; l == nil || l.bg == nil {
		g.pauseVideoLocked(d)
	}
	l := d.lease
	g.mu.Unlock()
	// Only a lock screen gets the PIN: a phone unlocked meanwhile would
	// take it as typing in whatever app is in front.
	if locked, known := g.lockedNow(ctx, dev); known && !locked {
		d.act.Unlock()
		g.log.Info("unlock from the computer: the phone is already unlocked", "device", d.id)
		g.setLocked(d, false)
		g.answerUnlockHandoffs(d)
		return nil
	}
	start := time.Now()
	err = u.Unlock(ctx, pin)
	pin = ""
	d.act.Unlock()
	if err != nil {
		// Drivers never put the PIN in their errors.
		g.log.Info("unlock from the computer", "device", d.id, "ok", false, "err", err.Error())
	} else {
		g.log.Info("unlock from the computer", "device", d.id, "ok", true)
	}
	g.recordUnlock(l, start, err)
	if err != nil {
		if errors.Is(err, driver.ErrLocked) {
			g.setLocked(d, true)
			// Both: the client words a wrong PIN by driver.ErrLocked.
			return fmt.Errorf("%w: %w", ErrWrongPIN, driver.ErrLocked)
		}
		if errors.Is(err, driver.ErrPINNotEntered) {
			return fmt.Errorf("Pinky could not get to the passcode screen, so it entered nothing; try again or unlock it on the phone: %w", err)
		}
		return err
	}
	g.checkLock(ctx, d)
	if _, ok := dev.(driver.LockReporter); !ok {
		g.readLock(ctx, d, dev)
	}
	g.answerUnlockHandoffs(d)
	return nil
}

// unlockOnly reports whether a handoff's reason asks the person for nothing
// but unlocking the phone, so that unlocking it from the computer answers
// it. Every clause that asks the person for something has to be about the
// lock: "The phone is locked. Please unlock it." is; "Unlock it, then
// unblock three apps in Settings" is not (field test #26), and stays open
// until the person clicks Done. Courtesies ("thanks"), the agent's own plans
// ("then I'll continue") and purposes ("以便我继续") ask for nothing.
func unlockOnly(reason string) bool {
	lock := false
	for _, c := range clauses(reason) {
		switch {
		case mentionsLock(c):
			lock = true
		case asksNothing(c):
		default:
			return false
		}
	}
	return lock
}

// clauseBreaks split a reason into clauses: punctuation, and words that
// join one request to the next.
var clauseBreaks = strings.NewReplacer(
	".", "\n", ";", "\n", "!", "\n", "?", "\n", ",", "\n", ":", "\n",
	"。", "\n", "；", "\n", "！", "\n", "？", "\n", "，", "\n", "、", "\n", "：", "\n",
	" then ", "\n", " and ", "\n", " after that ", "\n", " afterwards ", "\n", " also ", "\n", " plus ", "\n",
	"然后", "\n", "之后", "\n", "以后", "\n", "接着", "\n", "并且", "\n", "以及", "\n", "同时", "\n",
	"还要", "\n", "再", "\n", "并", "\n", "和", "\n", "后", "\n",
)

func clauses(reason string) []string {
	var out []string
	for _, c := range strings.Split(clauseBreaks.Replace(" "+strings.ToLower(reason)+" "), "\n") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// mentionsLock reports whether a clause is about the phone's lock. A
// passcode alone is not: apps have their own.
func mentionsLock(c string) bool {
	if strings.ContainsAny(c, "锁鎖") || strings.Contains(c, "잠금") || strings.Contains(c, "잠겨") {
		return true
	}
	if i := strings.Index(c, "ロック"); i >= 0 && !strings.HasSuffix(c[:i], "ブ") { // ブロック is a block
		return true
	}
	for _, w := range strings.FieldsFunc(c, func(r rune) bool { return !unicode.IsLetter(r) }) {
		for _, p := range lockWords {
			if strings.HasPrefix(w, p) {
				return true
			}
		}
	}
	return false
}

// lockWords start words about a phone's lock (and never about blocking):
// lock, unlock, entsperren, gesperrt, déverrouiller, desbloquear, разблокировать.
var lockWords = []string{"lock", "unlock", "entsperr", "gesperrt", "sperrbildschirm", "verrouill", "déverrouill",
	"desbloque", "разблокир", "заблокир"}

// asksNothing reports whether a clause asks the person for nothing: a
// courtesy, what the agent will do itself, or what for.
func asksNothing(c string) bool {
	switch c {
	case "please", "thanks", "thank you", "thx", "谢谢", "请", "麻烦了", "麻烦你", "感谢", "辛苦了":
		return true
	}
	for _, p := range []string{
		"so ", "so that ", "in order to ", "to continue", "to go on", "to proceed",
		"i'll ", "i will ", "i can ", "i could ", "i'm going to ", "i am going to ", "let me ", "we'll ", "we can ",
		"以便", "好让", "让我", "方便我", "我来", "我会", "我再", "我就", "我好", "我才", "我可以", "我能", "我继续", "我将", "我接着",
	} {
		if strings.HasPrefix(c+" ", p) {
			return true
		}
	}
	return false
}

// answerUnlockHandoffs answers the handoffs the agent opened for the lock,
// once the phone is unlocked from the computer.
func (g *Gateway) answerUnlockHandoffs(d *device) {
	g.mu.Lock()
	var ids []string
	if !d.locked && d.lease != nil {
		ids = d.lease.unlockHandoffs
		d.lease.unlockHandoffs = nil
	}
	g.mu.Unlock()
	for _, id := range ids {
		_ = g.approvals.Decide(id, approval.AllowOnce, "The phone was unlocked.")
	}
}

// readyNow ends the handoffs opened while the phone waited for a step
// (device_needs_setup), once the gateway has opened it: the step is done.
// The agent gets the screen, as from a hand-back.
func (g *Gateway) readyNow(d *device) {
	g.mu.Lock()
	var ids []string
	if d.online && d.lease != nil {
		ids = d.lease.readyHandoffs
		d.lease.readyHandoffs = nil
	}
	g.mu.Unlock()
	for _, id := range ids {
		_ = g.approvals.Decide(id, approval.AllowOnce, "The phone is ready.")
	}
}

// lockedNow asks the phone whether it is locked, by its LockReporter or a
// read of its screen; known is false when it cannot tell.
func (g *Gateway) lockedNow(ctx context.Context, dev driver.Device) (locked, known bool) {
	if lr, ok := dev.(driver.LockReporter); ok {
		v, err := lr.Locked(ctx)
		return v, err == nil
	}
	rctx, cancel := context.WithTimeout(ctx, observeTimeout)
	defer cancel()
	s, err := dev.Observe(rctx, driver.ObserveOptions{})
	if err != nil || s == nil {
		return false, false
	}
	return s.Locked, true
}

// recordUnlock adds the unlock to the task running on the phone, if any.
func (g *Gateway) recordUnlock(l *lease, start time.Time, err error) {
	if l == nil {
		return
	}
	st := StepView{Text: "Unlocked from the computer", Status: string(StatusSettled), At: start, Elapsed: time.Since(start)}
	switch {
	case errors.Is(err, driver.ErrLocked):
		st.Text, st.Status = "Unlock from the computer failed: the phone did not accept the saved PIN", "wrong_pin"
	case errors.Is(err, driver.ErrPINNotEntered):
		st.Text, st.Status = "Unlock from the computer stopped before entering the PIN", "pin_not_entered"
	case err != nil:
		st.Text, st.Status = "Unlock from the computer failed", string(ErrInternal)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if l.ended {
		return
	}
	g.addStepLocked(l.task, st, "", "")
}

// readLock reads the phone's own screen to learn its lock state, for
// drivers that report it only through reads.
func (g *Gateway) readLock(ctx context.Context, d *device, dev driver.Device) {
	rctx, cancel := context.WithTimeout(ctx, observeTimeout)
	defer cancel()
	s, err := dev.Observe(rctx, driver.ObserveOptions{})
	if err != nil || s == nil {
		return
	}
	g.setLocked(d, s.Locked)
}

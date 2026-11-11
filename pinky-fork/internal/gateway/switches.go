package gateway

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/approval"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// phone_device and phone_launch's language (docs/notes/device-switches.md):
// the switches a developer flips while testing an app. Each changes a
// setting of the phone, so the person is asked under the rule "Phone
// settings" (always on a real phone, on an emulator or Simulator only
// under the stricter presets), read-only agents and read-only mode never
// change one, every change is a step of the task, and when the task ends,
// however it ends, Pinky puts back what it changed, unless the person
// changed it since.

// settingsRule is the rule changing the phone's settings asks under.
const settingsRule = policy.RulePhoneSettings

// changeLanguage names an app's own language in a task's change log, and
// in phone_device's reset.
const changeLanguage = "language"

// putBackTimeout bounds putting back what a task changed.
const putBackTimeout = 30 * time.Second

// DeviceRequest is phone_device. With nothing to set or reset it reads the
// switches.
type DeviceRequest struct {
	Call
	Rotation   string    // a driver.Rotation value, "" leaves it
	Appearance string    // light or dark, "" leaves it
	Location   *Location // nil leaves it
	// Reset puts back what this task changed: switch names, "language"
	// for the apps it opened in another language, or "all".
	Reset []string
}

// Location is a simulated location.
type Location struct {
	Latitude, Longitude float64
}

// SwitchView is a switch in a phone_device result.
type SwitchView struct {
	Name  string // rotation, appearance or location
	Value string // its value now; "" for location when none is simulated
	// Was is the value before this call changed it, when it did.
	Was     string
	Changed bool
	// Unsupported says why the phone has no such switch.
	Unsupported string
}

// change is a setting a task changed, which Pinky puts back when the task
// ends.
type change struct {
	name     string // a driver switch name, or changeLanguage
	was, set string // the switch's value before the task changed it, and the one Pinky set last
	appID    string // changeLanguage: the app, and its name for history
	app      string
	undo     func(context.Context) error
}

// switchSet is one switch a phone_device call sets or puts back.
type switchSet struct {
	name, value, was string
	reset            bool
}

// Device implements phone_device.
func (s *Session) Device(ctx context.Context, req DeviceRequest) *Result {
	want, resets, res := req.check()
	if res != nil {
		return res
	}
	l, res := s.acquire(req.Device)
	if res != nil {
		return res
	}
	defer s.done(l)
	g := s.g
	ctx, cancel := g.callCtx(ctx, l)
	defer cancel()
	changing := len(want) > 0 || len(resets) > 0
	if res := g.gate(l, req.ApprovalID, changing); res != nil {
		return g.finish(l, res)
	}
	d := l.dev
	dev := g.driverOf(d)
	if dev == nil {
		return g.finish(l, errResult(ErrDeviceOffline, "%s is offline.", d.name()))
	}
	sw, _ := dev.(driver.Switcher)
	note := ""
	if sw == nil && len(want) > 0 {
		// An iPhone or iPad over CoreDevice: the helper has the switches
		// (helperneed.go), once the change may happen at all.
		if res := g.switchRefusal(l, want, req.args(), time.Now()); res != nil {
			return g.finish(l, res)
		}
		res, n, now := g.helperFor(ctx, l, switchNeed(want))
		if res != nil {
			return g.finish(l, res)
		}
		note = n
		sw, _ = now.(driver.Switcher)
	}
	if !changing {
		about := g.about(ctx, d)
		if sw == nil {
			if about == nil {
				return g.finish(l, errResult(ErrUnsupported, "%s", noSwitches(d.name())))
			}
			return g.finish(l, &Result{Status: StatusSettled, About: about, Message: noSwitches(d.name())})
		}
		res := g.readSwitches(ctx, l, sw)
		if !res.IsError() {
			res.About = about
		}
		return g.finish(l, res)
	}
	return g.finish(l, withNote(g.setSwitches(ctx, l, req, sw, want, resets), note))
}

// switchRefusal refuses changing switches to a read-only or watch-only
// agent, a read-only task and a phone in read-only mode, recording the
// last two as a step; nil when the change may go on. Device calls it before
// anything moves the phone to the helper for the change (helperneed.go).
func (g *Gateway) switchRefusal(l *lease, want []switchSet, args string, start time.Time) *Result {
	d := l.dev
	switch g.scopeOf(l.session).Mode {
	case ScopeReadOnly, ScopeWatchOnly:
		return errResult(ErrScopeDenied, "This agent may not change the phone's settings: the person gave it %s access. Nothing was done.",
			strings.ReplaceAll(string(g.scopeOf(l.session).Mode), "_", "-"))
	}
	g.mu.Lock()
	readOnly := l.readOnly
	g.mu.Unlock()
	if readOnly || g.policy.ReadOnly(d.id) {
		res := &Result{Status: StatusBlocked, Rule: policy.RuleReadOnly, Message: "Read-only mode is on, so the phone's settings stay as they are. Nothing was done."}
		g.recordSwitch(l, args, fmt.Sprintf("Blocked by “%s”: %s", policy.RuleReadOnly, lower(switchSteps(want))), res, start)
		return res
	}
	return nil
}

// check validates the request and returns the switches to set, in
// driver.Switches order, and the names to put back.
func (req DeviceRequest) check() (want []switchSet, resets []string, res *Result) {
	bad := func(format string, args ...any) *Result { return errResult(ErrInvalidArgument, format, args...) }
	if v := req.Rotation; v != "" {
		switch v {
		case driver.RotationPortrait, driver.RotationLandscape, driver.RotationUpsideDown, driver.RotationReverseLandscape, driver.RotationAuto:
		default:
			return nil, nil, bad("rotation must be portrait, landscape, upside_down, reverse_landscape or auto.")
		}
		want = append(want, switchSet{name: driver.SwitchRotation, value: v})
	}
	if v := req.Appearance; v != "" {
		if v != driver.AppearanceLight && v != driver.AppearanceDark {
			return nil, nil, bad("appearance must be light or dark.")
		}
		want = append(want, switchSet{name: driver.SwitchAppearance, value: v})
	}
	if loc := req.Location; loc != nil {
		if math.IsNaN(loc.Latitude) || math.IsNaN(loc.Longitude) || loc.Latitude < -90 || loc.Latitude > 90 || loc.Longitude < -180 || loc.Longitude > 180 {
			return nil, nil, bad("latitude must be between -90 and 90, and longitude between -180 and 180.")
		}
		want = append(want, switchSet{name: driver.SwitchLocation, value: driver.FormatLocation(loc.Latitude, loc.Longitude)})
	}
	seen := map[string]bool{}
	for _, r := range req.Reset {
		r = strings.ToLower(strings.TrimSpace(r))
		names := []string{r}
		switch r {
		case "all":
			names = append(append([]string(nil), driver.Switches...), changeLanguage)
		case driver.SwitchRotation, driver.SwitchAppearance, driver.SwitchLocation, changeLanguage:
		default:
			return nil, nil, bad("reset takes rotation, appearance, location, language or all, not %q.", r)
		}
		for _, n := range names {
			for _, w := range want {
				if w.name == n {
					return nil, nil, bad("%s is both set and reset; do one.", n)
				}
			}
			if !seen[n] {
				seen[n] = true
				resets = append(resets, n)
			}
		}
	}
	return want, resets, nil
}

func noSwitches(name string) string {
	return name + " has no switches Pinky can set: phone_device works on Android phones and emulators, iOS Simulators, and iPhones and iPads through the WebDriverAgent helper. " +
		"On an iPhone or iPad left on Automatic in Settings › Phones, a change moves the task to the helper by itself when it is installed."
}

// readSwitches reads every switch.
func (g *Gateway) readSwitches(ctx context.Context, l *lease, sw driver.Switcher) *Result {
	var views []SwitchView
	for _, name := range driver.Switches {
		v, err := sw.Switch(ctx, name)
		switch {
		case errors.Is(err, driver.ErrUnsupported):
			views = append(views, SwitchView{Name: name, Unsupported: unsupportedWhy(err)})
			continue
		case err != nil:
			return g.switchError(l, err)
		}
		views = append(views, SwitchView{Name: name, Value: v})
	}
	return &Result{Status: StatusSettled, Switches: views, Message: "These are the phone's switches now; nothing was changed."}
}

// setSwitches sets and puts back the switches of a phone_device call.
func (g *Gateway) setSwitches(ctx context.Context, l *lease, req DeviceRequest, sw driver.Switcher, want []switchSet, resets []string) *Result {
	d := l.dev
	start := time.Now()
	args := req.args()
	if len(want) > 0 {
		if sw == nil {
			return errResult(ErrUnsupported, "%s", noSwitches(d.name()))
		}
		if res := g.switchRefusal(l, want, args, start); res != nil {
			return res
		}
	}
	// Read every switch first: one the phone lacks fails the call before
	// anything changes or the person is asked.
	var sets []switchSet
	var views []SwitchView
	for _, w := range want {
		was, err := sw.Switch(ctx, w.name)
		if err != nil {
			return g.switchError(l, err)
		}
		w.was = was
		if sameValue(w.name, was, w.value) {
			views = append(views, SwitchView{Name: w.name, Value: was})
			continue
		}
		sets = append(sets, w)
	}
	if len(sets) > 0 {
		if res := g.askSettings(ctx, l, req, sets, args); res != nil {
			return res
		}
	}
	d.act.Lock()
	defer d.act.Unlock()
	if res := g.gate(l, req.ApprovalID, true); res != nil {
		return res
	}
	if len(sets) > 0 && g.rotates(sets) {
		if res := g.leaveForRotation(ctx, l); res != nil {
			return res
		}
	}
	var done []string
	changedScreen := false
	for _, w := range sets {
		if err := sw.SetSwitch(ctx, w.name, w.value); err != nil {
			res := g.switchError(l, err)
			if len(done) > 0 {
				res.Message = fmt.Sprintf("%s Already done: %s.", res.Message, lower(joinSteps(done)))
			}
			g.recordSwitch(l, args, "Failed: "+lower(switchStep(w)), res, start)
			res.Switches = views
			return res
		}
		g.mu.Lock()
		g.noteSwitchLocked(l, w.name, w.was, w.value)
		g.mu.Unlock()
		text := switchStep(w)
		g.recordSwitch(l, args, text, &Result{Status: StatusSettled}, start)
		done = append(done, text)
		views = append(views, SwitchView{Name: w.name, Value: w.value, Was: w.was, Changed: true})
		changedScreen = changedScreen || w.name != driver.SwitchLocation
	}
	for _, name := range resets {
		put, err := g.resetChange(ctx, l, sw, name, args, start)
		if err != nil {
			res := g.switchError(l, err)
			res.Switches = views
			return res
		}
		for _, p := range put {
			done = append(done, p.text)
			if p.view != nil {
				views = append(views, *p.view)
			}
			changedScreen = changedScreen || p.view == nil || p.view.Name != driver.SwitchLocation
		}
	}
	res := &Result{Status: StatusSettled, Switches: views}
	switch {
	case len(done) == 0 && len(resets) > 0 && len(want) == 0:
		res.Message = "This task changed none of these; nothing was put back."
	case len(done) == 0:
		res.Message = "The phone was already so; nothing was changed."
	default:
		res.Message = joinSteps(done) + "."
		if len(sets) > 0 {
			res.Message += " Pinky puts the phone's settings back when the task ends; reset puts them back sooner."
		}
	}
	if !changedScreen {
		return res
	}
	// Turning and switching light or dark lay the screen out again.
	g.settleAction(ctx, d, g.opt.SettleMax)
	cur, err := g.observe(ctx, d, false)
	if err != nil {
		return res
	}
	return g.attachScreen(l, res, cur)
}

// rotates reports whether the call turns the screen.
func (g *Gateway) rotates(sets []switchSet) bool {
	for _, w := range sets {
		if w.name == driver.SwitchRotation {
			return true
		}
	}
	return false
}

// leaveForRotation moves a task on a background display to the phone's
// own screen before the screen turns: the background display does not
// turn with it, and the agent tests the app as people see it.
func (g *Gateway) leaveForRotation(ctx context.Context, l *lease) *Result {
	g.mu.Lock()
	onBG := l.bg != nil
	g.mu.Unlock()
	if !onBG {
		return nil
	}
	_, res := g.leaveForPhone(ctx, l, "A background display does not turn", "nothing was changed")
	return res
}

// askSettings asks the person before the phone's settings change, unless
// asking does not apply or they allowed it for the task. It returns nil
// once allowed.
func (g *Gateway) askSettings(ctx context.Context, l *lease, req DeviceRequest, sets []switchSet, args string) *Result {
	if !g.settingsAsk(l) {
		return nil
	}
	s := l.session
	var key strings.Builder
	for _, w := range sets {
		fmt.Fprintf(&key, "%s=%s\x00", w.name, w.value)
	}
	b := approval.Binding{Session: s.id, Device: l.dev.id, Lease: l.id, Tool: "phone_device", Args: digest(key.String())}
	id := req.ApprovalID
	if id != "" {
		if res := g.checkApproval(id, b); res != nil {
			if res.Error != "" {
				return res
			}
			id = ""
		}
	}
	var info approval.Info
	if id == "" {
		title := switchSteps(sets) + "?"
		detail := s.agent.Name + " · Pinky puts it back when the task ends"
		if c := strings.TrimSpace(req.Intent); c != "" {
			detail = s.agent.Name + " · " + c
		}
		info = g.approvals.Open(approval.Request{Kind: approval.KindAction, Binding: b, Rule: settingsRule, Title: title,
			Detail: detail, Agent: s.agent.Name, TaskID: l.task.id, DeviceName: l.dev.name(),
			Data: &standingAsk{rule: settingsRule, agent: s.agent.Name, device: l.dev.id, deviceName: l.dev.name()}})
		if res := g.afterOpen(l, info); res != nil {
			return res
		}
		g.openStep(l, info, "phone_device", args, "Waiting for you: "+switchSteps(sets), screen.Rect{}, screen.Point{}, "")
	} else {
		info, _ = g.approvals.Get(id)
	}
	out, err := g.waitApproval(ctx, l, info.ID, info, req.Progress)
	if errors.Is(err, errTaken) {
		return takenResult(info.ID)
	}
	if err != nil {
		return g.cancelled(l, info.ID)
	}
	switch {
	case out.State == approval.Open:
		return g.pendingResult(l, info)
	case out.State == approval.Withdrawn:
		g.closeStep(info.ID, string(StatusDenied), 0)
		return g.withdrawnResult(l, out)
	}
	g.recordDecision(info, out)
	if !out.Decision.Allows() {
		g.closeStep(info.ID, string(StatusDenied), out.Waited)
		return &Result{Status: StatusDenied, Rule: settingsRule, Note: denyNote(out), Message: "The person did not allow changing the phone's settings. Nothing was done."}
	}
	g.closeStep(info.ID, "allowed", out.Waited)
	g.setTaskState(l, TaskWorking)
	if out.Decision != approval.AllowOnce {
		g.mu.Lock()
		l.settingsGranted = true
		g.mu.Unlock()
	}
	return nil
}

// settingsAsk reports whether changing the phone's settings asks the
// person now: always on a real phone, whose settings are the person's
// own; on an emulator or a Simulator only when they asked to approve more
// than payments. Allow for this task, here or for a launch in another
// language, ends the asking for the task; Always allow ends it for the
// agent on this phone (standing.go).
func (g *Gateway) settingsAsk(l *lease) bool {
	if g.standingAllowed(l, settingsRule, screen.App{}) {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if settingsGrantedLocked(l) {
		return false
	}
	if !l.dev.info.Virtual {
		return true
	}
	switch g.policy.Preset() {
	case policy.PresetCommits, policy.PresetEvery:
		return true
	}
	return false
}

// settingsGrantedLocked reports whether the person allowed changing the
// phone's settings for the rest of the task. Callers hold g.mu.
func settingsGrantedLocked(l *lease) bool {
	if l.settingsGranted {
		return true
	}
	for k, ok := range l.grants {
		if ok && strings.HasPrefix(k, settingsRule+"\x00") {
			return true
		}
	}
	return false
}

// settingsVerdict applies "Phone settings" to phone_launch with a
// language or region: read-only agents and read-only mode refuse it, and
// it asks where changing the phone's settings asks.
func (g *Gateway) settingsVerdict(l *lease, a Action, p *prep, v policy.Verdict) policy.Verdict {
	if a.Tool != "launch" || !a.localized() || v.Decision == policy.Deny {
		return v
	}
	if p.input.ReadOnly || g.policy.ReadOnly(l.dev.id) {
		mode := g.scopeOf(l.session).Mode
		return policy.Verdict{Decision: policy.Deny, Rule: policy.RuleReadOnly, Scope: mode == ScopeReadOnly || mode == ScopeWatchOnly,
			Reason: "Read-only mode is on, and opening an app in another language changes the phone's settings."}
	}
	if !g.settingsAsk(l) {
		return v
	}
	if v.Decision == policy.Ask {
		v.Asks = append(append([]string(nil), v.Asks...), settingsRule)
		return v
	}
	return policy.Verdict{Decision: policy.Ask, Rule: settingsRule, Asks: []string{settingsRule}, Exempted: v.Exempted,
		Reason: "Opening an app in another language changes the phone's settings until the task ends."}
}

// putBack is one change a reset put back, or left as the person set it.
type putBack struct {
	text string
	view *SwitchView // nil for an app's language
}

// resetChange puts back what the task changed of one switch, or the
// languages of the apps it opened in another one.
func (g *Gateway) resetChange(ctx context.Context, l *lease, sw driver.Switcher, name, args string, start time.Time) ([]putBack, error) {
	g.mu.Lock()
	var mine []change
	keep := l.changes[:0:0]
	for _, c := range l.changes {
		if c.name == name {
			mine = append(mine, c)
		} else {
			keep = append(keep, c)
		}
	}
	l.changes = keep
	g.mu.Unlock()
	var out []putBack
	for i := len(mine) - 1; i >= 0; i-- {
		c := mine[i]
		text, err := g.putBackOne(ctx, sw, c)
		if err != nil && !errors.Is(err, driver.ErrSwitchChanged) {
			// It stays in the log, and the end of the task tries again,
			// unless the phone cannot put it back at all.
			keep, code := mine[:i+1], ErrInternal
			if errors.Is(err, driver.ErrUnsupported) {
				keep, code = mine[:i], ErrUnsupported
			}
			g.mu.Lock()
			l.changes = append(l.changes, keep...)
			g.mu.Unlock()
			g.recordSwitch(l, args, text, &Result{Error: code}, start)
			return out, err
		}
		g.recordSwitch(l, args, text, &Result{Status: StatusSettled}, start)
		p := putBack{text: text}
		if c.name != changeLanguage {
			p.view = &SwitchView{Name: c.name, Value: c.was, Was: c.set, Changed: true}
			if err != nil { // the person changed it since: it stays
				now, _ := sw.Switch(ctx, c.name)
				p.view = &SwitchView{Name: c.name, Value: now}
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// noteSwitchLocked adds a switch the task set to its change log: its first
// value stays what is put back. Callers hold g.mu.
func (g *Gateway) noteSwitchLocked(l *lease, name, was, set string) {
	for i, c := range l.changes {
		if c.name != name {
			continue
		}
		if sameValue(name, c.was, set) {
			l.changes = append(l.changes[:i:i], l.changes[i+1:]...)
		} else {
			l.changes[i].set = set
		}
		return
	}
	l.changes = append(l.changes, change{name: name, was: was, set: set})
}

// noteLanguageLocked adds an app the task opened in another language to
// its change log. Opened again in another one, the newer undo runs first,
// as each puts back the language before it. Callers hold g.mu.
func (g *Gateway) noteLanguageLocked(l *lease, app driver.App, undo func(context.Context) error) {
	for i, c := range l.changes {
		if c.name == changeLanguage && c.appID == app.ID {
			older := c.undo
			l.changes[i].undo = func(ctx context.Context) error {
				if err := undo(ctx); err != nil {
					return err
				}
				return older(ctx)
			}
			return
		}
	}
	name := app.Name
	if name == "" {
		name = app.ID
	}
	l.changes = append(l.changes, change{name: changeLanguage, appID: app.ID, app: name, undo: undo})
}

// putBackLocked puts back what the lease's task changed, newest first,
// once the lease ended: after the agent's last call, under the device's
// action lock, so the next task starts from the phone as it was. Callers
// hold g.mu.
func (g *Gateway) putBackLocked(l *lease) {
	changes := l.changes
	l.changes = nil
	if len(changes) == 0 {
		return
	}
	d, t := l.dev, l.task
	g.shots.add()
	go func() {
		defer g.shots.done()
		d.act.Lock()
		defer d.act.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), putBackTimeout)
		defer cancel()
		g.mu.Lock()
		dev := d.dev
		g.mu.Unlock()
		sw, _ := dev.(driver.Switcher)
		for i := len(changes) - 1; i >= 0; i-- {
			c := changes[i]
			start := time.Now()
			status := string(StatusSettled)
			var text string
			if dev == nil {
				text, status = notBackText(c), string(ErrDeviceOffline)
			} else {
				var err error
				text, err = g.putBackOne(ctx, sw, c)
				switch {
				case err == nil, errors.Is(err, driver.ErrSwitchChanged):
				case errors.Is(err, driver.ErrUnsupported):
					status = string(ErrUnsupported) // an emulator's location: it cannot stop
				case errors.Is(err, driver.ErrOffline):
					status = string(ErrDeviceOffline)
				default:
					status = string(ErrInternal)
					g.log.Warn("put back a setting", "device", d.id, "setting", c.name, "app", c.appID, "err", err)
				}
			}
			g.mu.Lock()
			g.addStepLocked(t, StepView{Tool: "phone_device", Text: text, Status: status, At: start, Elapsed: time.Since(start)}, "", "")
			g.mu.Unlock()
		}
	}()
}

// putBackOne puts one change back and says what happened. A switch the
// person changed since stays as they set it (driver.ErrSwitchChanged).
func (g *Gateway) putBackOne(ctx context.Context, sw driver.Switcher, c change) (string, error) {
	if c.name == changeLanguage {
		err := c.undo(ctx)
		switch {
		case err == nil:
			return fmt.Sprintf("Put the language of %s back", c.app), nil
		case errors.Is(err, driver.ErrSwitchChanged):
			return fmt.Sprintf("Left the language of %s as the person set it", c.app), err
		}
		return notBackText(c), err
	}
	if sw == nil {
		return notBackText(c), driver.ErrOffline
	}
	now, err := sw.Switch(ctx, c.name)
	if err != nil {
		return notBackText(c), err
	}
	if !sameValue(c.name, now, c.set) {
		return fmt.Sprintf("Left %s as the person set it", switchNoun(c.name)), driver.ErrSwitchChanged
	}
	if err := sw.SetSwitch(ctx, c.name, c.was); err != nil {
		return notBackText(c), err
	}
	if c.name == driver.SwitchLocation && c.was == "" {
		return "Stopped the simulated location", nil
	}
	return fmt.Sprintf("Put %s back to %s", switchNoun(c.name), valueWords(c.name, c.was)), nil
}

func notBackText(c change) string {
	if c.name == changeLanguage {
		return fmt.Sprintf("Could not put the language of %s back", c.app)
	}
	return fmt.Sprintf("Could not put %s back to %s", switchNoun(c.name), valueWords(c.name, c.was))
}

// switchStep says setting a switch, as history and approvals word it:
// "Set the appearance to dark".
func switchStep(w switchSet) string {
	return fmt.Sprintf("Set %s to %s", switchNoun(w.name), valueWords(w.name, w.value))
}

// switchSteps says several, "Set the screen's rotation to landscape; set
// the appearance to dark".
func switchSteps(sets []switchSet) string {
	parts := make([]string, len(sets))
	for i, w := range sets {
		parts[i] = switchStep(w)
		if i > 0 {
			parts[i] = lower(parts[i])
		}
	}
	return strings.Join(parts, "; ")
}

// joinSteps makes one sentence of step texts: "Set the appearance to dark;
// set the location to …".
func joinSteps(steps []string) string {
	out := make([]string, len(steps))
	for i, s := range steps {
		if i > 0 {
			s = lower(s)
		}
		out[i] = s
	}
	return strings.Join(out, "; ")
}

func lower(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func switchNoun(name string) string {
	switch name {
	case driver.SwitchRotation:
		return "the screen's rotation"
	case driver.SwitchAppearance:
		return "the appearance"
	case driver.SwitchLocation:
		return "the location"
	}
	return name
}

// valueWords says a switch's value in words: "upside down", "auto-rotate",
// "35.6812, 139.7671".
func valueWords(name, v string) string {
	switch {
	case name == driver.SwitchLocation && v == "":
		return "none"
	case name == driver.SwitchLocation:
		return strings.Replace(v, ",", ", ", 1)
	case name == driver.SwitchRotation && v == driver.RotationAuto:
		return "auto-rotate"
	case name == driver.SwitchAppearance && v == driver.AppearanceAuto:
		return "automatic"
	}
	return strings.ReplaceAll(v, "_", " ")
}

// sameValue compares two values of a switch; locations match within a
// few centimetres, as phones round them.
func sameValue(name, a, b string) bool {
	if name != driver.SwitchLocation || a == "" || b == "" {
		return a == b
	}
	alat, alon, err1 := driver.ParseLocation(a)
	blat, blon, err2 := driver.ParseLocation(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	return math.Abs(alat-blat) < 1e-6 && math.Abs(alon-blon) < 1e-6
}

func (req DeviceRequest) args() string {
	m := map[string]any{}
	if req.Rotation != "" {
		m["rotation"] = req.Rotation
	}
	if req.Appearance != "" {
		m["appearance"] = req.Appearance
	}
	if req.Location != nil {
		m["location"] = map[string]float64{"latitude": req.Location.Latitude, "longitude": req.Location.Longitude}
	}
	if len(req.Reset) > 0 {
		m["reset"] = req.Reset
	}
	if req.Intent != "" {
		m["intent"] = req.Intent
	}
	if req.ApprovalID != "" {
		m["approval_id"] = req.ApprovalID
	}
	return argsJSON(m)
}

// recordSwitch adds a phone_device step to the task.
func (g *Gateway) recordSwitch(l *lease, args, text string, res *Result, start time.Time) {
	status := string(res.Status)
	if res.Error != "" {
		status = string(res.Error)
	}
	g.addStep(l, StepView{Tool: "phone_device", Args: args, Text: text, Status: status, At: start, Elapsed: time.Since(start)})
}

func (g *Gateway) switchError(l *lease, err error) *Result {
	switch {
	case errors.Is(err, driver.ErrUnsupported):
		return errResult(ErrUnsupported, "%s Nothing more was changed.", unsupportedWhy(err))
	case errors.Is(err, driver.ErrOffline):
		g.setOffline(l.dev, "")
		return errResult(ErrDeviceOffline, "%s is offline.", l.dev.name())
	case errors.Is(err, context.Canceled):
		return errResult(ErrInternal, "The call was cancelled.")
	case errors.Is(err, context.DeadlineExceeded):
		return errResult(ErrInternal, "The phone did not answer in time (%v). Check the phone before trying again.", err)
	}
	return errResult(ErrInternal, "%v", err)
}

// unsupportedWhy turns a driver's ErrUnsupported into the sentence it
// carries: "android: rotation \"x\": not supported" → "Rotation \"x\": not
// supported.".
func unsupportedWhy(err error) string {
	s := strings.TrimSpace(err.Error())
	s = reDriverPrefix.ReplaceAllString(s, "")
	s = strings.TrimSuffix(s, ": "+driver.ErrUnsupported.Error())
	if pre, ok := strings.CutPrefix(s, driver.ErrUnsupported.Error()+": "); ok {
		s = pre
	}
	s = capitalize(s)
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

var reDriverPrefix = regexp.MustCompile(`^(android|ios|demo): `)

// localized reports whether a phone_launch opens the app in another
// language or region.
func (a Action) localized() bool { return a.Language != "" || a.Region != "" }

// locale returns the launch's language and region, checked by check.
func (a Action) locale() driver.Locale {
	loc, _ := parseLocale(a.Language, a.Region)
	return loc
}

// parseLocale reads phone_launch's language and region into their usual
// case, "ja", "zh-Hans", "JP". A region inside the language ("pt-BR")
// becomes the region.
func parseLocale(language, region string) (driver.Locale, error) {
	var loc driver.Locale
	region = strings.TrimSpace(region)
	if region != "" {
		if !reRegion.MatchString(region) {
			return loc, fmt.Errorf("region %q is not a two-letter region code such as JP or BR", region)
		}
		loc.Region = strings.ToUpper(region)
	}
	language = strings.ReplaceAll(strings.TrimSpace(language), "_", "-")
	if language == "" {
		return loc, nil
	}
	parts := strings.Split(language, "-")
	if !reLanguage.MatchString(parts[0]) {
		return loc, fmt.Errorf("language %q is not a language tag such as ja, zh-Hans or pt-BR", language)
	}
	keep := []string{strings.ToLower(parts[0])}
	for _, p := range parts[1:] {
		switch {
		case reScript.MatchString(p):
			keep = append(keep, strings.ToUpper(p[:1])+strings.ToLower(p[1:]))
		case reRegion.MatchString(p):
			r := strings.ToUpper(p)
			if loc.Region != "" && loc.Region != r {
				return loc, fmt.Errorf("language %q names the region %s and region %s another; give one", language, r, loc.Region)
			}
			loc.Region = r
		default:
			return loc, fmt.Errorf("language %q is not a language tag such as ja, zh-Hans or pt-BR", language)
		}
	}
	loc.Language = strings.Join(keep, "-")
	return loc, nil
}

var (
	reLanguage = regexp.MustCompile(`^[A-Za-z]{2,3}$`)
	reScript   = regexp.MustCompile(`^[A-Za-z]{4}$`)
	reRegion   = regexp.MustCompile(`^(?:[A-Za-z]{2}|[0-9]{3})$`)
)

// localeWords says a launch's language: " in ja-JP", " with region JP",
// or "" for none.
func localeWords(a Action) string {
	if !a.localized() {
		return ""
	}
	loc := a.locale()
	if loc.Language == "" {
		return " with region " + loc.Region
	}
	return " in " + loc.Tag()
}

// DeviceSwitch reads one of a phone's switches for the person, such as
// the screen's rotation the ⋯ menu under the mirror marks.
func (g *Gateway) DeviceSwitch(ctx context.Context, deviceID, name string) (string, error) {
	_, sw, err := g.personSwitcher(deviceID)
	if err != nil || sw == nil {
		return "", err // sw nil: over CoreDevice, which cannot read it
	}
	return sw.Switch(ctx, name)
}

// SetDeviceSwitch sets one of a phone's switches for the person: Rotate in
// the ⋯ menu under the mirror. It is the person's own input, like a touch
// on the mirror: it pauses an agent task on the phone's own screen, since
// its screen turns under the agent, but not one on a background display,
// which does not turn; while the person is in control (Take over, or an
// agent's handoff), it only turns the screen. Nobody is asked, and nothing
// is put back when a task ends: a task that changed the switch earlier
// leaves it as the person set it. An iPhone or iPad over CoreDevice with
// the helper installed moves to it first (personHelper), which takes a few
// seconds.
func (g *Gateway) SetDeviceSwitch(ctx context.Context, deviceID, name, value string) error {
	if err := validSwitch(name, value); err != nil {
		return err
	}
	d, now, err := g.personSwitcher(deviceID)
	if err != nil {
		return err
	}
	_, dev, err := g.personInput(deviceID)
	if err != nil {
		return err
	}
	if now == nil {
		if dev, err = g.personHelper(ctx, d); err != nil {
			return err
		}
	}
	sw, ok := dev.(driver.Switcher)
	if !ok {
		return fmt.Errorf("%s: %w", name, driver.ErrUnsupported)
	}
	return sw.SetSwitch(ctx, name, value)
}

// personSwitcher returns a phone's switches, before anything pauses. It
// returns no switches and no error for an iPhone or iPad that has them
// once it moves to the helper (helperReadyLocked).
func (g *Gateway) personSwitcher(deviceID string) (*device, driver.Switcher, error) {
	d, err := g.device(deviceID)
	if err != nil {
		return nil, nil, err
	}
	g.mu.Lock()
	dev, ready := d.dev, g.helperReadyLocked(d)
	g.mu.Unlock()
	if dev == nil {
		return nil, nil, driver.ErrOffline
	}
	sw, ok := dev.(driver.Switcher)
	switch {
	case ok:
		return d, sw, nil
	case ready:
		return d, nil, nil
	}
	return nil, nil, fmt.Errorf("%s: %w", noSwitches(d.name()), driver.ErrUnsupported)
}

// validSwitch checks a value the person sets: the values phone_device
// takes, and Android's automatic light and dark.
func validSwitch(name, value string) error {
	ok := false
	switch name {
	case driver.SwitchRotation:
		switch value {
		case driver.RotationPortrait, driver.RotationLandscape, driver.RotationUpsideDown, driver.RotationReverseLandscape, driver.RotationAuto:
			ok = true
		}
	case driver.SwitchAppearance:
		ok = value == driver.AppearanceLight || value == driver.AppearanceDark
	case driver.SwitchLocation:
		_, _, err := driver.ParseLocation(value)
		ok = value == "" || err == nil
	}
	if !ok {
		return fmt.Errorf("%s %q: %w", name, value, driver.ErrUnsupported)
	}
	return nil
}

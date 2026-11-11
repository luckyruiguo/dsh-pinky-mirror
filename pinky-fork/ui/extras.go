package ui

import (
	"bytes"
	"context"
	"image"
	_ "image/jpeg" // step screenshots are JPEG
	_ "image/png"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// Methods the gateway has beyond gateway.API. The client checks for them
// on the API value and leaves out what they back when they are missing,
// so gateway.API stays as it is. The signatures are internal/gateway's.
type (
	mediaPather interface {
		MediaPath(ctx context.Context, id string) (string, error)
	}
	agentLister interface {
		Agents(ctx context.Context) ([]gateway.AgentView, error)
	}
	agentScoper interface {
		SetAgentScope(name string, scope gateway.Scope)
	}
	readOnlySetter interface {
		SetReadOnly(deviceID string, on bool) error
	}
	historyWiper interface {
		WipeHistory(ctx context.Context) error
	}
	// exemptionKeeper lists the person's Always allow answers for Settings ›
	// Safety, and takes them back.
	exemptionKeeper interface {
		Exemptions() []gateway.Exemption
		RemoveExemption(x gateway.Exemption) error
	}
	// rulesEditor reads and replaces the rules file's rules, for Settings ›
	// Safety › Rules. SetRules takes the rules the change was made from,
	// and refuses it (gateway.ErrRulesChanged) when rules.yaml was edited
	// by hand since.
	rulesEditor interface {
		Rules() gateway.RulesView
		SetRules(base, rules []gateway.RuleView) error
	}
	// fileProblemLister lists the rules files edited by hand that the
	// gateway cannot use, which Settings shows until they are fixed.
	fileProblemLister interface {
		FileProblems() []gateway.FileProblem
	}

	// Round three: pairing, the companion app, background displays,
	// recordings, retention and export.
	pairer interface {
		StartPairing(ctx context.Context) (driver.Pairing, error)
		PairWithCode(ctx context.Context, addr, code string) (driver.Info, error)
	}
	// wifiConnector connects a phone that paired but that Pinky could not
	// find on the network, at the address it shows.
	wifiConnector interface {
		ConnectWiFi(ctx context.Context, addr string) (driver.Info, error)
	}
	companionInstaller interface {
		CompanionState(ctx context.Context, deviceID string) (driver.CompanionState, error)
		InstallCompanion(ctx context.Context, deviceID string) error
	}
	// helperInstaller installs the iPhone helper from the window, on a Mac
	// with Xcode (helper.go).
	helperInstaller interface {
		CanInstallHelper() bool
		InstallHelper(ctx context.Context, deviceID, team string, progress func(driver.HelperStep)) error
	}
	backgroundSetter interface {
		SetBackground(deviceID string, on bool) error
		Background(deviceID string) bool
	}
	// connectionChooser is how Pinky connects to a phone, for Settings ›
	// Phones: iPhones can go through CoreDevice or the WebDriverAgent helper.
	connectionChooser interface {
		Connections(ctx context.Context, deviceID string) (driver.Connections, error)
		SetConnection(deviceID, way string) error
	}
	agentStreamer interface {
		AgentStream(ctx context.Context, deviceID string, opt driver.StreamOptions) (driver.Stream, error)
	}
	recordingFinder interface {
		Recording(ctx context.Context, taskID string) (gateway.RecordingView, error)
	}
	recordingSetter interface {
		SetRecording(on bool)
	}
	retentionSetter interface {
		SetRetention(days int)
	}
	historyExporter interface {
		ExportHistory(ctx context.Context, taskIDs []string, dir string) (string, error)
	}
	// recordingPlayer plays a recording without a file, as fakes do; the
	// client plays the others' files with recording.Open.
	recordingPlayer interface {
		OpenRecording(ctx context.Context, rec gateway.RecordingView, from time.Duration) (driver.Stream, error)
	}
)

// mediaEntry is a step screenshot, loaded once.
type mediaEntry struct {
	bitmap  *mg.Bitmap
	w, h    int
	err     error
	loading bool
}

// media returns a step screenshot by its media id, loading it in the
// background the first time: nil while it loads, or when the gateway has
// no screenshots.
func (a *app) media(id string) *mediaEntry {
	if id == "" {
		return nil
	}
	mp, ok := a.api.(mediaPather)
	if !ok {
		return nil
	}
	if e, ok := a.mediaCache[id]; ok {
		if e.bitmap == nil {
			return nil
		}
		return e
	}
	if len(a.mediaCache) > 400 {
		a.mediaCache = map[string]*mediaEntry{} // keep memory bounded; they load again
	}
	e := &mediaEntry{loading: true}
	a.mediaCache[id] = e
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
		defer cancel()
		var img image.Image
		path, err := mp.MediaPath(ctx, id)
		if err == nil {
			var data []byte
			if data, err = os.ReadFile(path); err == nil {
				img, _, err = image.Decode(bytes.NewReader(data))
			}
		}
		a.post(func() {
			e.loading = false
			if err != nil {
				e.err = err
				return
			}
			e.bitmap = mg.NewBitmap(img)
			b := img.Bounds()
			e.w, e.h = b.Dx(), b.Dy()
		})
	}()
	return nil
}

// stepShot returns the screenshot that shows a step best, the one after
// it, else the one before, and whether one is still loading.
func (a *app) stepShot(s gateway.StepView) (*mediaEntry, bool) {
	for _, id := range []string{s.After, s.Before} {
		if id == "" {
			continue
		}
		if e := a.media(id); e != nil {
			return e, false
		}
		if e := a.mediaCache[id]; e != nil && e.loading {
			return nil, true
		}
	}
	return nil, false
}

// hasShot reports whether a step has a screenshot this gateway can give.
func (a *app) hasShot(s gateway.StepView) bool {
	if _, ok := a.api.(mediaPather); !ok {
		return false
	}
	return s.After != "" || s.Before != ""
}

// shotScale is screenshot pixels per screen pixel, to place a step's
// target on its screenshot. The gateway scales screenshots down by a whole
// factor, which the live mirror's picture size gives; 0 when unknown.
func (a *app) shotScale(deviceID string, e *mediaEntry) float32 {
	m := a.mirrors[deviceID]
	if m == nil || m.picH == 0 || e.h == 0 {
		return 0
	}
	k := max(1, int(float32(m.picH)/float32(e.h)+0.5))
	return 1 / float32(k)
}

// shotView draws a step's screenshot, h points high, with the step's
// target outlined when it can be placed.
func (a *app) shotView(c *mg.Context, t gateway.TaskView, s gateway.StepView, h float32) mg.Element {
	p := pal(c)
	e, loading := a.stepShot(s)
	// A thumbnail's corners are a phone's at its size.
	rad := float32(10)
	if h < 120 {
		rad = 5
	}
	if e == nil {
		if loading {
			c.After(150 * time.Millisecond)
		}
		w := h * 9 / 19.5
		// A thumbnail is too narrow for words, which Chinese would stack a
		// character a line: it shows the sketch an agent's step has where
		// there is no picture.
		if w < 60 {
			return stepThumb(c, appColor(nonEmpty(appOf(t), t.Title)), h)
		}
		box := mg.Box(c).Size(w, h).Shrink(0).Radius(rad).Border(1, p.line2).Background(p.bg2).Center()
		box.Children(func() {
			msg := locales.ShotNone()
			if loading {
				msg = locales.ShotLoading()
			}
			mg.Text(c, msg).FontSize(fsCaption).TextColor(p.ink3).TextAlign(mg.Center)
		})
		return box
	}
	w := h * float32(e.w) / float32(e.h)
	k := a.shotScale(t.DeviceID, e)
	box := mg.Box(c).Size(w, h).Shrink(0).Radius(rad).Border(1, p.line2).Clip().Background(hex("#000000")).
		Label(locales.ShotLabel(t.DeviceName))
	box.Draw(func(pt *mg.Painter, r mg.Rect) {
		pt.Clip(r, rad, func() {
			pt.Image(e.bitmap, r, mg.Contain)
			// Thumbnails are too small for the outline to say anything.
			if k > 0 && !s.Target.Empty() && h >= 120 {
				drawOverlay(pt, r, p, s, stepDone, shotMarks{}, float32(e.w)/k, float32(e.h)/k, true)
			}
		})
	})
	return box
}

// stepDisplay is how a step reads in the client: its text, a smaller
// second line, and its mark. Besides the conventions in format.go it reads
// the gateway's own: an approval's step says "Waiting for you: Tapped …"
// while it waits and keeps that text when it is denied or withdrawn, with
// only its status changing.
func stepDisplay(t gateway.TaskView, i int) (text, sub string, kind stepKind) {
	s := t.Steps[i]
	text, sub = splitStep(s.Text)
	kind = kindOf(t, i)
	action, waiting := strings.CutPrefix(text, "Waiting for you: ")
	if waiting {
		action = actionAsked(action)
	}
	handoff, isHandoff := strings.CutPrefix(text, "Asked you to take over: ")
	asked, isAsk := strings.CutPrefix(text, "Asked you: ")
	sub = stepSub(sub)
	asked2 := func(why string) string { return withSub(locales.StepAsked(why), sub) }
	said := false // text is the client's words, not the gateway's
	set := func(s string) { text, said = s, true }
	switch s.Status {
	case "approval_pending":
		switch {
		case waiting && t.State == gateway.TaskWorking:
			// Answered: the gateway closes the step once the action ran.
			set(locales.StepYouAnswered(action))
			kind = stepYou
		case waiting:
			set(locales.StepWaiting(action))
		}
	case "approval_denied":
		switch {
		case waiting:
			set(locales.StepYouDenied(action))
		case isHandoff:
			set(locales.StepYouDeclined())
			sub = asked2(handoff)
		case isAsk:
			set(locales.StepYouSaidNo(asked))
		}
		if waiting || isHandoff || isAsk {
			kind = stepYou
		}
	case "settled":
		switch {
		case isHandoff:
			set(locales.StepYouHandedBack())
			sub, kind = asked2(handoff), stepYou
		case isAsk:
			set(locales.StepYouAgreed(asked))
			kind = stepYou
		}
	case "user_took_over":
		if waiting {
			set(upperFirst(action))
		}
		sub, kind = locales.StepNotDoneTookOver(), stepYou
	case "paused":
		sub = locales.StepNotDoneInControl()
	case "stopped":
		if waiting {
			set(upperFirst(action))
		}
		sub = locales.StepNotDoneStopped()
	case "stale":
		sub = locales.StepNotDoneStale()
	case "uncertain":
		// The gateway's line under it stays: an Always allow that let the
		// step run without asking.
		sub = withSub(locales.StepMayHaveHappened(), sub)
	case "expired":
		if waiting {
			set(upperFirst(action))
		}
		sub = locales.StepAllowedNotUsed()
	case "device_locked":
		sub = locales.StepPhoneLocked()
	case "device_offline":
		sub = locales.StepPhoneOffline()
	case "device_needs_setup":
		sub = locales.StepPhoneNeedsSetup()
	case "lease_held", "scope_denied", "not_found", "unsupported", "invalid_argument", "internal":
		sub = locales.StepFailedCode(failureName(s.Status))
	}
	switch {
	case said:
	case waiting && kind != stepWait:
		text = upperFirst(action)
	default:
		text = stepText(text)
	}
	return text, sub, kind
}

// failures name the error codes a step can end with.
var failures = map[string]func() string{
	"lease_held": locales.StepCodeLeaseHeld, "scope_denied": locales.StepCodeScopeDenied,
	"not_found": locales.StepCodeNotFound, "unsupported": locales.StepCodeUnsupported,
	"invalid_argument": locales.StepCodeInvalidArgument, "internal": locales.StepCodeInternal,
}

func failureName(code string) string {
	if f := failures[code]; f != nil {
		return f()
	}
	return strings.ReplaceAll(code, "_", " ")
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

// imperative turns a step's past tense into what was asked: "Tapped “Pay”"
// becomes "tap “Pay”".
func imperative(s string) string {
	for _, v := range [][2]string{
		{"Long-pressed ", "long-press "}, {"Tapped ", "tap "}, {"Typed ", "type "}, {"Swiped ", "swipe "},
		{"Pressed ", "press "}, {"Opened ", "open "}, {"Zoomed ", "zoom "}, {"Turned ", "turn "},
		{"Dragged ", "drag "},
	} {
		if rest, ok := strings.CutPrefix(s, v[0]); ok {
			return v[1] + rest
		}
	}
	return lowerFirst(s)
}

// Scopes as the person reads them.
var scopeChoices = []struct {
	mode  gateway.ScopeMode
	label func() string
}{
	{gateway.ScopeEverything, locales.ScopeEverything},
	{gateway.ScopeApps, locales.ScopeApps},
	{gateway.ScopeReadOnly, locales.ScopeReadOnly},
	{gateway.ScopeWatchOnly, locales.ScopeWatchOnly},
}

// scopeHint says what a scope lets an agent do, under the menu.
func scopeHint(m gateway.ScopeMode) string {
	switch m {
	case gateway.ScopeApps:
		return locales.ScopeHintApps()
	case gateway.ScopeReadOnly:
		return locales.ScopeHintReadOnly()
	case gateway.ScopeWatchOnly:
		return locales.ScopeHintWatchOnly()
	}
	return locales.ScopeHintEverything()
}

func scopeLabel(m gateway.ScopeMode) string {
	for _, c := range scopeChoices {
		if c.mode == m {
			return c.label()
		}
	}
	return string(m)
}

func scopeOf(label string) gateway.ScopeMode {
	for _, c := range scopeChoices {
		if c.label() == label {
			return c.mode
		}
	}
	return gateway.ScopeEverything
}

// scopeLine describes a scope in Settings › Agents.
func scopeLine(s gateway.Scope) string {
	switch s.Mode {
	case gateway.ScopeApps:
		if len(s.Apps) == 0 {
			return locales.ScopeLineNoApp()
		}
		return locales.ScopeLineApps(joinAnd(s.Apps))
	case gateway.ScopeReadOnly:
		return locales.ScopeLineReadOnly()
	case gateway.ScopeWatchOnly:
		return locales.ScopeLineWatchOnly()
	}
	return locales.ScopeLineEverything()
}

// newAgentNeed reports an agent's first-connection approval.
func newAgentNeed(n gateway.NeedView) bool {
	return n.Kind == gateway.NeedApproval && n.Rule == "New agent"
}

// isLocked reports whether a phone shows its lock screen: the gateway says
// so, or an unlock card is open for it (a locked phone with a waiting task
// counts as needing the person).
func (a *app) isLocked(d gateway.DeviceView) bool {
	return d.Status == gateway.DeviceLocked || a.unlocks[d.Info.ID]
}

// handoffFor returns the open handoff on a phone. The gateway ranks it
// above the person's control, so a phone the person took over shows as
// needing them once the agent calls phone_handoff to wait.
func (a *app) handoffFor(deviceID string) (gateway.NeedView, bool) {
	for _, n := range a.needs {
		if n.Kind == gateway.NeedHandoff && n.DeviceID == deviceID {
			return n, true
		}
	}
	return gateway.NeedView{}, false
}

// agentRequestDetail is a New agent request's second line. With a scope
// picker under it, the gateway's "Allowing gives it … access." would
// contradict the choice, so it is left out.
func agentRequestDetail(n gateway.NeedView, picker bool) string {
	if picker {
		if i := strings.Index(n.Detail, " Allowing gives it"); i >= 0 {
			n.Detail = n.Detail[:i]
		}
	}
	d := needDetail(n)
	if !n.Created.IsZero() {
		d = strings.TrimSpace(locales.SentencePair(d, locales.AgentRequestAskedAt(n.Created.Format("15:04"))))
	}
	return d
}

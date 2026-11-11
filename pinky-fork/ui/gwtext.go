package ui

import (
	"regexp"
	"strings"

	"github.com/egoist/mygo/i18n"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/gateway"
)

// The gateway words steps, approvals and status lines in English as it
// records them (internal/gateway: describe, approvalTitle, statusLocked,
// the files and agent steps), and history keeps those words. In another
// language the client reads them back by their patterns and says them in
// its own words; text that matches none, and in English all of it, shows
// as the gateway wrote it. Agents' words (task titles, reasons, summaries)
// and the phone's (labels in quotes, app names) stay as they are inside.

// translated reports whether the app shows another language than the
// gateway's English.
func translated() bool {
	_, lang, ok := locales.Catalog.Message(i18n.Locale(), "dayToday")
	return ok && lang != locales.Catalog.Source()
}

func re(s string) *regexp.Regexp { return regexp.MustCompile(s) }

// An action as describe words it, in the past tense.
var (
	reTapLabel   = re(`^(Tapped|Long-pressed) “(.*)”$`)
	reTapAt      = re(`^(Tapped|Long-pressed) at (-?\d+),(-?\d+)$`)
	reTapBare    = re(`^(Tapped|Long-pressed)$`)
	reTyped      = re(`^Typed “(.*?)”(?: into “(.*)”)?( and submitted)?$`)
	reSwipeIn    = re(`^Swiped (\S+) in “(.*)”$`)
	reSwipeFrom  = re(`^Swiped from (-?\d+),(-?\d+) to (-?\d+),(-?\d+)$`)
	reSwipe      = re(`^Swiped (\S+)$`)
	reZoom       = re(`^Zoomed (in|out)(?: on “(.*)”)?$`)
	reTurn       = re(`^Turned (?:“(.*)” )?with two fingers$`)
	reDragFrom   = re(`^Dragged from (-?\d+),(-?\d+) to (-?\d+),(-?\d+)$`)
	reDragLabel  = re(`^Dragged(?: “(.*)”)?$`)
	rePressed    = re(`^Pressed (.+)$`)
	reOpenedLink = re(`^Opened “(.*)” in (.+)$`)
	reOpenedIn   = re(`^Opened (.+) in ([a-z]{2,3}(?:-[A-Za-z0-9]{2,8})*)$`)
	reOpenedReg  = re(`^Opened (.+) with region ([A-Z]{2}|[0-9]{3})$`)
	reOpened     = re(`^Opened (.+)$`)
	reSetSwitch  = re(`^[Ss]et (the screen's rotation|the appearance|the location) to ([^;]+)$`)
	reCopyOne    = re(`^[Cc]opy “(.*)” to (.+)$`)
	reCopyMany   = re(`^[Cc]opy (\d+) files? to (.+)$`)
	reListFolder = re(`^[Ll]ist the phone's Pinky folder$`)
	reCopyFrom   = re(`^[Cc]opy from the phone$`)
	reCopyTo     = re(`^[Cc]opy to the phone$`)
	reReceive    = re(`^[Rr]eceive “(.*)” from the agent$`)

	// phone_app's, in steps: "install “Dinely” 1.3 (14)"; and with the
	// phone, capitalized, in titles and waiting steps: "Install “Dinely”
	// 1.3 (14) on Pixel 9".
	reInstallOn   = re(`^Install “(.*?)”(?: (.+?))? on (.+)$`)
	reInstall     = re(`^install “(.*?)”(?: (.+))?$`)
	reUpdateOn    = re(`^Update “(.*?)”(?: to (.+?))? on (.+)$`)
	reUpdate      = re(`^update “(.*?)”(?: to (.+))?$`)
	reUninstall   = re(`^[Uu]ninstall “(.*?)”(?: from (.+))?$`)
	reStopApp     = re(`^[Ss]top “(.*?)”(?: on (.+))?$`)
	reInstallFrom = re(`^install from (“.*)$`)

	reReadLogs = re(`^[Rr]ead the logs of (.+)$`)
)

// action is a parsed action, which says itself as done or as asked.
type action struct {
	verb                 string // tap, longpress, type, swipe, swipefrom, zoomin, zoomout, turn, drag, dragfrom, press, open, openlink, openin, openregion, set, stoplocation, copy, copymany, list, copyfrom, copyto, receive, install, update, uninstall, stop, installfrom, readlogs
	label, text, x, y    string
	x2, y2, dir, key     string
	app, files, phone    string
	version              string
	locale               string // openin's language tag, openregion's region
	setting, value       string // set's switch and value, in the gateway's words
	into, submit, placed bool
}

func parseAction(s string) (action, bool) {
	if m := reTapLabel.FindStringSubmatch(s); m != nil {
		return action{verb: tapVerb(m[1]), label: m[2], into: true}, true
	}
	if m := reTapAt.FindStringSubmatch(s); m != nil {
		return action{verb: tapVerb(m[1]), x: m[2], y: m[3], placed: true}, true
	}
	if m := reTapBare.FindStringSubmatch(s); m != nil {
		return action{verb: tapVerb(m[1])}, true
	}
	if m := reTyped.FindStringSubmatch(s); m != nil {
		return action{verb: "type", text: m[1], label: m[2], into: m[2] != "", submit: m[3] != ""}, true
	}
	if m := reSwipeIn.FindStringSubmatch(s); m != nil {
		return action{verb: "swipe", dir: m[1], label: m[2], into: true}, true
	}
	if m := reSwipeFrom.FindStringSubmatch(s); m != nil {
		return action{verb: "swipefrom", x: m[1], y: m[2], x2: m[3], y2: m[4]}, true
	}
	if m := reSwipe.FindStringSubmatch(s); m != nil {
		return action{verb: "swipe", dir: m[1]}, true
	}
	if m := reZoom.FindStringSubmatch(s); m != nil {
		return action{verb: "zoom" + m[1], label: m[2], into: m[2] != ""}, true
	}
	if m := reTurn.FindStringSubmatch(s); m != nil {
		return action{verb: "turn", label: m[1], into: m[1] != ""}, true
	}
	if m := reDragFrom.FindStringSubmatch(s); m != nil {
		return action{verb: "dragfrom", x: m[1], y: m[2], x2: m[3], y2: m[4]}, true
	}
	if m := reDragLabel.FindStringSubmatch(s); m != nil {
		return action{verb: "drag", label: m[1], into: m[1] != ""}, true
	}
	if m := rePressed.FindStringSubmatch(s); m != nil {
		return action{verb: "press", key: m[1]}, true
	}
	if m := reOpenedLink.FindStringSubmatch(s); m != nil {
		return action{verb: "openlink", label: m[1], app: m[2]}, true
	}
	if m := reOpenedIn.FindStringSubmatch(s); m != nil {
		return action{verb: "openin", app: m[1], locale: m[2]}, true
	}
	if m := reOpenedReg.FindStringSubmatch(s); m != nil {
		return action{verb: "openregion", app: m[1], locale: m[2]}, true
	}
	if m := reOpened.FindStringSubmatch(s); m != nil {
		return action{verb: "open", app: m[1]}, true
	}
	if m := reSetSwitch.FindStringSubmatch(s); m != nil {
		return action{verb: "set", setting: m[1], value: m[2]}, true
	}
	if s == "Stopped the simulated location" {
		return action{verb: "stoplocation"}, true
	}
	return action{}, false
}

// askSteps says what several steps asked for, as phone_device joins them:
// "Set the screen's rotation to landscape; set the appearance to dark".
func askSteps(s string) (string, bool) {
	parts := strings.Split(s, "; ")
	if len(parts) < 2 {
		return "", false
	}
	for i, p := range parts {
		a, ok := parseAction(p)
		if !ok {
			return "", false
		}
		parts[i] = a.ask()
	}
	return strings.Join(parts, locales.ListSeparator()), true
}

// The phone's settings phone_device changes, and their values, as the
// gateway words them in its steps.
var switchNames = map[string]func() string{
	"the screen's rotation": locales.GwSwitchRotation,
	"the appearance":        locales.GwSwitchAppearance,
	"the location":          locales.GwSwitchLocation,
}

var switchValues = map[string]func() string{
	"portrait": locales.GwValuePortrait, "landscape": locales.GwValueLandscape, "upside down": locales.GwValueUpsideDown,
	"reverse landscape": locales.GwValueReverseLandscape, "auto-rotate": locales.GwValueAutoRotate,
	"light": locales.GwValueLight, "dark": locales.GwValueDark, "automatic": locales.GwValueAutomatic,
	"custom": locales.GwValueCustom, "none": locales.GwValueNoLocation,
}

func switchName(s string) string {
	if f := switchNames[s]; f != nil {
		return f()
	}
	return s
}

// switchValue says a setting's value; a place is "35.6812, 139.7671".
func switchValue(s string) string {
	if f := switchValues[s]; f != nil {
		return f()
	}
	return locales.GwValuePlace(s)
}

// parseAsked reads what the files, app and logs steps word in the
// imperative: "copy to the phone", "Copy “a.txt” to Pixel 9", "receive
// “a.png” from the agent", "Install “Dinely” 1.3 (14) on Pixel 9", "read
// the logs of Example".
func parseAsked(s string) (action, bool) {
	if a, ok := parseAppAsked(s); ok {
		return a, true
	}
	if m := reCopyOne.FindStringSubmatch(s); m != nil {
		return action{verb: "copy", label: m[1], phone: m[2]}, true
	}
	if m := reReceive.FindStringSubmatch(s); m != nil {
		return action{verb: "receive", label: m[1]}, true
	}
	if m := reCopyMany.FindStringSubmatch(s); m != nil {
		return action{verb: "copymany", files: m[1], phone: m[2]}, true
	}
	if m := reReadLogs.FindStringSubmatch(s); m != nil {
		return action{verb: "readlogs", app: m[1]}, true
	}
	switch {
	case reListFolder.MatchString(s):
		return action{verb: "list"}, true
	case reCopyFrom.MatchString(s):
		return action{verb: "copyfrom"}, true
	case reCopyTo.MatchString(s):
		return action{verb: "copyto"}, true
	}
	return action{}, false
}

func parseAppAsked(s string) (action, bool) {
	if m := reInstallOn.FindStringSubmatch(s); m != nil {
		return action{verb: "install", app: m[1], version: m[2], phone: m[3]}, true
	}
	if m := reInstall.FindStringSubmatch(s); m != nil {
		return action{verb: "install", app: m[1], version: m[2]}, true
	}
	if m := reUpdateOn.FindStringSubmatch(s); m != nil {
		return action{verb: "update", app: m[1], version: m[2], phone: m[3]}, true
	}
	if m := reUpdate.FindStringSubmatch(s); m != nil {
		return action{verb: "update", app: m[1], version: m[2]}, true
	}
	if m := reUninstall.FindStringSubmatch(s); m != nil {
		return action{verb: "uninstall", app: m[1], phone: m[2]}, true
	}
	if m := reStopApp.FindStringSubmatch(s); m != nil {
		return action{verb: "stop", app: m[1], phone: m[2]}, true
	}
	if m := reInstallFrom.FindStringSubmatch(s); m != nil {
		return action{verb: "installfrom", files: m[1]}, true
	}
	return action{}, false
}

func tapVerb(s string) string {
	if s == "Long-pressed" {
		return "longpress"
	}
	return "tap"
}

// done says the action as a step that happened, as "Tapped “Pay”" does.
func (a action) done() string {
	switch a.verb {
	case "tap", "longpress":
		long := a.verb == "longpress"
		switch {
		case a.into && long:
			return locales.GwDidLongPress(a.label)
		case a.into:
			return locales.GwDidTap(a.label)
		case a.placed && long:
			return locales.GwDidLongPressAt(a.x, a.y)
		case a.placed:
			return locales.GwDidTapAt(a.x, a.y)
		case long:
			return locales.GwDidLongPressBare()
		}
		return locales.GwDidTapBare()
	case "type":
		switch {
		case a.into && a.submit:
			return locales.GwDidTypeIntoSubmit(a.text, a.label)
		case a.into:
			return locales.GwDidTypeInto(a.text, a.label)
		case a.submit:
			return locales.GwDidTypeSubmit(a.text)
		}
		return locales.GwDidType(a.text)
	case "swipe":
		if a.into {
			return locales.GwDidSwipeIn(dirName(a.dir), a.label)
		}
		return locales.GwDidSwipe(dirName(a.dir))
	case "swipefrom":
		return locales.GwDidSwipeFromTo(a.x, a.y, a.x2, a.y2)
	case "zoomin":
		if a.into {
			return locales.GwDidZoomInOn(a.label)
		}
		return locales.GwDidZoomIn()
	case "zoomout":
		if a.into {
			return locales.GwDidZoomOutOn(a.label)
		}
		return locales.GwDidZoomOut()
	case "turn":
		if a.into {
			return locales.GwDidTurnOn(a.label)
		}
		return locales.GwDidTurn()
	case "drag":
		if a.into {
			return locales.GwDidDrag(a.label)
		}
		return locales.GwDidDragBare()
	case "dragfrom":
		return locales.GwDidDragFromTo(a.x, a.y, a.x2, a.y2)
	case "press":
		return locales.GwDidPress(keyName(a.key))
	case "open":
		return locales.GwDidOpen(a.app)
	case "openlink":
		return locales.GwDidOpenLink(a.label, a.app)
	case "openin":
		return locales.GwDidOpenIn(a.app, a.locale)
	case "openregion":
		return locales.GwDidOpenRegion(a.app, a.locale)
	case "set":
		return locales.GwDidSetSwitch(switchName(a.setting), switchValue(a.value))
	}
	return a.ask()
}

// ask says the action as what was asked, without a tense, as "tap “Pay”"
// does.
func (a action) ask() string {
	switch a.verb {
	case "tap", "longpress":
		long := a.verb == "longpress"
		switch {
		case a.into && long:
			return locales.GwDoLongPress(a.label)
		case a.into:
			return locales.GwDoTap(a.label)
		case a.placed && long:
			return locales.GwDoLongPressAt(a.x, a.y)
		case a.placed:
			return locales.GwDoTapAt(a.x, a.y)
		case long:
			return locales.GwDoLongPressBare()
		}
		return locales.GwDoTapBare()
	case "type":
		switch {
		case a.into && a.submit:
			return locales.GwDoTypeIntoSubmit(a.text, a.label)
		case a.into:
			return locales.GwDoTypeInto(a.text, a.label)
		case a.submit:
			return locales.GwDoTypeSubmit(a.text)
		}
		return locales.GwDoType(a.text)
	case "swipe":
		if a.into {
			return locales.GwDoSwipeIn(dirName(a.dir), a.label)
		}
		return locales.GwDoSwipe(dirName(a.dir))
	case "swipefrom":
		return locales.GwDoSwipeFromTo(a.x, a.y, a.x2, a.y2)
	case "zoomin":
		if a.into {
			return locales.GwDoZoomInOn(a.label)
		}
		return locales.GwDoZoomIn()
	case "zoomout":
		if a.into {
			return locales.GwDoZoomOutOn(a.label)
		}
		return locales.GwDoZoomOut()
	case "turn":
		if a.into {
			return locales.GwDoTurnOn(a.label)
		}
		return locales.GwDoTurn()
	case "drag":
		if a.into {
			return locales.GwDoDrag(a.label)
		}
		return locales.GwDoDragBare()
	case "dragfrom":
		return locales.GwDoDragFromTo(a.x, a.y, a.x2, a.y2)
	case "press":
		return locales.GwDoPress(keyName(a.key))
	case "open":
		return locales.GwDoOpen(a.app)
	case "openlink":
		return locales.GwDoOpenLink(a.label, a.app)
	case "openin":
		return locales.GwDoOpenIn(a.app, a.locale)
	case "openregion":
		return locales.GwDoOpenRegion(a.app, a.locale)
	case "set":
		return locales.GwDoSetSwitch(switchName(a.setting), switchValue(a.value))
	case "stoplocation":
		return locales.GwDidStopLocation() // never asked: only ever a step that happened
	case "copy":
		return locales.GwDoCopyFile(a.label, a.phone)
	case "copymany":
		return locales.GwDoCopyFiles(a.files, a.phone)
	case "list":
		return locales.GwDoListFolder()
	case "copyfrom":
		return locales.GwDoCopyFromPhone()
	case "copyto":
		return locales.GwDoCopyToPhone()
	case "receive":
		return locales.GwDoReceiveFile(a.label)
	case "install", "update", "uninstall", "stop":
		return a.appAsk()
	case "installfrom":
		return locales.GwDoInstallFrom(appFiles(a.files))
	case "readlogs":
		return locales.GwDoReadLogs(a.app)
	}
	return ""
}

// appAsk says an app change in the imperative, with the phone when the
// gateway named it.
func (a action) appAsk() string {
	var s string
	switch a.verb {
	case "install":
		s = locales.GwDoInstallAppBare(a.app)
		if a.version != "" {
			s = locales.GwDoInstallApp(a.app, a.version)
		}
	case "update":
		s = locales.GwDoUpdateAppBare(a.app)
		if a.version != "" {
			s = locales.GwDoUpdateApp(a.app, a.version)
		}
	case "uninstall":
		s = locales.GwDoUninstallApp(a.app)
		if a.phone != "" {
			return locales.GwDoFromPhone(s, a.phone)
		}
		return s
	default:
		s = locales.GwDoStopApp(a.app)
	}
	if a.phone != "" {
		return locales.GwDoOnPhone(s, a.phone)
	}
	return s
}

var reAppFiles = re(`^“(.*)” and (\d+) more files?$`)

// appFiles says the files an app is installed from: “base.apk” and 2 more
// files.
func appFiles(s string) string {
	if m := reAppFiles.FindStringSubmatch(s); m != nil {
		return locales.GwAppFilesMore(m[1], m[2])
	}
	return s
}

// actionAsked says the action of a step that waited for the person, given
// as the gateway words it after "Waiting for you: ": "tap “Pay”" in
// English, and the catalog's gwDoTap and the like in other languages.
func actionAsked(past string) string {
	if translated() {
		if a, ok := parseAction(past); ok {
			return a.ask()
		}
		if a, ok := parseAsked(past); ok {
			return a.ask()
		}
		if ask, ok := askSteps(past); ok {
			return ask
		}
	}
	return imperative(past)
}

var dirNames = map[string]func() string{
	"up": locales.GwDirUp, "down": locales.GwDirDown, "left": locales.GwDirLeft, "right": locales.GwDirRight,
}

func dirName(d string) string {
	if f := dirNames[d]; f != nil {
		return f()
	}
	return d
}

// keyNames name phone_key's keys, as "Pressed home" (a space for each _)
// and approval titles ("Press page_up in …") write them.
var keyNames = map[string]func() string{
	"back": locales.GwKeyBack, "home": locales.GwKeyHome, "recents": locales.GwKeyRecents,
	"enter": locales.GwKeyEnter, "tab": locales.GwKeyTab, "delete": locales.GwKeyDelete,
	"forward delete": locales.GwKeyForwardDelete, "escape": locales.GwKeyEscape,
	"dpad up": locales.GwKeyUp, "dpad down": locales.GwKeyDown, "dpad left": locales.GwKeyLeft,
	"dpad right": locales.GwKeyRight, "page up": locales.GwKeyPageUp, "page down": locales.GwKeyPageDown,
}

func keyName(k string) string {
	if f := keyNames[strings.ReplaceAll(k, "_", " ")]; f != nil {
		return f()
	}
	return k
}

// Steps of their own, besides actions.
var (
	reWaiting   = re(`^Waiting for you: (.*)$`)
	reBlocked   = re(`^Blocked by “(.*?)”: (.*)$`)
	reOutside   = re(`^Outside the scope: (.*)$`)
	reFailed    = re(`^Failed: (.*)$`)
	reConnected = re(`^Connected to (.*) for “(.*)”(, read only)?(, on the phone's own screen)?$`)
	reFinished  = re(`^Finished: (.*)$`)
	reWaited    = re(`^Waited for “(.*)”$`)
	reAskedYou  = re(`^Asked you: (.*)$`)
	reAskedTake = re(`^Asked you to take over: (.*)$`)
	reListed    = re(`^Listed the phone's Pinky folder \((\d+) files?\)$`)
	reListedPC  = re(`^Listed the phone's folder on the computer \((\d+) files?\)$`)
	reInstalled = re(`^Installed “(.*?)”(?: (.+?))? from (“.*)$`)
	reUpdated   = re(`^Updated “(.*?)”(?: to (.+?))? from (“.*)$`)
	reRemoved   = re(`^Uninstalled “(.*)”$`)
	reStopped   = re(`^Stopped “(.*)”$`)
	reCopied    = re(`^Copied (\d+) files? (from|to) the phone(: .*)?$`)
	reReceived  = re(`^Received “(.*)” \(([^()]+)\) from the agent$`)
	reSavedShot = re(`^Saved a screenshot as “(.*)”$`)
	reGiveVideo = re(`^[Gg]ive (.+) the screen recording of “(.*)”$`)
	reGiveVids  = re(`^[Gg]ive (.+) the screen recordings of (\d+) tasks$`)
	reLogLines  = re(`^Read (\d+) log lines? of (.+)$`)
	reCrashes   = re(`^Read (\d+) crash reports? of (.+)$`)
	reYouDid    = re(`^You (allowed|denied): (.*)$`)
	reYourNote  = re(`^Your note: (.*)$`)
	reAlways    = re(`^Always allowed: “(.*?)” in (.+)$`)
)

var outcomes = map[string]func() string{
	"done": locales.GwOutcomeDone, "failed": locales.GwOutcomeFailed, "partial": locales.GwOutcomePartial,
	"cancelled": locales.GwOutcomeCancelled, "stopped": locales.GwOutcomeStopped, "expired": locales.GwOutcomeExpired,
	"disconnected": locales.GwOutcomeDisconnected, "offline": locales.GwOutcomeOffline, "replaced": locales.GwOutcomeReplaced,
	"paused": locales.GwOutcomePaused,
}

// stepText says a step's first line as the gateway wrote it.
func stepText(s string) string {
	if !translated() {
		return s
	}
	if a, ok := parseAction(s); ok {
		return a.done()
	}
	asked := func(rest string) string {
		if a, ok := parseAction(rest); ok {
			return a.ask()
		}
		if a, ok := parseAsked(rest); ok {
			return a.ask()
		}
		if give, ok := giveRecordings(rest); ok {
			return give
		}
		if ask, ok := askSteps(rest); ok {
			return ask
		}
		return rest
	}
	switch s {
	case "Unlocked from the computer":
		return locales.GwStepUnlocked()
	case "You took over":
		return locales.GwStepYouTookOver()
	case "You stopped the task":
		return locales.GwStepYouStopped()
	case "Saved the task's report for the agent":
		return locales.GwStepSavedReport()
	case "Saved the task's report and screen recording for the agent":
		return locales.GwStepSavedReportVideo()
	}
	if m := reWaiting.FindStringSubmatch(s); m != nil {
		return locales.GwStepWaiting(asked(m[1]))
	}
	if t, ok := putBackText(s); ok {
		return t
	}
	if m := reBlocked.FindStringSubmatch(s); m != nil {
		return locales.GwStepBlocked(ruleName(m[1]), asked(m[2]))
	}
	if m := reOutside.FindStringSubmatch(s); m != nil {
		return locales.GwStepOutside(asked(m[1]))
	}
	if m := reFailed.FindStringSubmatch(s); m != nil {
		return locales.GwStepFailed(asked(m[1]))
	}
	if m := reConnected.FindStringSubmatch(s); m != nil {
		t := locales.GwStepConnected(m[1], m[2])
		if m[3] != "" {
			t = locales.GwStepConnectedReadOnly(t)
		}
		if m[4] != "" {
			t = locales.GwStepConnectedOwnScreen(t)
		}
		return t
	}
	if m := reFinished.FindStringSubmatch(s); m != nil {
		if f := outcomes[m[1]]; f != nil {
			return locales.GwStepFinished(f())
		}
		return locales.GwStepFinished(m[1])
	}
	if m := reWaited.FindStringSubmatch(s); m != nil {
		return locales.GwStepWaited(m[1])
	}
	if m := reAskedTake.FindStringSubmatch(s); m != nil {
		return locales.GwStepAskedTakeOver(m[1])
	}
	if m := reAskedYou.FindStringSubmatch(s); m != nil {
		return locales.GwStepAskedYou(m[1])
	}
	if m := reListed.FindStringSubmatch(s); m != nil {
		return locales.GwStepListedFolder(m[1])
	}
	if m := reListedPC.FindStringSubmatch(s); m != nil {
		return locales.GwStepListedComputerFolder(m[1])
	}
	if m := reInstalled.FindStringSubmatch(s); m != nil {
		if m[2] == "" {
			return locales.GwStepInstalledAppBare(m[1], appFiles(m[3]))
		}
		return locales.GwStepInstalledApp(m[1], m[2], appFiles(m[3]))
	}
	if m := reUpdated.FindStringSubmatch(s); m != nil {
		if m[2] == "" {
			return locales.GwStepUpdatedAppBare(m[1], appFiles(m[3]))
		}
		return locales.GwStepUpdatedApp(m[1], m[2], appFiles(m[3]))
	}
	if m := reRemoved.FindStringSubmatch(s); m != nil {
		return locales.GwStepUninstalledApp(m[1])
	}
	if m := reStopped.FindStringSubmatch(s); m != nil {
		return locales.GwStepStoppedApp(m[1])
	}
	if m := reCopied.FindStringSubmatch(s); m != nil {
		names := fileNames(m[3])
		if m[2] == "from" {
			return locales.GwStepCopiedFrom(m[1], names)
		}
		return locales.GwStepCopiedTo(m[1], names)
	}
	if m := reReceived.FindStringSubmatch(s); m != nil {
		return locales.GwStepReceivedFile(m[1], m[2])
	}
	if m := reSavedShot.FindStringSubmatch(s); m != nil {
		return locales.GwStepSavedScreenshot(m[1])
	}
	if m := reLogLines.FindStringSubmatch(s); m != nil {
		return locales.GwStepReadLogLines(m[1], m[2])
	}
	if m := reCrashes.FindStringSubmatch(s); m != nil {
		return locales.GwStepReadCrashes(m[1], m[2])
	}
	if m := reYouDid.FindStringSubmatch(s); m != nil {
		if m[1] == "allowed" {
			return locales.GwStepYouAllowed(asked(m[2]))
		}
		return locales.GwStepYouDenied(asked(m[2]))
	}
	return s
}

// What Pinky did when a task ended to put back the settings an agent
// changed (gateway/switches.go).
var (
	reSwitchBack      = re(`^Put (the screen's rotation|the appearance|the location) back to (.+)$`)
	reSwitchLeft      = re(`^Left (the screen's rotation|the appearance|the location) as the person set it$`)
	reSwitchNotBack   = re(`^Could not put (the screen's rotation|the appearance|the location) back to (.+)$`)
	reLanguageBack    = re(`^Put the language of (.+) back$`)
	reLanguageLeft    = re(`^Left the language of (.+) as the person set it$`)
	reLanguageNotBack = re(`^Could not put the language of (.+) back$`)
)

func putBackText(s string) (string, bool) {
	if m := reSwitchBack.FindStringSubmatch(s); m != nil {
		return locales.GwStepSwitchBack(switchName(m[1]), switchValue(m[2])), true
	}
	if m := reSwitchLeft.FindStringSubmatch(s); m != nil {
		return locales.GwStepSwitchLeft(switchName(m[1])), true
	}
	if m := reSwitchNotBack.FindStringSubmatch(s); m != nil {
		return locales.GwStepSwitchNotBack(switchName(m[1]), switchValue(m[2])), true
	}
	if m := reLanguageBack.FindStringSubmatch(s); m != nil {
		return locales.GwStepLanguageBack(m[1]), true
	}
	if m := reLanguageLeft.FindStringSubmatch(s); m != nil {
		return locales.GwStepLanguageLeft(m[1]), true
	}
	if m := reLanguageNotBack.FindStringSubmatch(s); m != nil {
		return locales.GwStepLanguageNotBack(m[1]), true
	}
	return "", false
}

// stepSub says a step's second line as the gateway (or the fake) wrote
// it: "Your note: “…”", or "Always allowed: “Payments” in Dinely" under a
// step that ran without asking because of the person's Always allow.
func stepSub(s string) string {
	if !translated() || s == "" {
		return s
	}
	first, rest, more := strings.Cut(s, "\n")
	if m := reYourNote.FindStringSubmatch(first); m != nil {
		first = locales.GwStepYourNote(m[1])
	} else if m := reAlways.FindStringSubmatch(first); m != nil {
		first = locales.GwStepAlwaysAllowed(ruleName(m[1]), m[2])
	}
	if more {
		return first + "\n" + stepSub(rest)
	}
	return first
}

var reMore = re(`^and (\d+) more$`)

// fileNames says the gateway's list of files, ": a.txt, b.png, and 3
// more", in the app's language.
func fileNames(s string) string {
	rest, ok := strings.CutPrefix(s, ": ")
	if !ok {
		return s
	}
	parts := strings.Split(rest, ", ")
	for i, p := range parts {
		if m := reMore.FindStringSubmatch(p); m != nil {
			parts[i] = locales.GwFilesMore(m[1])
		}
	}
	return locales.GwFilesList(strings.Join(parts, locales.ListSeparator()))
}

// Approval titles, as approvalTitle and the files, app, logs and agent steps
// write them.
var (
	rePayTitle    = re(`^Pay: “(.*)”\?$`)
	reSendTitle   = re(`^Send “(.*)”\?$`)
	reLinkTitle   = re(`^Open “(.*)” in (.+)\?$`)
	reOpenTitle   = re(`^Open (.+)\?$`)
	reChangeTitle = re(`^Change “(.*)”\?$`)
	reTapTitle    = re(`^(Tap|Long-press|Type into|Swipe|Drag) “(.*)”\?$`)
	rePressTitle  = re(`^Press (\S+) in “(.*)”\?$`)
	reAgentTitle  = re(`^Let (.+) use your phones\?$`)
	reAskedTitle  = re(`^((?:Copy|Install|Update|Uninstall|Stop) .+|Read the logs of .+)\?$`)
	reQuestion    = re(`^(.+)\?$`)
)

// approvalText says an approval's title: "Pay: “Place order · $12.80”?".
func approvalText(s string) string {
	if !translated() {
		return s
	}
	if s == "Send the message?" {
		return locales.GwAskSendMessage()
	}
	if m := rePayTitle.FindStringSubmatch(s); m != nil {
		return locales.GwAskPay(m[1])
	}
	if m := reSendTitle.FindStringSubmatch(s); m != nil {
		return locales.GwAskSend(m[1])
	}
	if m := reChangeTitle.FindStringSubmatch(s); m != nil {
		return locales.GwAskChange(m[1])
	}
	if m := reTapTitle.FindStringSubmatch(s); m != nil {
		switch m[1] {
		case "Tap":
			return locales.GwAskTap(m[2])
		case "Long-press":
			return locales.GwAskLongPress(m[2])
		case "Type into":
			return locales.GwAskTypeInto(m[2])
		case "Drag":
			return locales.GwAskDrag(m[2])
		}
		return locales.GwAskSwipe(m[2])
	}
	if m := rePressTitle.FindStringSubmatch(s); m != nil {
		return locales.GwAskPressIn(keyName(m[1]), m[2])
	}
	if m := reAgentTitle.FindStringSubmatch(s); m != nil {
		return locales.GwAskAgent(m[1])
	}
	if m := reAskedTitle.FindStringSubmatch(s); m != nil {
		if a, ok := parseAsked(m[1]); ok {
			return locales.GwAskQuestion(a.ask())
		}
	}
	if q, ok := strings.CutSuffix(s, "?"); ok {
		if give, ok := giveRecordings(q); ok {
			return locales.GwAskQuestion(give)
		}
	}
	if m := reLinkTitle.FindStringSubmatch(s); m != nil {
		return locales.GwAskOpenLink(m[1], m[2])
	}
	if m := reOpenTitle.FindStringSubmatch(s); m != nil && !strings.Contains(m[1], "“") {
		// "Open Settings in ja-JP?"
		if a, ok := parseAction("Opened " + m[1]); ok && a.verb != "open" {
			return locales.GwAskQuestion(a.ask())
		}
		return locales.GwAskOpen(m[1])
	}
	// What describe says, capitalized, as a question: "Tapped at 540,1200?",
	// and phone_device's "Set the appearance to dark; set …?".
	if m := reQuestion.FindStringSubmatch(s); m != nil {
		if a, ok := parseAction(m[1]); ok {
			return locales.GwAskQuestion(a.ask())
		}
		if ask, ok := askSteps(m[1]); ok {
			return locales.GwAskQuestion(ask)
		}
	}
	return s
}

// giveRecordings says the Recordings card's question, without its
// question mark: "give Claude Code the screen recording of “Reorder
// lunch”".
func giveRecordings(s string) (string, bool) {
	if m := reGiveVideo.FindStringSubmatch(s); m != nil {
		return locales.GwDoGiveRecording(m[1], m[2]), true
	}
	if m := reGiveVids.FindStringSubmatch(s); m != nil {
		return locales.GwDoGiveRecordings(m[1], m[2]), true
	}
	return "", false
}

// needTitle is a Needs you card's title.
func needTitle(n gateway.NeedView) string {
	if !translated() {
		return n.Title
	}
	switch {
	case n.Kind == gateway.NeedUnlock:
		if name, ok := strings.CutPrefix(n.Title, "Unlock "); ok {
			return locales.GwUnlockTitle(name)
		}
		return n.Title
	case n.Kind == gateway.NeedSetup:
		if name, ok := strings.CutPrefix(n.Title, "Enter the passcode on "); ok {
			return locales.GwSetupPasscodeTitle(name)
		}
		return n.Title
	case n.Kind == gateway.NeedHandoff, n.Rule == "Agent request":
		return n.Title // the agent's own words
	}
	return approvalText(n.Title)
}

var (
	reUnlockDetail = re(`^(.+) is waiting for the phone to unlock · (.*)$`)
	reSetupDetail  = re(`^iOS asks for it on the phone before the WebDriverAgent helper can control it, and stops asking after a while\. (.+) is waiting · (.*)$`)
	reAgentWants   = re(`^(.+) wants to connect for “(.*)”\.(?: Allowing gives it (\S+) access\.)?$`)
	reFilesDetail  = re(`^(.+) · into the phone's Pinky folder(: .*)?$`)
	reSettingsBack = re(`^(.+) · Pinky puts it back when the task ends$`)
)

// needDetail is a Needs you card's second line.
func needDetail(n gateway.NeedView) string {
	if !translated() {
		return n.Detail
	}
	if m := reUnlockDetail.FindStringSubmatch(n.Detail); m != nil && n.Kind == gateway.NeedUnlock {
		return locales.GwUnlockDetail(m[1], m[2])
	}
	if m := reSetupDetail.FindStringSubmatch(n.Detail); m != nil && n.Kind == gateway.NeedSetup {
		return locales.GwSetupPasscodeDetail(m[1], m[2])
	}
	if m := reAgentWants.FindStringSubmatch(n.Detail); m != nil && newAgentNeed(n) {
		d := locales.GwAgentWants(m[1], m[2])
		if m[3] != "" {
			d += locales.GwAgentGives(scopeLine(gateway.Scope{Mode: gateway.ScopeMode(m[3])}))
		}
		return d
	}
	if m := reFilesDetail.FindStringSubmatch(n.Detail); m != nil {
		return locales.GwFilesDetail(m[1], fileNames(m[2]))
	}
	if n.Detail == "Unlike screenshots, recordings do not cover one-time codes or blocked apps' notifications." {
		return locales.GwRecordingsDetail()
	}
	if n.Rule == "Installing from the computer" || n.Rule == "Uninstalling apps" {
		return appDetail(n.Detail)
	}
	if m := reSettingsBack.FindStringSubmatch(n.Detail); m != nil {
		return locales.GwSettingsDetail(m[1])
	}
	return n.Detail
}

var (
	reSignedBy = re(`^signed by (.+)$`)
	reFromApp  = re(`^from (“.*)$`)
	reReplaces = re(`^replaces (.+)$`)
)

// appDetail says an install or uninstall approval's second line: the
// agent, the app's id, then for an install "signed by …", "from …" and
// "replaces …", or for an uninstall that its data goes too, and last the
// agent's own words, which stay as they are.
func appDetail(s string) string {
	parts := strings.Split(s, " · ")
	i := 2
	if i < len(parts) && parts[i] == "its data on the phone is deleted too" {
		parts[i] = locales.GwAppDataDeleted()
		return strings.Join(parts, " · ")
	}
	if i < len(parts) {
		if m := reSignedBy.FindStringSubmatch(parts[i]); m != nil {
			parts[i] = locales.GwAppSignedBy(m[1])
			i++
		}
	}
	if i < len(parts) {
		if m := reFromApp.FindStringSubmatch(parts[i]); m != nil {
			parts[i] = locales.GwAppFrom(appFiles(m[1]))
			i++
			if i < len(parts) {
				if m := reReplaces.FindStringSubmatch(parts[i]); m != nil {
					parts[i] = locales.GwAppReplaces(m[1])
				}
			}
		}
	}
	return strings.Join(parts, " · ")
}

var (
	reReconnecting = re(`^Offline · reconnecting for (.*)$`)
	reOfflineWhy   = re(`^Offline · (.*)$`)
	reNeedsYou     = re(`^Needs you · (.*)$`)
	reWaitingYou   = re(`^Waiting for you · (.*)$`)
	reLockedTask   = re(`^Locked · unlock to continue (.*)$`)
	reInControl    = re(`^You're in control · (.+) is paused$`)
	reBackground   = re(`^(.*) · in the background$`)
)

// statusLine is a phone's status line in the sidebar, the tiles and the
// menu bar flyout.
func statusLine(d gateway.DeviceView) string {
	s := d.StatusLine
	if !translated() {
		return s
	}
	switch s {
	case "Offline":
		return locales.GwStatusOffline()
	case "Paused":
		return locales.GwStatusPaused()
	case "Locked":
		return locales.GwStatusLocked()
	case "Idle":
		return locales.GwStatusIdle()
	case "You're in control":
		return locales.GwStatusInControl()
	case "Needs setup":
		return locales.GwStatusNeedsSetup()
	case "Camera or microphone in use":
		return locales.GwStatusMediaInUse()
	case "Found on Wi-Fi":
		return locales.GwStatusFoundOnWiFi()
	case "Not the same phone":
		return locales.GwStatusPhoneChanged()
	case "Locked on Wi-Fi":
		return locales.GwStatusLockedOnWiFi()
	case "Lost on Wi-Fi":
		return locales.GwStatusLostOnWiFi()
	case "Not seen on Wi-Fi":
		return locales.GwStatusNotSeenOnWiFi()
	case "Connecting":
		return locales.GwStatusConnecting()
	case "Moving to Wi-Fi":
		return locales.GwStatusMovingWiFi()
	case "Moving to the cable":
		return locales.GwStatusMovingCable()
	case "Switching to the helper":
		return locales.GwStatusSwitchHelper()
	case "Switching to CoreDevice":
		return locales.GwStatusSwitchCoreDevice()
	}
	if m := reReconnecting.FindStringSubmatch(s); m != nil {
		return locales.GwStatusReconnecting(m[1])
	}
	if m := reOfflineWhy.FindStringSubmatch(s); m != nil {
		return locales.GwStatusOfflineDetail(m[1])
	}
	if m := reNeedsYou.FindStringSubmatch(s); m != nil {
		return locales.GwStatusNeedsYou(m[1])
	}
	if m := reWaitingYou.FindStringSubmatch(s); m != nil {
		return locales.GwStatusWaiting(approvalText(m[1]))
	}
	if m := reLockedTask.FindStringSubmatch(s); m != nil {
		return locales.GwStatusLockedTask(m[1])
	}
	if m := reInControl.FindStringSubmatch(s); m != nil {
		return locales.GwStatusInControlPaused(m[1])
	}
	if m := reBackground.FindStringSubmatch(s); m != nil && d.Status == gateway.DeviceWorking {
		return locales.GwStatusBackground(m[1])
	}
	return s
}

var reNotWritable = re(`^Pinky can't write to (.+), where it is installed\.$`)

// whereProblems are internal/update's reasons why this copy of Pinky
// cannot replace itself (Location.Problem).
var whereProblems = map[string]func() string{
	"This Pinky doesn't run from Pinky.app, so it can't replace itself.":                                                           locales.UpdateWhereNotApp,
	"macOS runs this Pinky from a temporary copy. Move Pinky to Applications and open it from there to get updates automatically.": locales.UpdateWhereTranslocated,
	"Pinky runs from its disk image. Drag it to Applications and open it from there to get updates automatically.":                 locales.UpdateWhereDiskImage,
	"Pinky.app belongs to another user of this Mac, so Pinky can't replace it.":                                                    locales.UpdateWhereOtherUser,
	"This copy of Pinky isn't signed by Pinky's developer (a build made on this Mac), so it doesn't replace itself.":               locales.UpdateWhereUnsigned,
	"Pinky could not find where it is installed.":                                                                                  locales.UpdateWhereUnknown,
}

// whereProblem says why this copy of Pinky cannot update itself, as
// internal/update words it, in the app's language.
func whereProblem(p string) string {
	if !translated() {
		return p
	}
	if f := whereProblems[p]; f != nil {
		return f()
	}
	if m := reNotWritable.FindStringSubmatch(p); m != nil {
		return locales.UpdateWhereNotWritable(m[1])
	}
	return p
}

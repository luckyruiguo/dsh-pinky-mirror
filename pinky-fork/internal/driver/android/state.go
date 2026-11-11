package android

import (
	"regexp"
	"strconv"
	"strings"
)

// Parsers for dumpsys output. Android versions word these differently, so
// each parser accepts every form seen on the devices Pinky supports
// (Android 10 to 17).

var keyguardRE = regexp.MustCompile(`(?m)^\s*(showing|mIsShowing|isKeyguardShowing|mShowingLockscreen|isStatusBarKeyguard)=(true|false)\b`)

// parseKeyguard reports whether the lock screen is showing, from
// `dumpsys window policy`. It returns ok=false when the output has no
// keyguard state at all.
func parseKeyguard(out string) (showing, ok bool) {
	for _, m := range keyguardRE.FindAllStringSubmatch(out, -1) {
		ok = true
		if m[2] == "true" {
			return true, true
		}
	}
	return false, ok
}

var imeWindowRE = regexp.MustCompile(`\b(isVisible|isOnScreen)=(true|false)\b`)

// parseIMEWindowVisible reports whether `dumpsys window InputMethod` shows
// a visible keyboard window.
func parseIMEWindowVisible(out string) bool {
	for _, m := range imeWindowRE.FindAllStringSubmatch(out, -1) {
		if m[2] == "true" {
			return true
		}
	}
	return false
}

var focusedAppRE = regexp.MustCompile(`mFocusedApp=.*?ActivityRecord\{[0-9a-f]+ u\d+ ([A-Za-z0-9_.]+)/`)

// parseFocusedApp returns the package of the focused activity, from
// `dumpsys window displays` or `dumpsys window`.
func parseFocusedApp(out string) string {
	if m := focusedAppRE.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

var (
	wakefulnessRE = regexp.MustCompile(`mWakefulness=(\w+)`)
	displayIDRE   = regexp.MustCompile(`mDisplayId=\s*:?\s*(-?\d+)`)
	screenStateRE = regexp.MustCompile(`mScreenState=(\w+)`)
)

// parseScreenOn reports whether display 0 is on, from `dumpsys display`:
// each display power controller prints its display id before its
// mScreenState (Android 10 has one controller and may print no id). ON
// and VR count as on; OFF, DOZE, DOZE_SUSPEND and ON_SUSPEND (an always-on
// display) as off. Without any mScreenState it falls back to the
// wakefulness in `dumpsys power`.
func parseScreenOn(out string) (on, ok bool) {
	cur := -1
	for _, line := range strings.Split(out, "\n") {
		if m := displayIDRE.FindStringSubmatch(line); m != nil {
			cur, _ = strconv.Atoi(m[1])
		}
		if m := screenStateRE.FindStringSubmatch(line); m != nil && cur <= 0 {
			return m[1] == "ON" || m[1] == "VR", true
		}
	}
	return parseAwake(out)
}

// parseAwake reports whether the screen is on, from `dumpsys power`.
// Dozing (always-on display) counts as off.
func parseAwake(out string) (awake, ok bool) {
	m := wakefulnessRE.FindStringSubmatch(out)
	if m == nil {
		return false, false
	}
	return m[1] == "Awake", true
}

var sizeRE = regexp.MustCompile(`(Physical|Override) size: (\d+)x(\d+)`)

// parseWMSize returns the display size from `wm size`: the override when
// one is set, else the physical size, in the natural orientation.
func parseWMSize(out string) (w, h int, ok bool) {
	for _, m := range sizeRE.FindAllStringSubmatch(out, -1) {
		mw, _ := strconv.Atoi(m[2])
		mh, _ := strconv.Atoi(m[3])
		if m[1] == "Override" || !ok {
			w, h, ok = mw, mh, true
		}
	}
	return w, h, ok
}

// rotate returns the size of a w x h display after quarter turns.
func rotate(w, h, rotation int) (int, int) {
	if rotation%2 == 1 {
		return h, w
	}
	return w, h
}

// orient returns the size of a w x h display (in any orientation) laid out
// like a frame of fw x fh: landscape frames give the longer side first.
func orient(w, h, fw, fh int) (int, int) {
	long, short := max(w, h), min(w, h)
	if fw > fh {
		return long, short
	}
	return short, long
}

// splitSections cuts the output of several commands separated by
// "--name--" marker lines into a map.
func splitSections(out string) map[string]string {
	sec := map[string]string{}
	name := ""
	var b strings.Builder
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if len(t) > 4 && strings.HasPrefix(t, "--") && strings.HasSuffix(t, "--") && !strings.Contains(t, " ") {
			sec[name] = b.String()
			b.Reset()
			name = strings.Trim(t, "-")
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	sec[name] = b.String()
	return sec
}

var curSizeRE = regexp.MustCompile(`\bcur=(\d+)x(\d+)`)

// parseCurSize returns the first display's current size, rotation
// included, from `dumpsys window displays` ("init=1080x2400 420dpi
// cur=1080x2400 app=...").
func parseCurSize(out string) (w, h int, ok bool) {
	m := curSizeRE.FindStringSubmatch(out)
	if m == nil {
		return 0, 0, false
	}
	w, _ = strconv.Atoi(m[1])
	h, _ = strconv.Atoi(m[2])
	return w, h, w > 0 && h > 0
}

// rootTask is a root task (a stack before Android 12) and its tasks, from
// `cmd activity stack list`.
type rootTask struct {
	id, display int
	tasks       []int
}

var (
	rootTaskRE = regexp.MustCompile(`^(?:RootTask|Stack) id=(-?\d+)\b.*\bdisplayId=(-?\d+)`)
	taskIDRE   = regexp.MustCompile(`^\s+taskId=(\d+):`)
)

// parseRootTasks reads `cmd activity stack list` ("RootTask id=125 ...
// displayId=0 ..." on Android 12 and later, "Stack id=60 ... displayId=0
// ..." before, each followed by "  taskId=66: pkg/cls ..." lines).
func parseRootTasks(out string) []rootTask {
	var roots []rootTask
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		if m := rootTaskRE.FindStringSubmatch(line); m != nil {
			id, _ := strconv.Atoi(m[1])
			disp, _ := strconv.Atoi(m[2])
			roots = append(roots, rootTask{id: id, display: disp})
			continue
		}
		if m := taskIDRE.FindStringSubmatch(line); m != nil && len(roots) > 0 {
			t, _ := strconv.Atoi(m[1])
			roots[len(roots)-1].tasks = append(roots[len(roots)-1].tasks, t)
		}
	}
	return roots
}

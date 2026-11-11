package companion

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// probeOut builds probeCmd's output from the sections given, in its
// order.
func probeOut(sections map[string]string) string {
	var b strings.Builder
	for _, name := range []string{"setting", "user", "pid", "socket", "a11y", "log", "now", "crash", "package", "record", "bad", "system"} {
		if s, ok := sections[name]; ok {
			b.WriteString(probeMark + name + "\n" + s)
		}
	}
	return b.String()
}

// What an Android 10 emulator showed while `uiautomator events` ran:
// Pinky's service enabled, and nothing bound.
var probeUIAutomation = map[string]string{
	"setting": "app.pinky.companion/.PinkyAccessibilityService\n",
	"user":    "RUNNING_UNLOCKED\n",
	"pid":     "1882\n",
	"socket":  "0\n",
	"a11y": `User state[attributes:{id=0, currentUser=true, touchExplorationEnabled=false, displayMagnificationEnabled=false, navBarMagnificationEnabled=false, autoclickEnabled=false, nonInteractiveUiTimeout=0, interactiveUiTimeout=0, installedServiceCount=1, Ui Automation[eventTypes=TYPES_ALL_MASK, notificationTimeout=0]
     Bound services:{}
     Enabled services:{{app.pinky.companion/app.pinky.companion.PinkyAccessibilityService}}
     Binding services:{}]
`,
	"log":   "I/PinkyServer: socket closed\n",
	"now":   "1794287345\n",
	"crash": "",
}

func TestProbe(t *testing.T) {
	with := func(base map[string]string, changes map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range changes {
			m[k] = v
		}
		return m
	}
	for _, c := range []struct {
		name     string
		app      App
		sections map[string]string
		reason   string
		facts    []string
		not      []string
	}{
		{
			name: "ui automation", app: Release, sections: probeUIAutomation,
			reason: driver.CompanionStartUIAutomation,
			facts: []string{"no answer within 10s", "Android lists it as enabled but has not bound it",
				"another tool holds Android's UI automation", "its app is running", "its socket is not listening",
				`its last log line: "I/PinkyServer: socket closed"`},
			not: []string{"took it off"},
		},
		{
			// A tool that keeps accessibility services on
			// (FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES) is not the reason.
			name: "bound next to ui automation", app: Release,
			sections: with(probeUIAutomation, map[string]string{"a11y": `User state[attributes:{id=0, installedServiceCount=2, Ui Automation[eventTypes=TYPES_ALL_MASK, notificationTimeout=0]
     Bound services:{Service[label=Other, feedbackType[FEEDBACK_SPOKEN], capabilities=1], Service[label=Pinky Companion, feedbackType[FEEDBACK_GENERIC], capabilities=1, eventTypes=TYPES_ALL_MASK, notificationTimeout=0]}
     Enabled services:{{com.example.other/com.example.other.Svc}{app.pinky.companion/app.pinky.companion.PinkyAccessibilityService}}
     Binding services:{}]
`, "socket": "1\n"}),
			facts: []string{"Android has bound it", "its socket is listening"},
		},
		{
			// Android 11 and later list crashed services; the development
			// build's label is not the release build's.
			name: "crashed", app: Release,
			sections: with(probeUIAutomation, map[string]string{"a11y": `User state[attributes:{id=0, installedServiceCount=2}
     Bound services:{Service[label=Pinky Companion (dev), feedbackType[FEEDBACK_GENERIC]]}
     Enabled services:{{app.pinky.companion.dev/app.pinky.companion.PinkyAccessibilityService}{app.pinky.companion/app.pinky.companion.PinkyAccessibilityService}}
     Binding services:{}
     Crashed services:{{app.pinky.companion/.PinkyAccessibilityService}}]
`, "pid": "", "crash": `1794287340.123  1882  1882 E AndroidRuntime: Process: app.pinky.companion, PID: 1882
1794287340.123  1882  1882 E AndroidRuntime: java.lang.IllegalStateException: boom
--
1794200000.000  1700  1700 E AndroidRuntime: Process: app.pinky.companion, PID: 1700
1794200000.000  1700  1700 E AndroidRuntime: java.lang.OutOfMemoryError: old
`}),
			facts: []string{"Android lists it as enabled but has not bound it", "Android lists it as crashed",
				"its app is not running", "it crashed 4s ago: java.lang.IllegalStateException: boom"},
			not: []string{"old", "UI automation"},
		},
		{
			name: "binding, not unlocked", app: Dev,
			sections: with(probeUIAutomation, map[string]string{
				"setting": "app.pinky.companion.dev/app.pinky.companion.PinkyAccessibilityService\n",
				"user":    "RUNNING_LOCKED\n",
				"a11y": `User state[attributes:{id=0, installedServiceCount=0}
     Bound services:{}
     Enabled services:{{app.pinky.companion.dev/app.pinky.companion.PinkyAccessibilityService}}
     Binding services:{{app.pinky.companion.dev/app.pinky.companion.PinkyAccessibilityService}}]
`}),
			reason: driver.CompanionStartNotUnlocked,
			facts:  []string{"Android is still binding it", "the phone was not unlocked since it started"},
		},
		{
			// Android 17, with a PIN, before the first unlock and while
			// `uiautomator events` ran: unlocking comes first.
			name: "not unlocked, ui automation", app: Release,
			sections: with(probeUIAutomation, map[string]string{"user": "RUNNING_LOCKED\n", "pid": "", "log": "", "a11y": `     Bound services:{}
     Enabled services:{{app.pinky.companion/app.pinky.companion.PinkyAccessibilityService}}
     Binding services:{}
     Crashed services:{}
Ui Automation[eventTypes=TYPES_ALL_MASK, notificationTimeout=0]
`}),
			reason: driver.CompanionStartNotUnlocked,
			facts: []string{"Android lists it as enabled but has not bound it", "another tool holds Android's UI automation",
				"the phone was not unlocked since it started", "its app is not running"},
		},
		{
			name: "taken off the list", app: Release,
			sections: with(probeUIAutomation, map[string]string{"setting": "com.example.other/.Svc\n", "a11y": `     Bound services:{}
     Enabled services:{{com.example.other/com.example.other.Svc}}
     Binding services:{}]
`}),
			facts: []string{"something took it off enabled_accessibility_services again", "Android does not list it as enabled"},
		},
		{
			// Issue #56, a Huawei Mate 10 on EMUI (Android 10) after the
			// person turned off its launch manager's auto-manage: the
			// setting stays and Android binds, but the app's process
			// never starts and it never logs. logcat prints its header
			// alone, which is not a log line of the app. What the system
			// said is made up here: no EMUI phone was at hand.
			name: "binding, process never started", app: Release,
			sections: with(probeUIAutomation, map[string]string{
				"pid":    "",
				"socket": "0\n",
				"log":    "--------- beginning of main\n",
				"a11y": `User state[attributes:{id=0, installedServiceCount=1}
     Bound services:{}
     Enabled services:{{app.pinky.companion/app.pinky.companion.PinkyAccessibilityService}}
     Binding services:{{app.pinky.companion/app.pinky.companion.PinkyAccessibilityService}}]
`,
				"crash":   "--------- beginning of crash\n",
				"package": "stopped=true\nnotLaunched=true\n",
				"record": `  * ServiceRecord{5d1c2e0 u0 app.pinky.companion/.PinkyAccessibilityService}
    app=null
    restartDepth=0 restartCount=3 restartDelay=+40s0ms restartTime=-2s crashCount=2
`,
				"bad": "",
				"system": `--------- beginning of events
1794287200.000  1000  1100 I am_proc_start: [0,9000,10200,app.pinky.companion,service,{app.pinky.companion/app.pinky.companion.PinkyAccessibilityService}]
1794287338.100  1000  1100 I am_proc_start: [0,9100,10200,app.pinky.companion,service,{app.pinky.companion/app.pinky.companion.PinkyAccessibilityService}]
1794287338.200  1000  1100 I am_kill : [0,9100,app.pinky.companion,900,startup forbidden]
1794287338.300  1000  1100 I am_kill : [0,9100,app.pinky.companion,900,startup forbidden]
`,
			}),
			facts: []string{"Android is still binding it", "its app is not running", "its package is stopped",
				"its app was never opened", "Android's record of the service has no process",
				"Android restarted it 3 times", "it crashed 2 times",
				`the system said: "I/am_proc_start: [0,9100,10200,app.pinky.companion,service,{app.pinky.companion/app.pinky.companion.PinkyAccessibilityService}]", "I/am_kill : [0,9100,app.pinky.companion,900,startup forbidden]"`},
			not: []string{"last log line", "beginning of", "[0,9000,", "bad process", "it crashed: ", "UI automation"},
		},
		{
			// A crash from an earlier try, minutes ago, still counts; one
			// from long before does not. The app counts as a bad process.
			name: "crashed on an earlier try", app: Release,
			sections: with(probeUIAutomation, map[string]string{
				"pid":  "",
				"a11y": "     Bound services:{}\n     Enabled services:{{app.pinky.companion/.PinkyAccessibilityService}}\n",
				"crash": `1794287045.000  1882  1882 E AndroidRuntime: Process: app.pinky.companion, PID: 1882
1794287045.000  1882  1882 E AndroidRuntime: java.lang.NoSuchMethodError: no method
`,
				"bad": "    Bad process app.pinky.companion uid 10200: crashed at time 1794287045000\n",
			}),
			facts: []string{"it crashed 300s ago: java.lang.NoSuchMethodError: no method", "Android counts its app as a bad process"},
		},
		{
			// A phone whose shell lacks some commands: what came back
			// still counts, and nothing is made up for the rest.
			name: "little came back", app: Release,
			sections: map[string]string{"setting": "null\n"},
			facts:    []string{"something took it off", "dumpsys accessibility showed nothing"},
			not:      []string{"its app", "its socket", "unlocked"},
		},
	} {
		e := parseProbe(probeOut(c.sections), c.app).startError(10 * time.Second)
		if e.Reason != c.reason {
			t.Errorf("%s: reason %q, want %q (%s)", c.name, e.Reason, c.reason, e.Detail)
		}
		for _, f := range c.facts {
			if !strings.Contains(e.Detail, f) {
				t.Errorf("%s: no %q in %q", c.name, f, e.Detail)
			}
		}
		for _, f := range c.not {
			if strings.Contains(e.Detail, f) {
				t.Errorf("%s: %q in %q", c.name, f, e.Detail)
			}
		}
	}
}

// The service never answers: Enable reads why before it restores the
// setting, and its error does not look like the caller's time running out
// (issue #56: the client told the person the install had not finished).
func TestEnableSaysWhyServiceDidNotStart(t *testing.T) {
	defer func(w time.Duration) { startWait = w }(startWait)
	startWait = 300 * time.Millisecond
	p := newFakePhone(t)
	p.mu.Lock()
	p.rewrite = func(v string) string { return strings.ReplaceAll(v, Release.Component(), "") }
	p.probe = probeOut(probeUIAutomation)
	p.mu.Unlock()
	c := New(p)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := c.Enable(ctx)
	var se *driver.CompanionStartError
	if !errors.As(err, &se) || se.Reason != driver.CompanionStartUIAutomation {
		t.Fatalf("Enable = %v, want a CompanionStartError for UI automation", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the error wraps context.DeadlineExceeded: %v", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	probed := slices.IndexFunc(p.shell, func(s string) bool { return strings.HasPrefix(s, "echo "+probeMark) })
	restored := slices.Index(p.shell, "settings delete secure "+settingKey)
	if probed < 0 || restored < 0 || probed > restored {
		t.Errorf("probe at %d, restore at %d, in %q", probed, restored, p.shell)
	}
	if _, ok := p.settings[settingKey]; ok {
		t.Errorf("setting left behind: %q", p.settings[settingKey])
	}

	// A phone that cannot say: the error still says the service did not
	// answer.
	p.probe = ""
	p.mu.Unlock()
	err = c.Enable(ctx)
	p.mu.Lock()
	if !errors.As(err, &se) || se.Reason != "" || !strings.Contains(se.Detail, "no answer within") {
		t.Errorf("Enable without a probe = %v", err)
	}
}

// The system's lines leave out those that name the person's other
// accessibility services (Huawei's SPMN logs the whole setting, GitHub
// issue #56) and traffic counters.
func TestProbeCmdLeavesOutOtherServices(t *testing.T) {
	cmd := probeCmd(App{Package: "app.pinky.companion", Socket: "pinky_companion"})
	for _, want := range []string{"-e " + settingKey, "-e HwUidTcpMonitor", "-e SettingsShellCmd"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("probeCmd lacks %q", want)
		}
	}
}

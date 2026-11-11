package android

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// A logcat dump as `logcat -d -v epoch -v uid` writes it, from an Android
// 16 emulator and an Android 10 one (where uids of system processes are
// names), with lines of other apps mixed in.
const logcatSample = `--------- beginning of main
         1794218293.100 10213  8566  8566 I CrashTest: button pressed
         1794218293.200 10220  9100  9100 I Other   : someone else's line
         1794218293.300  root   472   472 I zygote64: option[5]=-XX:PerfettoHprof=true
         1794218293.400 u0_a213  8566  8570 D CrashTest: from a named uid
--------- beginning of crash
         1794218293.491 10213  8566  8566 E AndroidRuntime: FATAL EXCEPTION: main
         1794218293.491 10213  8566  8566 E AndroidRuntime: Process: dev.pinky.crashtest, PID: 8566
         1794218293.491 10213  8566  8566 E AndroidRuntime: java.lang.IllegalStateException: Crash button tapped
         1794218293.491 10213  8566  8566 E AndroidRuntime: 	at dev.pinky.crashtest.Main.lambda$onCreate$1(Main.java:26)
         1794218293.600 1010213  8600  8600 I CrashTest: the clone's line
         1794218293.700 10213  8566  8566 W HWUI    : Davey! duration=20000ms
         1794218293.800 10213  8566  8566 I Empty:
not a log line
`

func TestReadLogcat(t *testing.T) {
	res, unknown, err := readLogcat(strings.NewReader(logcatSample), 10213, driver.LogQuery{Max: 100})
	if err != nil || unknown {
		t.Fatal(err, unknown)
	}
	var got []string
	for _, l := range res.Lines {
		got = append(got, string(l.Level.Letter())+" "+l.Tag+": "+l.Text)
	}
	want := []string{
		"I CrashTest: button pressed",
		"D CrashTest: from a named uid",
		"E AndroidRuntime: FATAL EXCEPTION: main",
		"E AndroidRuntime: Process: dev.pinky.crashtest, PID: 8566",
		"E AndroidRuntime: java.lang.IllegalStateException: Crash button tapped",
		"E AndroidRuntime: \tat dev.pinky.crashtest.Main.lambda$onCreate$1(Main.java:26)",
		"W HWUI: Davey! duration=20000ms",
		"I Empty: ",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lines:\n%s", strings.Join(got, "\n"))
	}
	if l := res.Lines[0]; l.PID != 8566 || !l.Time.Equal(time.UnixMilli(1794218293100)) {
		t.Fatalf("first line %+v", l)
	}

	// The newest Max lines, of the level, since a time.
	q := driver.LogQuery{Max: 2, Since: time.UnixMilli(1794218293150)}
	res, _, _ = readLogcat(strings.NewReader(logcatSample), 10213, q)
	if len(res.Lines) != 2 || res.Dropped != 5 || res.Lines[1].Tag != "Empty" || res.Lines[0].Tag != "HWUI" {
		t.Fatalf("max 2: %+v", res)
	}
	q = driver.LogQuery{Max: 10, Level: driver.LevelWarn, Filter: "davey"}
	res, _, _ = readLogcat(strings.NewReader(logcatSample), 10213, q)
	if len(res.Lines) != 1 || res.Lines[0].Tag != "HWUI" {
		t.Fatalf("warn, davey: %+v", res)
	}
	res, _, _ = readLogcat(strings.NewReader(logcatSample), 1010213, driver.LogQuery{})
	if len(res.Lines) != 1 || res.Lines[0].Text != "the clone's line" {
		t.Fatalf("the clone: %+v", res)
	}

	// Android 10's logcat has no --uid.
	_, unknown, _ = readLogcat(strings.NewReader("Unrecognized Option  \nUsage: logcat [options] [filterspecs]\n"), 10213, driver.LogQuery{})
	if !unknown {
		t.Fatal("unknown option not noticed")
	}

	// A line too long for the reader is cut, and the lines after it read.
	long := "         1794218293.100 10213  8566  8566 I Long: " + strings.Repeat("x", 200_000) + "\n" +
		"         1794218293.200 10213  8566  8566 I After: ok\n"
	res, _, _ = readLogcat(strings.NewReader(long), 10213, driver.LogQuery{})
	if len(res.Lines) != 2 || len(res.Lines[0].Text) >= logcatLineMax || res.Lines[1].Text != "ok" {
		t.Fatalf("long line: %d lines", len(res.Lines))
	}
}

func TestLogcatCommand(t *testing.T) {
	q := driver.LogQuery{Since: time.UnixMilli(1794218293045), Level: driver.LevelWarn}
	if got := logcatCommand(10213, "main,system,crash", q, true); got !=
		"logcat -d -b main,system,crash -v epoch -v uid --uid=10213 -t 1794218293.045 '*:W' 2>&1" {
		t.Fatalf("command %q", got)
	}
	if got := logcatCommand(10213, "crash", driver.LogQuery{}, false); got != "logcat -d -b crash -v epoch -v uid 2>&1" {
		t.Fatalf("command %q", got)
	}
}

func TestReadDropbox(t *testing.T) {
	f, err := os.Open("testdata/applogs/dropbox.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	since := time.Date(2026, 11, 9, 17, 0, 0, 0, time.FixedZone("", 8*3600))
	reports, err := readDropbox(f, "dev.pinky.crashtest", 10213, since)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 3 {
		t.Fatalf("%d reports: %+v", len(reports), reports)
	}
	crash, native, anr := reports[0], reports[1], reports[2]
	if crash.Kind != "crash" || crash.PID != 8566 || crash.Process != "dev.pinky.crashtest" ||
		crash.Summary != "java.lang.IllegalStateException: Crash button tapped" || !strings.Contains(crash.Text, "Main.java:26") ||
		!crash.Time.Equal(time.Date(2026, 11, 9, 17, 58, 13, 497e6, time.FixedZone("", 8*3600))) {
		t.Fatalf("crash %+v", crash)
	}
	if native.Kind != "native crash" || native.PID != 8650 ||
		native.Summary != "signal 6 (SIGABRT), code -1 (SI_QUEUE), fault addr -------- · Abort message: 'native crash button'" ||
		!strings.Contains(native.Text, "libcrash.so") {
		t.Fatalf("native %+v", native)
	}
	if anr.Kind != "ANR" || anr.PID != 8726 || !strings.HasPrefix(anr.Summary, "Input dispatching timed out") ||
		!strings.HasPrefix(anr.Text, "----- pid 8726 at ") || !strings.HasSuffix(anr.Text, "----- end 8726 -----") ||
		!strings.Contains(anr.Text, `"main" prio=5 tid=1 Sleeping`) {
		t.Fatalf("ANR %+v", anr)
	}
	// Nothing of other processes: their CPU use, their threads, events.
	for _, r := range reports {
		for _, other := range []string{"surfaceflinger", "system_server", "com.example.other", "someone else", "old crash", "CriticalEventLog"} {
			if strings.Contains(r.Text+r.Summary, other) {
				t.Errorf("%s report has %q", r.Kind, other)
			}
		}
	}
}

func TestCrashBufferReports(t *testing.T) {
	res, _, _ := readLogcat(strings.NewReader(logcatSample), 10213, driver.LogQuery{})
	reports := crashBufferReports(res.Lines)
	if len(reports) != 1 {
		t.Fatalf("reports %+v", reports)
	}
	r := reports[0]
	if r.PID != 8566 || r.Process != "dev.pinky.crashtest" || r.Summary != "java.lang.IllegalStateException: Crash button tapped" ||
		!strings.HasPrefix(r.Text, "FATAL EXCEPTION: main\n") || !strings.Contains(r.Text, "Main.java:26") || strings.Contains(r.Text, "Davey") {
		t.Fatalf("report %+v", r)
	}
}

func TestDebuggable(t *testing.T) {
	b, err := os.ReadFile("testdata/applogs/dumpsys-package.txt")
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	if !debuggable(out, "dev.pinky.crashtest") {
		t.Fatal("debug build not debuggable")
	}
	release := strings.ReplaceAll(out, "DEBUGGABLE ", "")
	if debuggable(release, "dev.pinky.crashtest") {
		t.Fatal("release build debuggable")
	}
	if debuggable(out, "dev.pinky") {
		t.Fatal("another package's flags")
	}
}

func TestPackageUIDs(t *testing.T) {
	m := packageUIDs("package:com.android.cts.priv.ctsshim uid:10051\r\npackage:dev.pinky.crashtest uid:10213\npackage:com.example.multi uid:10300,1010300\nnoise\n")
	if m["dev.pinky.crashtest"] != 10213 || m["com.example.multi"] != 10300 || len(m) != 3 {
		t.Fatalf("uids %v", m)
	}
	if n, ok := parseUID("u10_a213"); !ok || n != 1010213 {
		t.Fatalf("u10_a213: %d", n)
	}
	for _, s := range []string{"root", "system", "u0_a", "u0_a213x"} {
		if _, ok := parseUID(s); ok {
			t.Errorf("%q read as a uid", s)
		}
	}
}

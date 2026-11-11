//go:build device

package ios

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestDeviceSpringBoardProbe watches what SpringBoard lists while Settings
// is in front, for PINKY_PROBE_SECONDS (60): have a notification arrive or
// a timer go off meanwhile. It writes every change of SpringBoard's tree to
// /tmp/pinky-ios-probe/springboard.txt (delete it afterwards: it holds the
// notifications' text).
func TestDeviceSpringBoardProbe(t *testing.T) {
	d := testDevice(t)
	secs := 60
	if v, err := strconv.Atoi(os.Getenv("PINKY_PROBE_SECONDS")); err == nil && v > 0 {
		secs = v
	}
	ctx := ctxFor(t, time.Duration(secs+60)*time.Second)
	w := probeOut(t, "springboard.txt")
	procs, err := d.processPaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sbPID := 0
	for pid, exe := range procs {
		if strings.HasPrefix(exe, "/System/Library/CoreServices/SpringBoard.app/") {
			sbPID = int(pid)
		}
	}
	if sbPID == 0 {
		t.Fatal("no SpringBoard process")
	}
	var names []string
	for pid, exe := range procs {
		names = append(names, fmt.Sprintf("%d %s", pid, exe[strings.LastIndex(exe, "/")+1:]))
	}
	sort.Strings(names)
	fmt.Fprintf(w, "processes: %s\n", strings.Join(names, ", "))
	// Only SpringBoard: asking for the application element of a process
	// without one (a daemon, the widget renderer) gets no answer, and the
	// session that timed out is dropped.
	if err := d.Launch(ctx, "com.apple.Preferences"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	rax, err := d.raxReady(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	sb, err := rax.appObject(c, sbPID)
	cancel()
	if err != nil {
		t.Fatalf("SpringBoard's application element: %v", err)
	}
	fmt.Fprintf(w, "SpringBoard pid %d, application element %+v\n", sbPID, *sb)
	t.Logf("watching SpringBoard for %d s: have a notification arrive or a timer go off now", secs)
	last := ""
	end := time.Now().Add(time.Duration(secs) * time.Second)
	for time.Now().Before(end) {
		rax, err := d.raxReady(ctx)
		if err != nil {
			fmt.Fprintf(w, "%s rax: %v\n", time.Now().Format("15:04:05.000"), err)
			time.Sleep(time.Second)
			continue
		}
		c, cancel := context.WithTimeout(ctx, raxLimits.Budget+raxRequestTimeout)
		t0 := time.Now()
		front, ferr := rax.frontmostApp(c)
		els, complete, err := rax.walkFrom(c, sb, walkLimits{MaxElements: 120, Budget: 3 * time.Second}, nil)
		cancel()
		var b strings.Builder
		if ferr == nil {
			fmt.Fprintf(&b, "frontmost pid %d\n", front.PID)
			if front.PID != sbPID {
				fels, _, _ := rax.walkFrom(c, front, walkLimits{MaxElements: 120, Budget: 3 * time.Second}, nil)
				fmt.Fprintf(&b, "front has %d elements\n", len(fels))
				for i, e := range fels {
					if e.HasFrame && e.Frame.Y < 120 {
						fmt.Fprintf(&b, "front %d|%d|%d|%#x|%q|%q|%v\n", i, e.Type, e.AXRole, e.Traits, e.Label, e.Ident, e.Frame)
					}
				}
			}
		}
		if cur := b.String(); cur != last || err != nil {
			fmt.Fprintf(w, "\n## %s walk %v, %d elements, complete %v, err %v\n", time.Now().Format("15:04:05.000"), time.Since(t0).Round(time.Millisecond), len(els), complete, err)
			dumpRAX(w, "springboard", els)
			fmt.Fprint(w, b.String())
			if ferr == nil {
				fmt.Fprintf(w, "frontmost: %+v (err %v)\n", *front, ferr)
			}
			last = cur
		}
		time.Sleep(400 * time.Millisecond)
	}
	home(ctx, d)
}

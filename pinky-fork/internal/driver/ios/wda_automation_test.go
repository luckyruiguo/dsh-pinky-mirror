package ios

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	goios "github.com/danielpaulus/go-ios/ios"
)

// The runner's log on the iPhone 13 mini (iOS 27.0.1) the first time it ran
// after a restart, until the phone asked for its passcode.
const automationLog = "12:35:05.530 AM WebDriverAgentRunner-Runner[1649:163564] <XCTWaiter: 0x140cf8cc0> entering wait loop for 600.00s with expectations: `requesting test configuration`\n" +
	"12:35:05.533 AM WebDriverAgentRunner-Runner[1649:163564] Returning result XCTWaiterResultCompleted for synchronous waiter <XCTWaiter: 0x140cf8cc0>\n" +
	"12:35:05.614 AM WebDriverAgentRunner-Runner[1649:163564] Creating future for 'enabling automation mode' with timeout 60.00\n" +
	"12:35:05.614 AM WebDriverAgentRunner-Runner[1649:163564] <XCTWaiter: 0x1403a4b70> entering wait loop for 60.00s with expectations: `enabling automation mode`\n"

const automationDone = "12:35:19.020 AM WebDriverAgentRunner-Runner[1649:163564] Returning result XCTWaiterResultCompleted for synchronous waiter <XCTWaiter: 0x1403a4b70>\n"

func TestRunnerLogAutomation(t *testing.T) {
	l := &runnerLog{}
	// The log arrives in pieces that split lines anywhere.
	for i := 0; i < len(automationLog); i += 37 {
		l.Write([]byte(automationLog[i:min(i+37, len(automationLog))]))
	}
	now := time.Now()
	if l.asking(now) {
		t.Fatal("asking before the grace: a wait that ends at once is no question")
	}
	if !l.asking(now.Add(automationGrace + time.Second)) {
		t.Fatalf("not asking %v into the automation wait (waiter %q)", automationGrace+time.Second, l.waiter)
	}
	l.Write([]byte(automationDone))
	if l.asking(now.Add(time.Minute)) {
		t.Fatal("still asking after the wait ended")
	}

	// Other waits are not the question.
	l = &runnerLog{}
	l.Write([]byte(strings.SplitAfter(automationLog, "\n")[0]))
	if l.asking(time.Now().Add(time.Minute)) {
		t.Fatal("the test configuration wait counted as the automation question")
	}

	// A huge line without a newline is cut, not kept whole.
	l = &runnerLog{}
	l.Write([]byte(strings.Repeat("x", 3*maxLogLine)))
	if len(l.line) != maxLogLine {
		t.Fatalf("kept %d bytes of a long line", len(l.line))
	}
}

// slowWDA is a WebDriverAgent that is not ready until ready is set.
type slowWDA struct{ ready atomic.Bool }

func (f *slowWDA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/status" {
		(&fakeWDA{}).ServeHTTP(w, r)
		return
	}
	fmt.Fprintf(w, `{"value":{"ready":%v,"os":{"version":"27.0.1"}},"sessionId":null}`, f.ready.Load())
}

// askingStart is a runner start whose phone waits for its passcode.
func askingStart(t *testing.T, base goios.DeviceEntry) (*wdaStart, *slowWDA) {
	f := &slowWDA{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	run := &wdaRun{cancel: func() {}, done: make(chan struct{}), log: &runnerLog{}}
	run.log.read("<XCTWaiter: 0x1403a4b70> entering wait loop for 60.00s with expectations: `enabling automation mode`", time.Now().Add(-time.Minute))
	return &wdaStart{base: base, c: newWDAURL(srv.URL, ""), run: run}, f
}

func TestWDAStartAsksForPasscode(t *testing.T) {
	s, _ := askingStart(t, goios.DeviceEntry{})
	t0 := time.Now()
	_, err := s.open(context.Background(), driver.Info{ID: "x", Platform: driver.IOS})
	if !errors.Is(err, errAutomation) {
		t.Fatalf("open: %v, want errAutomation", err)
	}
	if time.Since(t0) > 5*time.Second {
		t.Fatalf("open waited %v; it should give up once the phone asks", time.Since(t0))
	}
	if !s.running() {
		t.Fatal("the run was stopped; it must keep waiting for the passcode")
	}
}

func TestProviderWaitsForPasscode(t *testing.T) {
	now := time.Unix(1000, 0)
	p := NewProvider()
	p.now = func() time.Time { return now }
	p.inspectFn = func(_ context.Context, e goios.DeviceEntry) driver.Info {
		return driver.Info{ID: e.Properties.SerialNumber, Platform: driver.IOS, State: driver.StateOnline, Backend: backendWDA}
	}
	ctx := context.Background()
	e := entry("p", 1, "USB")
	if got := p.info(ctx, e); got.State != driver.StateOnline {
		t.Fatalf("before: %+v", got)
	}

	// The phone asks: List says so instead of starting the runner again.
	s, wda := askingStart(t, e)
	p.wdaWaits = map[string]*wdaStart{"p": s}
	if got := p.info(ctx, e); got.State != driver.StateNeedsSetup || got.Setup != driver.SetupPasscode || got.Detail != automationAsk {
		t.Fatalf("while asking: %+v", got)
	}
	// The passcode was entered: online, and the next Open takes the runner over.
	wda.ready.Store(true)
	if got := p.info(ctx, e); got.State != driver.StateOnline {
		t.Fatalf("after the passcode: %+v", got)
	}
	if p.wdaWaits["p"] != s {
		t.Fatal("the waiting runner was dropped before Open took it over")
	}

	// Nobody entered it: the runner gave up; no new runner for a while,
	// longer after each miss.
	wda.ready.Store(false)
	close(s.run.done)
	for i, wait := range []time.Duration{wdaHoldFirst, 2 * wdaHoldFirst} {
		if got := p.info(ctx, e); got.State != driver.StateNeedsSetup || got.Setup != driver.SetupPasscode || got.Detail != automationMissed {
			t.Fatalf("miss %d: %+v", i+1, got)
		}
		if _, ok := p.wdaWaits["p"]; ok {
			t.Fatalf("miss %d: the runner that gave up is still kept", i+1)
		}
		now = now.Add(wait - time.Second)
		if got := p.info(ctx, e); got.State != driver.StateNeedsSetup {
			t.Fatalf("miss %d, %v later: %+v", i+1, wait-time.Second, got)
		}
		now = now.Add(2 * time.Second)
		if got := p.info(ctx, e); got.State != driver.StateOnline {
			t.Fatalf("miss %d, %v later: %+v", i+1, wait+time.Second, got)
		}
		// Asked again, and missed again.
		s, wda = askingStart(t, e)
		close(s.run.done)
		p.wdaWaits["p"] = s
	}
	if h := p.wdaHolds["p"]; h.misses != 2 {
		t.Fatalf("misses %d, want 2", h.misses)
	}

	// Choosing the connection again asks at once.
	p.SetConnection("p", backendWDA)
	if got := p.info(ctx, e); got.State != driver.StateOnline {
		t.Fatalf("after choosing again: %+v", got)
	}

	// A runner from before the phone reconnected is dropped, and is no miss.
	s, _ = askingStart(t, e)
	close(s.run.done)
	p.wdaWaits["p"] = s
	e2 := entry("p", 2, "USB")
	p.info(ctx, e2)
	if got := p.info(ctx, e2); got.State != driver.StateOnline {
		t.Fatalf("after reconnecting: %+v", got)
	}
	if _, ok := p.wdaWaits["p"]; ok {
		t.Fatal("the old runner is still kept")
	}
	if _, ok := p.wdaHolds["p"]; ok {
		t.Fatal("a reconnect counted as a missed passcode")
	}

	// A phone that is gone takes its runner with it.
	s, _ = askingStart(t, e2)
	p.wdaWaits["p"] = s
	p.forgetExcept(nil)
	if _, ok := p.wdaWaits["p"]; ok {
		t.Fatal("the runner of a phone that is gone is still kept")
	}
}

package gateway

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// Tests for history screenshots taken in the background (shots.go). Tests
// call Flush between calls where an agent would think, so the screenshot
// is done before the next call reads the screen.

func shotGateway(t *testing.T) (*Gateway, *hookDev, string) {
	t.Helper()
	dir := t.TempDir()
	g, hd := newHookGateway(t, func(o *Options) { o.DataDir = dir; o.NoScreenshots = false })
	return g, hd, dir
}

// mediaFiles lists the screenshot files written to history.
func mediaFiles(dir string) []string {
	var out []string
	filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() && strings.HasSuffix(p, ".jpg") {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// toolSteps returns the steps of the task called title made by tool.
func toolSteps(t *testing.T, g *Gateway, title, tool string) []StepView {
	t.Helper()
	g.Flush()
	tasks, err := g.History(context.Background(), HistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var out []StepView
	for _, task := range tasks {
		if task.Title != title {
			continue
		}
		for _, st := range task.Steps {
			if st.Tool == tool {
				out = append(out, st)
			}
		}
	}
	return out
}

func TestShotsDoNotHoldTheResult(t *testing.T) {
	g, hd, _ := shotGateway(t)
	ctx := context.Background()
	s, first := connect(t, g, "Lunch")
	dinely := ref(t, first, "Dinely")
	g.Flush()
	hook, entered, release := blockOnce()
	hd.setShot(hook)
	done := make(chan *Result, 1)
	go func() { done <- tap(s, dinely) }()
	select {
	case res := <-done:
		mustStatus(t, res, StatusSettled)
	case <-time.After(5 * time.Second):
		release()
		t.Fatal("the result waited for the screenshot")
	}
	<-entered
	release()
	g.Flush()
	// The next call's read confirms the screenshot.
	mustStatus(t, s.Observe(ctx, ObserveRequest{}), StatusSettled)
	steps := toolSteps(t, g, "Lunch", "phone_tap")
	if len(steps) != 1 || steps[0].Before == "" || steps[0].After == "" || steps[0].Before == steps[0].After {
		t.Fatalf("steps: %+v", steps)
	}
}

func TestBeforeReusesTheAfterPicture(t *testing.T) {
	g, hd, dir := shotGateway(t)
	var shots atomic.Int32
	hd.setShot(func() { shots.Add(1) })
	s, first := connect(t, g, "Lunch")
	g.Flush()
	res := tap(s, ref(t, first, "Dinely"))
	mustStatus(t, res, StatusSettled)
	g.Flush()
	mustStatus(t, tap(s, ref(t, res, "Account")), StatusSettled)
	g.Flush()
	mustStatus(t, s.Observe(context.Background(), ObserveRequest{}), StatusSettled)
	steps := toolSteps(t, g, "Lunch", "phone_tap")
	if len(steps) != 2 {
		t.Fatalf("steps: %+v", steps)
	}
	if steps[0].Before == "" || steps[0].After == "" || steps[1].Before != steps[0].After || steps[1].After == "" {
		t.Fatalf("pictures: %+v", steps)
	}
	// One screenshot per screen (home, Dinely, Account) and none before an
	// action.
	if n := shots.Load(); n != 3 {
		t.Fatalf("%d screenshots, want 3", n)
	}
	if n := len(mediaFiles(dir)); n != 3 {
		t.Fatalf("%d files, want 3", n)
	}
}

func TestShotOfBlockedAppNeverStored(t *testing.T) {
	t.Run("action ends on it", func(t *testing.T) {
		g, hd, dir := shotGateway(t)
		var shots atomic.Int32
		hd.setShot(func() { shots.Add(1) })
		s, first := connect(t, g, "Lunch")
		g.Flush()
		hd.OnInput(func(in demo.Input) {
			if in.Kind == "tap" {
				hd.Show("wallet")
			}
		})
		res := tap(s, ref(t, first, "Dinely"))
		if res.Rule == "" {
			t.Fatalf("the wallet is not hidden: %+v", res)
		}
		g.Flush()
		s.Observe(context.Background(), ObserveRequest{})
		s.Release(context.Background(), ReleaseRequest{})
		if n := shots.Load(); n != 1 {
			t.Fatalf("%d screenshots, want only the home screen's", n)
		}
		steps := toolSteps(t, g, "Lunch", "phone_tap")
		if len(steps) != 1 || steps[0].After != "" {
			t.Fatalf("steps: %+v", steps)
		}
		if n := len(mediaFiles(dir)); n != 1 {
			t.Fatalf("%d files, want only the home screen's", n)
		}
	})
	t.Run("comes up while the screenshot is taken", func(t *testing.T) {
		// The read after the action shows Dinely; the wallet opens before
		// the screenshot. The read that confirms it shows the wallet.
		g, hd, dir := shotGateway(t)
		s, first := connect(t, g, "Lunch")
		g.Flush()
		var swapped atomic.Bool
		var once sync.Once
		hd.setShot(func() { once.Do(func() { hd.Show("wallet"); swapped.Store(true) }) })
		res := tap(s, ref(t, first, "Dinely"))
		mustStatus(t, res, StatusSettled)
		if res.Rule != "" || res.Screen.App.ID != demo.Dinely {
			t.Fatalf("the read after the tap: %+v", res.Screen.App)
		}
		g.Flush()
		if !swapped.Load() {
			t.Fatal("the screenshot did not run")
		}
		if obs := s.Observe(context.Background(), ObserveRequest{}); obs.Rule == "" {
			t.Fatalf("the wallet is not hidden: %+v", obs)
		}
		s.Release(context.Background(), ReleaseRequest{})
		steps := toolSteps(t, g, "Lunch", "phone_tap")
		if len(steps) != 1 || steps[0].After != "" {
			t.Fatalf("steps: %+v", steps)
		}
		// Only the home screen, the tap's before picture.
		if n := len(mediaFiles(dir)); n != 1 {
			t.Fatalf("%d files, want 1", n)
		}
	})
}

func TestShotDroppedByInput(t *testing.T) {
	t.Run("the next step", func(t *testing.T) {
		g, hd, dir := shotGateway(t)
		s, _ := connect(t, g, "Lunch")
		g.Flush()
		hook, entered, release := blockOnce()
		hd.setShot(hook)
		done := make(chan *Result, 1)
		go func() {
			done <- s.Batch(context.Background(), BatchRequest{Steps: []Action{
				{Tool: "launch", App: "Dinely"}, {Tool: "key", Key: "back"},
			}})
		}()
		<-entered // the launch's screenshot is being taken
		res := <-done
		mustStatus(t, res, StatusSettled)
		release()
		g.Flush()
		mustStatus(t, s.Observe(context.Background(), ObserveRequest{}), StatusSettled)
		launch := toolSteps(t, g, "Lunch", "phone_launch")
		key := toolSteps(t, g, "Lunch", "phone_key")
		if len(launch) != 1 || len(key) != 1 || launch[0].After != "" || key[0].Before != "" || key[0].After == "" {
			t.Fatalf("launch %+v key %+v", launch, key)
		}
		if n := len(mediaFiles(dir)); n != 1 {
			t.Fatalf("%d files, want 1", n)
		}
	})
	t.Run("the person", func(t *testing.T) {
		g, hd, dir := shotGateway(t)
		s, first := connect(t, g, "Lunch")
		g.Flush()
		hook, entered, release := blockOnce()
		hd.setShot(hook)
		done := make(chan *Result, 1)
		go func() { done <- tap(s, ref(t, first, "Dinely")) }()
		<-entered
		mustStatus(t, <-done, StatusSettled)
		g.TakeOver("pixel")
		release()
		g.Flush()
		g.HandBack("pixel", "")
		s.Observe(context.Background(), ObserveRequest{})
		s.Release(context.Background(), ReleaseRequest{})
		steps := toolSteps(t, g, "Lunch", "phone_tap")
		if len(steps) != 1 || steps[0].After != "" {
			t.Fatalf("steps: %+v", steps)
		}
		if n := len(mediaFiles(dir)); n != 1 {
			t.Fatalf("%d files, want 1", n)
		}
	})
}

func TestTaskEndConfirmsTheLastPicture(t *testing.T) {
	g, _, _ := shotGateway(t)
	s, first := connect(t, g, "Lunch")
	g.Flush()
	mustStatus(t, tap(s, ref(t, first, "Dinely")), StatusSettled)
	g.Flush()
	s.Release(context.Background(), ReleaseRequest{})
	steps := toolSteps(t, g, "Lunch", "phone_tap")
	if len(steps) != 1 || steps[0].After == "" {
		t.Fatalf("steps: %+v", steps)
	}

	t.Run("not after Stop", func(t *testing.T) {
		g, hd, _ := shotGateway(t)
		s, first := connect(t, g, "Lunch")
		g.Flush()
		mustStatus(t, tap(s, ref(t, first, "Dinely")), StatusSettled)
		g.Flush()
		var reads atomic.Int32
		hd.setObserve(func(*screen.Screen) { reads.Add(1) })
		if err := g.Stop("pixel"); err != nil {
			t.Fatal(err)
		}
		g.Flush()
		if n := reads.Load(); n != 0 {
			t.Fatalf("%d reads after Stop", n)
		}
		if steps := toolSteps(t, g, "Lunch", "phone_tap"); len(steps) != 1 || steps[0].After != "" {
			t.Fatalf("steps: %+v", steps)
		}
	})
}

// noLockDev hides the demo's lock reports, like a driver that knows the
// lock state only from reads.
type noLockDev struct{ driver.Device }

type noLockProvider struct {
	*demo.Provider
	d driver.Device
}

func (p noLockProvider) Open(context.Context, string) (driver.Device, error) { return p.d, nil }

func TestLaunchReadsOnlyWhenNeeded(t *testing.T) {
	ctx := context.Background()
	newGateway := func(t *testing.T, dev driver.Device, prov driver.Provider, pol *policy.Engine) *Gateway {
		t.Helper()
		g, err := New(Options{Providers: []driver.Provider{prov}, Policy: pol, TrustNewAgents: true,
			DataDir: t.TempDir(), DiscoverEvery: time.Hour, ApprovalTimeout: 3 * time.Second, MaxBlock: 2 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { g.Close() })
		return g
	}
	reads := func(cd *countingDevice) int {
		cd.mu.Lock()
		defer cd.mu.Unlock()
		n := cd.observes
		cd.observes = 0
		return n
	}

	cd := &countingDevice{Device: demo.NewDevice("pixel", "Pixel 9", driver.Android)}
	// A password manager is refused without a question (a finance app
	// asks whether to allow it for the task, with the screen it starts on).
	vault := policy.Default()
	vault.AddApps(policy.CatPasswords, policy.CategoryEntries{IDs: []string{"com.google.android.apps.walletnfcrel"}})
	g := newGateway(t, cd, countingProvider{demo.NewProvider(driver.Android, cd.Device), cd}, vault)
	s, _ := connect(t, g, "Lunch")
	g.Flush()
	reads(cd)
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Google Wallet"}), StatusBlocked)
	if n := reads(cd); n != 0 {
		t.Fatalf("a refused launch read %d times", n)
	}
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}), StatusSettled)
	if n := reads(cd); n != 1 {
		t.Fatalf("a launch the policy allows read %d times, want 1 (after)", n)
	}
	// The first launch's after picture waits for a read, which this one
	// makes before it acts.
	g.Flush()
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Chatter"}), StatusSettled)
	if n := reads(cd); n != 2 {
		t.Fatalf("read %d times, want 2", n)
	}
	if st := toolSteps(t, g, "Lunch", "phone_launch"); len(st) != 3 || st[1].After == "" {
		t.Fatalf("launch steps: %+v", st)
	}

	t.Run("policy asks", func(t *testing.T) {
		cd := &countingDevice{Device: demo.NewDevice("pixel", "Pixel 9", driver.Android)}
		pol := policy.Default()
		if err := pol.SetPreset(policy.PresetEvery); err != nil {
			t.Fatal(err)
		}
		g := newGateway(t, cd, countingProvider{demo.NewProvider(driver.Android, cd.Device), cd}, pol)
		s, _ := connect(t, g, "Lunch")
		reads(cd)
		go func() {
			n := waitNeed(t, g)
			if n.Screenshot == nil {
				t.Error("the approval has no screenshot")
			}
			g.Decide(n.ID, Deny, "")
		}()
		mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}), StatusDenied)
		if n := reads(cd); n != 1 {
			t.Fatalf("read %d times, want 1 (for the approval)", n)
		}
	})
	t.Run("lock state only from reads", func(t *testing.T) {
		dev := demo.NewDevice("pixel", "Pixel 9", driver.Android)
		cd := &countingDevice{Device: dev}
		g := newGateway(t, cd, noLockProvider{demo.NewProvider(driver.Android, dev), noLockDev{cd}}, nil)
		s, _ := connect(t, g, "Lunch")
		reads(cd)
		mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}), StatusSettled)
		if n := reads(cd); n < 2 {
			t.Fatalf("read %d times, want a read before the launch", n)
		}
	})
}

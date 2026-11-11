package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// A swap is staged in a work folder next to what it replaces (Pinky.app on
// macOS, the install folder on Windows), so that moving things in and out
// is a rename on one volume. The helper that swaps and the version that
// starts afterwards talk through files in it:
//
//	pending.json  the swap: the version it replaces and the one it puts in
//	old.app, old  the version it replaced, kept until the new one is up
//	launched      the new version's pid, once it holds the gateway's lock
//	started       the new version is up (its window, or the daemon)
//	rolled-back   the swap did not happen, with why; the old version is back
//	done          the helper is through, and needs nothing here (Windows)
//	kept          files of the person's that the helper could not put back
//	              into the install folder (Windows): never removed
//
// CheckSwap reads them at each start of Pinky.

// workPrefix names the work folders next to target: .Pinky.app.update-*,
// .Pinky.update-*.
func workPrefix(target string) string { return "." + filepath.Base(target) + ".update-" }

// pendingSwap is what Swap leaves in the work folder for the new version.
type pendingSwap struct {
	From string    `json:"from"`
	To   string    `json:"to"`
	At   time.Time `json:"at"`
}

func writePending(work, from, to string) error {
	data, _ := json.Marshal(pendingSwap{From: from, To: to, At: time.Now().UTC()})
	return os.WriteFile(filepath.Join(work, "pending.json"), data, 0o600)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// checkSwap is CheckSwap for the app (or install folder) at target, running
// version as process pid.
func checkSwap(target, version string, pid int, now time.Time) *Swapped {
	dirs, _ := filepath.Glob(filepath.Join(filepath.Dir(target), workPrefix(target)+"*"))
	var failed *Swapped
	live, liveAt := "", time.Time{}
	var liveP pendingSwap
	for _, dir := range dirs {
		data, err := os.ReadFile(filepath.Join(dir, "pending.json"))
		var p pendingSwap
		if err != nil || json.Unmarshal(data, &p) != nil {
			// Staged but never swapped (the person quit before, or the
			// helper gave up): remove it once it is a day old.
			if fi, err := os.Stat(dir); err == nil && now.Sub(fi.ModTime()) > 24*time.Hour {
				os.RemoveAll(dir)
			}
			continue
		}
		old := exists(filepath.Join(dir, "old.app")) || exists(filepath.Join(dir, "old"))
		switch rolledBack := exists(filepath.Join(dir, "rolled-back")); {
		case exists(filepath.Join(dir, "kept")):
			// The person's files, which the log names: theirs to remove.
		case rolledBack && Compare(p.To, version) != 0:
			// The old version again: the update did not happen.
			why, _ := os.ReadFile(filepath.Join(dir, "rolled-back"))
			failed = &Swapped{From: p.From, To: p.To, Failed: true, Why: strings.TrimSpace(string(why))}
			removeSoon(dir)
		case rolledBack:
			// This version was moved aside while it started, and may run
			// from there: the old version cleans up.
		case Compare(p.To, version) == 0 && old && !exists(filepath.Join(dir, "started")):
			// The swap that put this version in place: its helper waits.
			if live == "" || p.At.After(liveAt) {
				live, liveAt, liveP = dir, p.At, p
			}
		case exists(filepath.Join(dir, "done")) || now.Sub(p.At) > 24*time.Hour:
			// A helper that is through left its own copy behind (Windows),
			// or a swap of long ago.
			removeSoon(dir)
		}
	}
	if failed != nil {
		return failed
	}
	if live != "" && os.WriteFile(filepath.Join(live, "launched"), []byte(strconv.Itoa(pid)), 0o600) == nil {
		return &Swapped{From: liveP.From, To: liveP.To, dir: live}
	}
	return nil
}

// removeSoon removes a work folder, and keeps trying for half a minute in
// the background while something in it is in use: on Windows, the helper's
// copy of Pinky.exe, as the helper exits.
func removeSoon(dir string) {
	if os.RemoveAll(dir) == nil {
		return
	}
	go func() {
		for i := 0; i < 30 && os.RemoveAll(dir) != nil; i++ {
			time.Sleep(time.Second)
		}
	}()
}

// Confirm tells the helper that the new version is up, so it removes the
// old one.
func (s *Swapped) Confirm() error {
	if s == nil || s.Failed || s.dir == "" {
		return nil
	}
	if err := os.WriteFile(filepath.Join(s.dir, "started"), nil, 0o600); err != nil {
		return err
	}
	go cleanUpAfter(s.dir)
	return nil
}

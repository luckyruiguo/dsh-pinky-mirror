package policy

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"go.yaml.in/yaml/v3"
)

// Hand edits. Settings writes rules.yaml and apps.yaml, and the person may
// edit them too, also while Pinky runs (Settings › Advanced › Edit rules
// file). The engine remembers the version of each file it last read or
// wrote. Refresh, which the gateway runs every few seconds, and every
// change from Settings before it saves, read a file again when it changed
// on disk since then. An edit that parses takes effect, and the change
// from Settings is made on top of it. An edit that does not parse changes
// nothing: the rules in force stay, and changes from Settings to that file
// are refused until the person fixes it, so that writing the file never
// loses their edit. A file that is gone starts again from the bundled
// one, as at start.

// FileError is a rules file that changed on disk but cannot be used: it
// does not parse, or cannot be read. The engine keeps the rules it had,
// and refuses changes from Settings to the file, until it is fixed.
type FileError struct {
	Path string // rules.yaml or apps.yaml
	Err  error  // what is wrong, as the YAML parser or ParseFile says it
}

func (e *FileError) Error() string {
	return fmt.Sprintf("%s cannot be used until it is fixed: %v", filepath.Base(e.Path), e.Err)
}

func (e *FileError) Unwrap() error { return e.Err }

// FileChange is what the engine found in a rules file that changed on
// disk.
type FileChange struct {
	Path string
	// Err is why the file cannot be used. Nil means it is in force now, or
	// the problem it had is gone.
	Err error
}

// diskFile is what the engine knows of one of its files.
type diskFile struct {
	path string
	// seen is the version the engine last read or wrote, in force or not.
	// A new version has another size or time (an editor that writes in
	// place), or is another file (one that writes a new file and renames
	// it over the old one).
	seen fs.FileInfo
	// sum hashes the version in force: the one the engine last wrote, or
	// last read and put in force.
	sum [sha256.Size]byte
	// err is why the version on disk is not in force; nil when it is.
	err error
}

// look is what a look at a file found.
type look struct {
	data []byte
	info fs.FileInfo
	gone bool  // there is no file
	err  error // the file cannot be read
}

// settle waits between looks at a file that changed, which the engine
// reads once two looks in a row find the same version, or after
// settleTries waits.
var settle = func() { time.Sleep(50 * time.Millisecond) }

const settleTries = 20

// look reads the file when it is not the version seen last, or always with
// force; ok is false when there is nothing new.
func (d *diskFile) look(force bool) (l look, ok bool) {
	info, err := os.Stat(d.path)
	if !force {
		switch {
		case err == nil && sameVersion(d.seen, info):
			return look{}, false
		case err != nil && !errors.Is(err, fs.ErrNotExist) && d.err != nil && d.err.Error() == err.Error():
			return look{}, false // still cannot look at it
		}
	}
	for try := 0; ; try++ {
		// Wait until the file stays as it is, so as not to read an editor's
		// save half done (an empty rules.yaml has no rules), nor take a
		// file an editor is replacing for one that is gone. Load reads at
		// once.
		waiting := !force && try < settleTries
		if waiting {
			settle()
			again, err2 := os.Stat(d.path)
			if !sameLook(info, err, again, err2) {
				info, err = again, err2
				continue
			}
		}
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return look{gone: true}, true
		case err != nil:
			return look{err: err}, true
		}
		data, rerr := os.ReadFile(d.path)
		switch {
		case errors.Is(rerr, fs.ErrNotExist):
			return look{gone: true}, true
		case rerr != nil:
			return look{info: info, err: rerr}, true
		}
		if !waiting {
			return look{data: data, info: info}, true
		}
		after, err2 := os.Stat(d.path)
		if sameLook(info, nil, after, err2) {
			return look{data: data, info: info}, true
		}
		info, err = after, err2 // it changed while it was read
	}
}

// sameLook reports whether two looks at a file found the same: the same
// version, no file both times, or another failure both times.
func sameLook(a fs.FileInfo, aerr error, b fs.FileInfo, berr error) bool {
	if aerr != nil || berr != nil {
		return aerr != nil && berr != nil && errors.Is(aerr, fs.ErrNotExist) == errors.Is(berr, fs.ErrNotExist)
	}
	return sameVersion(a, b)
}

// sameVersion reports whether two looks at a file found the same version.
func sameVersion(a, b fs.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// Refresh takes in hand edits of rules.yaml and apps.yaml: a file that
// changed on disk since the engine last read or wrote it is read again,
// and takes effect when it parses. Runtime read-only switches and apps
// added with AddApps stay. It returns what it found, with what changes
// from Settings found before them, since the last Refresh.
func (e *Engine) Refresh() []FileChange {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.refreshLocked(false)
	out := e.changes
	e.changes = nil
	return out
}

// Version counts the changes of rules.yaml and apps.yaml since Load: hand
// edits the engine took in or found it cannot use, and what Settings
// wrote. Settings shows what the files hold again when it moves.
func (e *Engine) Version() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.version
}

// Problems lists the rules files that changed on disk but cannot be used,
// rules.yaml first.
func (e *Engine) Problems() []*FileError {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []*FileError
	for _, d := range []*diskFile{&e.rulesDisk, &e.appsDisk} {
		if d.err != nil {
			out = append(out, &FileError{Path: d.path, Err: d.err})
		}
	}
	return out
}

func (e *Engine) problemLocked() error {
	for _, d := range []*diskFile{&e.rulesDisk, &e.appsDisk} {
		if d.err != nil {
			return &FileError{Path: d.path, Err: d.err}
		}
	}
	return nil
}

// syncLocked takes in hand edits before a change from Settings to the file
// d, so that the change is made on top of them. It refuses the change
// while d cannot be used: Settings would change rules the file does not
// have, and writing it would lose the person's edit.
func (e *Engine) syncLocked(d *diskFile) error {
	e.refreshLocked(false)
	if d.err != nil {
		return &FileError{Path: d.path, Err: d.err}
	}
	return nil
}

func (e *Engine) refreshLocked(force bool) {
	e.refreshRulesLocked(force)
	e.refreshAppsLocked(force)
}

func (e *Engine) refreshRulesLocked(force bool) {
	d := &e.rulesDisk
	if d.path == "" {
		return
	}
	l, ok := d.look(force)
	if !ok || (l.gone && !e.recreateLocked(d, &l, defaultRulesYAML)) {
		return
	}
	// At start, a file an earlier version wrote and nobody changed takes
	// today's defaults (upgrade.go). Should writing fail, it stays as it is.
	if force && l.err == nil && !l.gone {
		if fresh, up := upgradeDefaults(l.data); up {
			if info, err := writeFile(d.path, fresh); err == nil {
				l = look{data: fresh, info: info}
			}
		}
	}
	e.settleLocked(d, l, force, func(data []byte) (func(), error) {
		f, err := ParseFile(data)
		if err != nil {
			return nil, err
		}
		migrateLegacy(&f)
		return func() { e.file = f }, nil
	})
}

func (e *Engine) refreshAppsLocked(force bool) {
	d := &e.appsDisk
	if d.path == "" {
		return
	}
	l, ok := d.look(force)
	if !ok || (l.gone && !e.recreateLocked(d, &l, appsTemplate)) {
		return
	}
	e.settleLocked(d, l, force, func(data []byte) (func(), error) {
		var o AppOverrides
		if err := yaml.Unmarshal(data, &o); err != nil {
			return nil, err
		}
		return func() { e.setOverridesLocked(o) }, nil
	})
}

// recreateLocked writes a file that is gone again with what Pinky ships,
// as at start, and makes l the look at it. It returns false when the file
// appeared meanwhile, for the next look.
func (e *Engine) recreateLocked(d *diskFile, l *look, data []byte) bool {
	info, err := writeNew(d.path, data)
	if info == nil && err == nil {
		return false
	}
	*l = look{data: data, info: info, err: err}
	return true
}

// settleLocked records a look at a file, and puts the version it read in
// force when parse takes it. parse returns what puts it in force.
func (e *Engine) settleLocked(d *diskFile, l look, force bool, parse func([]byte) (func(), error)) {
	if l.info != nil {
		d.seen = l.info
	}
	err := l.err
	if err == nil {
		sum := sha256.Sum256(l.data)
		if sum == d.sum && !force {
			// The version in force, touched or put back.
			if d.err != nil {
				d.err = nil
				e.noteLocked(FileChange{Path: d.path})
			}
			return
		}
		var apply func()
		if apply, err = parse(l.data); err == nil {
			apply()
			d.sum, d.err = sum, nil
			e.noteLocked(FileChange{Path: d.path})
			return
		}
	}
	if d.err == nil || d.err.Error() != err.Error() {
		e.noteLocked(FileChange{Path: d.path, Err: err})
	}
	d.err = err
}

// noteLocked keeps a change for Refresh to report.
func (e *Engine) noteLocked(c FileChange) {
	e.version++
	const keep = 16
	if len(e.changes) >= keep {
		e.changes = append(e.changes[:0], e.changes[len(e.changes)-keep+1:]...)
	}
	e.changes = append(e.changes, c)
}

// writeLocked writes one of the engine's files, which is then the version
// in force. A file that cannot be used is never written over.
func (e *Engine) writeLocked(d *diskFile, data []byte) error {
	if d.err != nil {
		return &FileError{Path: d.path, Err: d.err}
	}
	info, err := writeFile(d.path, data)
	if err != nil {
		return err
	}
	d.seen, d.sum = info, sha256.Sum256(data)
	e.version++
	return nil
}

// writeFile replaces a file atomically, readable only by the user, and
// returns how the file system sees what it wrote.
func writeFile(path string, data []byte) (fs.FileInfo, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return nil, err
	}
	fail := func(err error) (fs.FileInfo, error) {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail(err)
	}
	// Renaming keeps the file and its time, so this is how a later look
	// sees it, unless someone changed it since.
	info, err := tmp.Stat()
	if err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return nil, err
	}
	if err := replace(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return nil, err
	}
	return info, nil
}

// A save renames its new file over the old one. Windows refuses that rename
// ("Access is denied") while anything holds the old file open, whatever it
// shares: a virus scanner or a backup that reads rules.yaml for a moment,
// or the person's editor. replace then tries again, renameWait apart, up to
// renameTries times, as the person would after Settings said it could not
// save.
const (
	renameWait  = 25 * time.Millisecond
	renameTries = 20
)

// replace renames from over to, trying again while to is held open
// (renameBusy).
func replace(from, to string) error {
	err := os.Rename(from, to)
	for try := 1; err != nil && renameBusy(err) && try < renameTries; try++ {
		time.Sleep(renameWait)
		err = os.Rename(from, to)
	}
	return err
}

// writeNew creates a file that does not exist yet, readable only by the
// user, and returns how the file system sees it. It returns nil and no
// error when the file exists.
func writeNew(path string, data []byte) (fs.FileInfo, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return nil, err
	}
	info, err := f.Stat()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	return info, nil
}

package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/approval"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// phone_files (round three, decision 7): the phone's Pinky folder
// (driver.Files, /sdcard/Pinky on Android) and a folder on the computer,
// <data>/files/<phone id>/, copied either way on request. Agents use their
// own file tools on the local folder. An agent that cannot reach that
// folder (one on another computer, over HTTP) uploads a file into it
// instead. Read-only and watch-only agents may only list and pull; copying
// to the phone asks the person (rule "Files"), and read-only mode refuses
// it and the uploads made for it.

// FilesRequest is phone_files.
type FilesRequest struct {
	Call
	Action string   // list (default), pull, push or upload
	Paths  []string // files or folders, relative to the Pinky folder; empty means all. upload: the one file's name

	// For upload: the file's contents, at most MaxUpload bytes, and whether
	// they may replace a different file of that name. Check only checks
	// that the upload could go ahead and writes nothing, before an upload
	// link is handed out.
	Body    io.Reader
	Replace bool
	Check   bool
}

// MaxUpload bounds a file an agent uploads.
const MaxUpload = 200 * 1000 * 1000

// FileView is a file of a phone_files result.
type FileView struct {
	Path    string // slash-separated, relative to the folders
	Size    int64
	ModTime time.Time
	// Where says which side has it: "both" (the same size and time),
	// "phone", "computer", or "differs".
	Where string
}

// Where values.
const (
	FileBoth     = "both"
	FilePhone    = "phone"
	FileComputer = "computer"
	FileDiffers  = "differs"
)

// filesRule is the rule copying to the phone asks under.
const filesRule = "Files"

// maxFilesShown bounds the file names in step text and approvals.
const maxFilesShown = 5

// FilesDir returns the folder on the computer that a phone's Pinky folder
// is copied to, or "" without a data folder.
func (g *Gateway) FilesDir(deviceID string) string {
	if g.opt.DataDir == "" {
		return ""
	}
	return filepath.Join(g.opt.DataDir, "files", safeName(deviceID))
}

// safeName turns a device id ("192.168.1.5:5555") into a folder name.
func safeName(id string) string {
	b := []byte(id)
	for i, c := range b {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
		if !ok {
			b[i] = '_'
		}
	}
	if s := string(b); s != "" && s != "." && s != ".." {
		return s
	}
	return "phone"
}

// Files implements phone_files.
func (s *Session) Files(ctx context.Context, req FilesRequest) *Result {
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" {
		action = "list"
	}
	if action != "list" && action != "pull" && action != "push" && action != "upload" {
		return errResult(ErrInvalidArgument, "action must be list, pull, push or upload.")
	}
	var paths []string
	switch {
	case action == "upload" && len(req.Paths) != 1:
		return errResult(ErrInvalidArgument, `upload takes one file: pass its name in paths, such as ["photo.jpg"].`)
	case action == "upload":
		name, why := uploadName(req.Paths[0])
		if why != "" {
			return errResult(ErrInvalidArgument, "%q cannot be a file name in the Pinky folder: %s.", req.Paths[0], why)
		}
		paths = []string{name}
		req.Paths = nil
	}
	for _, p := range req.Paths {
		clean, ok := cleanRel(p)
		if !ok {
			return errResult(ErrInvalidArgument, "%q is not a path inside the Pinky folder: use a relative path without ..", p)
		}
		if clean != "" {
			paths = append(paths, clean)
		}
	}
	l, res := s.acquire(req.Device)
	if res != nil {
		return res
	}
	defer s.done(l)
	g := s.g
	ctx, cancel := g.callCtx(ctx, l)
	defer cancel()
	if res := g.gate(l, req.ApprovalID, action == "push"); res != nil {
		return g.finish(l, res)
	}
	d := l.dev
	dev := g.driverOf(d)
	if dev == nil {
		return g.finish(l, errResult(ErrDeviceOffline, "%s is offline.", d.name()))
	}
	// A phone without a Pinky folder (an iPhone) still has its folder on
	// the computer, which phone_app installs from: list and upload work.
	fsys, ok := dev.(driver.Files)
	if !ok && action != "list" && action != "upload" {
		return g.finish(l, errResult(ErrUnsupported, "%s has no Pinky folder that Pinky can reach, so nothing is copied to or from it; list and upload work on its folder on the computer.", d.name()))
	}
	dir := g.FilesDir(d.id)
	if dir == "" {
		return g.finish(l, errResult(ErrUnsupported, "This Pinky keeps no data folder, so it has no local folder for the phone's files."))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return g.finish(l, errResult(ErrInternal, "The local folder %s cannot be made: %v", dir, err))
	}
	args := argsJSON(map[string]any{"action": action, "paths": paths})
	start := time.Now()
	switch action {
	case "push":
		res = g.pushFiles(ctx, l, req, fsys, dir, paths, args, start)
	case "upload":
		res = g.upload(ctx, l, req, dir, paths[0], args, fsys != nil, start)
	default:
		res = g.listOrPull(ctx, l, action, fsys, dir, paths, args, start)
	}
	if res.Status != StatusPending {
		res.Folder = dir
	}
	return g.finish(l, res)
}

// cleanRel cleans a path inside the Pinky folder; ok is false for one
// that leaves it.
func cleanRel(p string) (string, bool) {
	p = strings.TrimSpace(strings.ReplaceAll(p, `\`, "/"))
	p = strings.TrimPrefix(p, "Pinky/")
	if strings.HasPrefix(p, "/") {
		return "", false
	}
	c := path.Clean("/" + p)[1:]
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return "", false
		}
	}
	return c, true
}

func (g *Gateway) listOrPull(ctx context.Context, l *lease, action string, fsys driver.Files, dir string, paths []string, args string, start time.Time) *Result {
	if fsys == nil {
		return g.listLocalOnly(l, dir, args, start)
	}
	phone, err := fsys.ListFiles(ctx)
	if err != nil {
		res := g.filesError(l, err)
		g.recordFiles(l, args, "Failed: list the phone's Pinky folder", res, start)
		return res
	}
	local, err := listLocal(dir)
	if err != nil {
		return errResult(ErrInternal, "Reading the local folder failed: %v", err)
	}
	if action == "list" {
		files := mergeFiles(phone, local)
		res := &Result{Status: StatusSettled, Files: files, Folder: dir,
			Message: fmt.Sprintf("%s on the phone, %s on the computer.", count(len(phone)-dirs(phone), "file"), count(len(local), "file"))}
		g.recordFiles(l, args, fmt.Sprintf("Listed the phone's Pinky folder (%s)", count(len(phone)-dirs(phone), "file")), res, start)
		return res
	}
	want, missing := pick(phone, paths)
	if len(missing) > 0 {
		res := errResult(ErrNotFound, "Not in the phone's Pinky folder: %s. Nothing was copied.", strings.Join(missing, ", "))
		g.recordFiles(l, args, "Failed: copy from the phone", res, start)
		return res
	}
	var copied []FileView
	for _, f := range want {
		if lf, ok := local[f.Path]; ok && sameFile(lf, f) {
			continue
		}
		if err := pullOne(ctx, fsys, dir, f); err != nil {
			res := g.filesError(l, err)
			res.Message = fmt.Sprintf("Copying %s from the phone failed after %s: %s", f.Path, count(len(copied), "file"), res.Message)
			g.recordFiles(l, args, "Failed: copy from the phone", res, start)
			return res
		}
		copied = append(copied, FileView{Path: f.Path, Size: f.Size, ModTime: f.ModTime, Where: FileBoth})
	}
	res := &Result{Status: StatusSettled, Files: copied, Folder: dir}
	if len(copied) == 0 {
		res.Message = "The local folder already has these files; nothing was copied."
	} else {
		res.Message = fmt.Sprintf("Copied %s from the phone.", count(len(copied), "file"))
	}
	g.recordFiles(l, args, fmt.Sprintf("Copied %s from the phone%s", count(len(copied), "file"), names(copied)), res, start)
	return res
}

// listLocalOnly lists the folder on the computer of a phone without a
// Pinky folder.
func (g *Gateway) listLocalOnly(l *lease, dir string, args string, start time.Time) *Result {
	local, err := listLocal(dir)
	if err != nil {
		return errResult(ErrInternal, "Reading the local folder failed: %v", err)
	}
	files := sortedLocal(local)
	for i := range files {
		files[i].Where = FileComputer
	}
	res := &Result{Status: StatusSettled, Files: files, Folder: dir,
		Message: fmt.Sprintf("%s on the computer. %s has no Pinky folder; install apps from this folder with phone_app.", count(len(files), "file"), l.dev.name())}
	g.recordFiles(l, args, fmt.Sprintf("Listed the phone's folder on the computer (%s)", count(len(files), "file")), res, start)
	return res
}

func (g *Gateway) pushFiles(ctx context.Context, l *lease, req FilesRequest, fsys driver.Files, dir string, paths []string, args string, start time.Time) *Result {
	d := l.dev
	switch g.scopeOf(l.session).Mode {
	case ScopeReadOnly, ScopeWatchOnly:
		return errResult(ErrScopeDenied, "This agent may only list and copy from the phone: the person gave it %s access.", strings.ReplaceAll(string(g.scopeOf(l.session).Mode), "_", "-"))
	}
	if g.policy.ReadOnly(d.id) {
		res := &Result{Status: StatusBlocked, Rule: "Read-only", Message: "Read-only mode is on, so nothing is copied to the phone."}
		g.recordFiles(l, args, "Blocked by “Read-only”: copy to the phone", res, start)
		return res
	}
	phone, err := fsys.ListFiles(ctx)
	if err != nil {
		return g.filesError(l, err)
	}
	local, err := listLocal(dir)
	if err != nil {
		return errResult(ErrInternal, "Reading the local folder failed: %v", err)
	}
	onPhone := map[string]driver.FileInfo{}
	for _, f := range phone {
		onPhone[f.Path] = f
	}
	var want []FileView
	var missing []string
	if len(paths) == 0 {
		for _, f := range sortedLocal(local) {
			if pf, ok := onPhone[f.Path]; !ok || !sameFile(f, pf) {
				want = append(want, f)
			}
		}
	} else {
		seen := map[string]bool{}
		for _, p := range paths {
			found := false
			for _, f := range sortedLocal(local) {
				if (f.Path == p || strings.HasPrefix(f.Path, p+"/")) && !seen[f.Path] {
					seen[f.Path] = true
					want = append(want, f)
					found = true
				} else if f.Path == p || strings.HasPrefix(f.Path, p+"/") {
					found = true
				}
			}
			if !found {
				missing = append(missing, p)
			}
		}
	}
	if len(missing) > 0 {
		res := errResult(ErrNotFound, "Not in the local folder: %s. Nothing was copied.", strings.Join(missing, ", "))
		g.recordFiles(l, args, "Failed: copy to the phone", res, start)
		return res
	}
	if len(want) == 0 {
		return &Result{Status: StatusSettled, Folder: dir, Message: "The phone already has these files; nothing was copied."}
	}
	if res := g.askToPush(ctx, l, req, want, args); res != nil {
		return res
	}
	var copied []FileView
	for _, f := range want {
		if err := pushOne(ctx, fsys, dir, f); err != nil {
			res := g.filesError(l, err)
			res.Message = fmt.Sprintf("Copying %s to the phone failed after %s: %s", f.Path, count(len(copied), "file"), res.Message)
			g.recordFiles(l, args, "Failed: copy to the phone", res, start)
			return res
		}
		f.Where = FileBoth
		copied = append(copied, f)
	}
	res := &Result{Status: StatusSettled, Files: copied, Folder: dir,
		Message: fmt.Sprintf("Copied %s to the phone's Pinky folder.", count(len(copied), "file"))}
	g.recordFiles(l, args, fmt.Sprintf("Copied %s to the phone%s", count(len(copied), "file"), names(copied)), res, start)
	return res
}

// askToPush asks the person before files go to the phone, unless they
// allowed it for the task. It returns nil once allowed.
func (g *Gateway) askToPush(ctx context.Context, l *lease, req FilesRequest, want []FileView, args string) *Result {
	g.mu.Lock()
	granted := l.filesGranted
	g.mu.Unlock()
	if granted {
		return nil
	}
	s := l.session
	var key strings.Builder
	for _, f := range want {
		fmt.Fprintf(&key, "%s\x00%d\x00", f.Path, f.Size)
	}
	b := approval.Binding{Session: s.id, Device: l.dev.id, Lease: l.id, Tool: "phone_files", Args: digest(key.String())}
	id := req.ApprovalID
	if id != "" {
		if res := g.checkApproval(id, b); res != nil {
			if res.Error != "" {
				return res
			}
			id = ""
		}
	}
	var info approval.Info
	if id == "" {
		title := fmt.Sprintf("Copy %s to %s?", count(len(want), "file"), l.dev.name())
		if len(want) == 1 {
			title = fmt.Sprintf("Copy “%s” to %s?", want[0].Path, l.dev.name())
		}
		detail := s.agent.Name + " · into the phone's Pinky folder" + names(want)
		if c := strings.TrimSpace(req.Intent); c != "" {
			detail = s.agent.Name + " · " + c
		}
		info = g.approvals.Open(approval.Request{Kind: approval.KindAction, Binding: b, Rule: filesRule, Title: title,
			Detail: detail, Agent: s.agent.Name, TaskID: l.task.id, DeviceName: l.dev.name()})
		if res := g.afterOpen(l, info); res != nil {
			return res
		}
		g.openStep(l, info, "phone_files", args, "Waiting for you: "+strings.TrimSuffix(title, "?"), screen.Rect{}, screen.Point{}, "")
	} else {
		info, _ = g.approvals.Get(id)
	}
	out, err := g.waitApproval(ctx, l, info.ID, info, req.Progress)
	if errors.Is(err, errTaken) {
		return takenResult(info.ID)
	}
	if err != nil {
		return g.cancelled(l, info.ID)
	}
	switch {
	case out.State == approval.Open:
		return g.pendingResult(l, info)
	case out.State == approval.Withdrawn:
		g.closeStep(info.ID, string(StatusDenied), 0)
		return g.withdrawnResult(l, out)
	}
	g.recordDecision(info, out)
	if !out.Decision.Allows() {
		g.closeStep(info.ID, string(StatusDenied), out.Waited)
		return &Result{Status: StatusDenied, Rule: filesRule, Note: denyNote(out), Message: "The person did not allow copying these files to the phone."}
	}
	g.closeStep(info.ID, "allowed", out.Waited)
	if out.Decision != approval.AllowOnce {
		g.mu.Lock()
		l.filesGranted = true
		g.mu.Unlock()
	}
	return nil
}

func (g *Gateway) filesError(l *lease, err error) *Result {
	switch {
	case errors.Is(err, driver.ErrNotFound):
		return errResult(ErrNotFound, "%v", err)
	case errors.Is(err, driver.ErrUnsupported):
		return errResult(ErrUnsupported, "%v", err)
	case errors.Is(err, driver.ErrOffline):
		g.setOffline(l.dev, "")
		return errResult(ErrDeviceOffline, "%s is offline.", l.dev.name())
	case errors.Is(err, context.Canceled):
		return errResult(ErrInternal, "The call was cancelled.")
	case errors.Is(err, context.DeadlineExceeded):
		return errResult(ErrInternal, "The phone did not answer in time (%v). Check the phone before trying again.", err)
	}
	return errResult(ErrInternal, "%v", err)
}

// recordFiles adds a phone_files step to the task.
func (g *Gateway) recordFiles(l *lease, args, text string, res *Result, start time.Time) {
	status := string(res.Status)
	if res.Error != "" {
		status = string(res.Error)
	}
	g.addStep(l, StepView{Tool: "phone_files", Args: args, Text: text, Status: status, At: start, Elapsed: time.Since(start)})
}

// listLocal lists the regular files under dir by slash path. Symbolic
// links and other special files are left out: only what is really in the
// folder goes to the phone.
func listLocal(dir string) (map[string]FileView, error) {
	out := map[string]FileView{}
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() || !e.Type().IsRegular() {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(path.Base(rel), ".pinky-") {
			return nil // a pull in progress
		}
		out[rel] = FileView{Path: rel, Size: info.Size(), ModTime: info.ModTime(), Where: FileComputer}
		return nil
	})
	return out, err
}

func sortedLocal(m map[string]FileView) []FileView {
	out := make([]FileView, 0, len(m))
	for _, f := range m {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// sameFile compares sizes and times, to the second (adb keeps seconds).
func sameFile(a FileView, b driver.FileInfo) bool {
	d := a.ModTime.Sub(b.ModTime)
	return a.Size == b.Size && d < 2*time.Second && d > -2*time.Second
}

// mergeFiles lists both sides by path.
func mergeFiles(phone []driver.FileInfo, local map[string]FileView) []FileView {
	var out []FileView
	seen := map[string]bool{}
	for _, f := range phone {
		if f.Dir {
			continue
		}
		seen[f.Path] = true
		v := FileView{Path: f.Path, Size: f.Size, ModTime: f.ModTime, Where: FilePhone}
		if lf, ok := local[f.Path]; ok {
			v.Where = FileDiffers
			if sameFile(lf, f) {
				v.Where = FileBoth
			}
		}
		out = append(out, v)
	}
	for _, f := range sortedLocal(local) {
		if !seen[f.Path] {
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// pick returns the phone's files that paths name, or all of them.
func pick(phone []driver.FileInfo, paths []string) (want []driver.FileInfo, missing []string) {
	files := make([]driver.FileInfo, 0, len(phone))
	for _, f := range phone {
		if !f.Dir {
			files = append(files, f)
		}
	}
	if len(paths) == 0 {
		return files, nil
	}
	seen := map[string]bool{}
	for _, p := range paths {
		found := false
		for _, f := range files {
			if f.Path == p || strings.HasPrefix(f.Path, p+"/") {
				found = true
				if !seen[f.Path] {
					seen[f.Path] = true
					want = append(want, f)
				}
			}
		}
		if !found {
			for _, f := range phone {
				found = found || (f.Dir && f.Path == p)
			}
		}
		if !found {
			missing = append(missing, p)
		}
	}
	return want, missing
}

// localPath returns where a file of the Pinky folder goes under dir. No
// folder on the way may be a link (which could lead out of dir); the
// missing ones are made.
func localPath(dir, rel string) (string, error) {
	clean, ok := cleanRel(rel)
	if !ok || clean == "" {
		return "", fmt.Errorf("%q: %w", rel, driver.ErrNotFound)
	}
	parts := strings.Split(clean, "/")
	cur := dir
	for _, part := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := os.Mkdir(cur, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				return "", err
			}
		case err != nil:
			return "", err
		case fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir():
			return "", fmt.Errorf("%q: %s is not a folder of the local folder", rel, part)
		}
	}
	return filepath.Join(cur, parts[len(parts)-1]), nil
}

// pullOne copies a file from the phone through a temporary file, so a
// failed copy leaves nothing half written and a link in the way is
// replaced, not followed.
func pullOne(ctx context.Context, fsys driver.Files, dir string, f driver.FileInfo) error {
	dst, err := localPath(dir, f.Path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".pinky-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := fsys.Pull(ctx, f.Path, tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if !f.ModTime.IsZero() {
		_ = os.Chtimes(tmp.Name(), f.ModTime, f.ModTime)
	}
	return os.Rename(tmp.Name(), dst)
}

// pushOne copies a local file to the phone; a link is not followed.
func pushOne(ctx context.Context, fsys driver.Files, dir string, f FileView) error {
	src, err := localPath(dir, f.Path)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", f.Path)
	}
	r, err := os.Open(src)
	if err != nil {
		return err
	}
	defer r.Close()
	return fsys.Push(ctx, f.Path, io.LimitReader(r, fi.Size()), fi.Size(), fi.ModTime())
}

func dirs(fs []driver.FileInfo) int {
	n := 0
	for _, f := range fs {
		if f.Dir {
			n++
		}
	}
	return n
}

func count(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

// names lists the first few files for step text: ": a.txt, b.png and 3 more".
func names(fs []FileView) string {
	if len(fs) == 0 {
		return ""
	}
	var list []string
	for i, f := range fs {
		if i == maxFilesShown {
			list = append(list, fmt.Sprintf("and %d more", len(fs)-i))
			break
		}
		list = append(list, f.Path)
	}
	return ": " + strings.Join(list, ", ")
}

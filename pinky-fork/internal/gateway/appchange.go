package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/apppkg"
	"github.com/DIYgod/pinky/internal/approval"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// phone_app (docs/notes/apps.md): install or update an app from files in
// the phone's folder on the computer, uninstall an app, or stop one. An
// install or uninstall asks the person; Allow for this task covers the
// same app (and, for an install, the same signer) for the rest of the
// task. An install also offers Always allow, kept for the agent on the
// phone and for the app id and signer (standingAsk), so that a developer's
// next builds go in without asking; an uninstall deletes data and asks
// every time. Pinky's own apps, system apps, finance apps, password
// managers and blocked apps are refused.

// AppRequest is phone_app.
type AppRequest struct {
	Call
	Action string   // install, uninstall or stop
	Paths  []string // install: the files, relative to the phone's folder on the computer
	App    string   // uninstall and stop: an installed app's name or id
}

// appAsk is the Data of a phone_app approval: Allow for this task lasts
// (canAllowTask), and Always allow is offered when standing is set.
type appAsk struct {
	key      string       // the lease's appGrants key the approval grants
	standing *standingAsk // an install's Always allow; nil when none may be kept
}

// appTarget is what a phone_app call acts on.
type appTarget struct {
	op    policy.Op
	app   screen.App     // as the rules judge it: a clone's original, the package's app
	name  string         // the app's name for the person, a clone's own
	id    string         // the id the driver acts on: a clone's own, so the clone alone goes
	pkg   driver.Package // install
	files []string       // install: absolute paths
	from  string         // install: the files, for the history ("“app-debug.apk”")
}

// update reports whether an install replaces an installed app.
func (t appTarget) update() bool { return t.op == policy.OpInstall && t.pkg.Installed != "" }

// App implements phone_app.
func (s *Session) App(ctx context.Context, req AppRequest) *Result {
	action := strings.ToLower(strings.TrimSpace(req.Action))
	var op policy.Op
	switch action {
	case "install":
		op = policy.OpInstall
		if len(req.Paths) == 0 {
			return errResult(ErrInvalidArgument, `install takes paths: the .apk, the split APKs of one app, the .ipa or the .app folder, relative to the phone's folder on the computer (phone_files list shows it), such as ["app-debug.apk"].`)
		}
	case "uninstall", "stop":
		op = policy.Op(action)
		if strings.TrimSpace(req.App) == "" {
			return errResult(ErrInvalidArgument, "%s takes app: an installed app's name or id, as phone_apps lists it.", action)
		}
	default:
		return errResult(ErrInvalidArgument, "action must be install, uninstall or stop.")
	}
	var rels []string
	for _, p := range req.Paths {
		clean, ok := cleanRel(p)
		if !ok || clean == "" {
			return errResult(ErrInvalidArgument, "%q is not a path inside the phone's folder on the computer: use a relative path without ..", p)
		}
		if !apppkg.IsAPK(clean) && !apppkg.IsIOSApp(clean) {
			return errResult(ErrInvalidArgument, "%q is not an app: pass an .apk (or the split APKs of one app), an .ipa or a .app folder.", p)
		}
		rels = append(rels, clean)
	}
	if op != policy.OpInstall && len(rels) > 0 {
		return errResult(ErrInvalidArgument, "%s takes app, not paths.", action)
	}

	l, res := s.acquire(req.Device)
	if res != nil {
		return res
	}
	defer s.done(l)
	g := s.g
	ctx, cancel := g.callCtx(ctx, l)
	defer cancel()
	if res := g.gate(l, req.ApprovalID, true); res != nil {
		return g.finish(l, res)
	}
	d := l.dev
	dev := g.driverOf(d)
	if dev == nil {
		return g.finish(l, errResult(ErrDeviceOffline, "%s is offline.", d.name()))
	}
	am, ok := dev.(driver.AppManager)
	if !ok {
		return g.finish(l, errResult(ErrUnsupported, "Pinky cannot install, uninstall or stop apps on %s.", d.name()))
	}
	args := argsJSON(map[string]any{"action": action, "paths": rels, "app": strings.TrimSpace(req.App)})
	start := time.Now()

	t, res := g.appTarget(ctx, l, am, op, rels, req.App)
	if res != nil {
		text := appFailed(op, rels, req.App)
		if res.Status == StatusBlocked {
			text = appRefused(t, res)
		}
		g.recordApp(l, args, text, res, start)
		return g.finish(l, res)
	}
	g.mu.Lock()
	taskApps := append([]string(nil), l.apps...)
	readOnly := l.readOnly
	g.mu.Unlock()
	scope := g.scopeOf(s)
	in := policy.Input{Op: op, Device: d.id, Launch: t.app, TaskApps: taskApps,
		ReadOnly: scope.Mode == ScopeReadOnly || readOnly}
	if scope.Mode == ScopeApps {
		in.ScopeApps = scope.Apps
	}
	v := g.policy.Evaluate(in)
	switch v.Decision {
	case policy.Deny:
		res := refusal(v)
		g.recordApp(l, args, appRefused(t, res), res, start)
		return g.finish(l, res)
	case policy.Ask:
		if res := g.askApp(ctx, l, req, t, v, args); res != nil {
			return g.finish(l, res)
		}
	}
	return g.finish(l, g.runApp(ctx, l, am, t, args, start))
}

// appTarget resolves what a call acts on: the package in the files for an
// install, the installed app for an uninstall or stop.
func (g *Gateway) appTarget(ctx context.Context, l *lease, am driver.AppManager, op policy.Op, rels []string, name string) (appTarget, *Result) {
	d := l.dev
	t := appTarget{op: op}
	if op != policy.OpInstall {
		if policy.IsLink(name) {
			return t, errResult(ErrInvalidArgument, "%q is a link, not an app. Pass an installed app's name or id.", name)
		}
		app, res := g.resolveLaunch(ctx, d, name)
		if res != nil {
			res.Message = strings.ReplaceAll(res.Message, "Call phone_launch again", "Call phone_app again")
			return t, res
		}
		orig := app.Original()
		t.app, t.id = screen.App{ID: orig.ID, Name: orig.Name}, app.ID
		t.name = appLabel(screen.App{ID: app.ID, Name: app.Name})
		if app.System {
			return t, systemApp(op, t.name)
		}
		return t, nil
	}
	dir := g.FilesDir(d.id)
	if dir == "" {
		return t, errResult(ErrUnsupported, "This Pinky keeps no data folder, so it has no folder on the computer to install from.")
	}
	seen := map[string]bool{}
	for _, rel := range rels {
		if seen[rel] {
			return t, errResult(ErrInvalidArgument, "%q is listed twice.", rel)
		}
		seen[rel] = true
		file, err := appFile(dir, rel)
		if err != nil {
			return t, errResult(ErrNotFound, "%v Nothing was installed. Put the file in %s first (phone_files upload sends one from another computer).", err, dir)
		}
		t.files = append(t.files, file)
	}
	t.from = fromText(rels)
	pkg, err := am.InspectPackage(ctx, t.files)
	if err != nil {
		return t, g.appError(l, err, "Reading the app failed")
	}
	t.pkg = pkg
	t.app, t.id = screen.App{ID: pkg.ID, Name: pkg.Name}, pkg.ID
	t.name = appLabel(t.app)
	// An install over an app the phone ships with is refused like an
	// uninstall of it: the phone lists it as a system app.
	if apps, err := g.driverOf(d).Apps(ctx); err == nil {
		for _, a := range apps {
			if a.ID == pkg.ID && a.CloneOf == nil && a.System {
				return t, systemApp(op, t.name)
			}
		}
	}
	return t, nil
}

func systemApp(op policy.Op, name string) *Result {
	verb := map[policy.Op]string{policy.OpInstall: "install over", policy.OpUninstall: "uninstall", policy.OpStop: "stop"}[op]
	return refusal(policy.Verdict{Decision: policy.Deny, Rule: policy.RuleSystemApps,
		Reason: fmt.Sprintf("Agents cannot %s %s: it is part of the phone's system.", verb, name)})
}

// appFile resolves a path in the phone's folder on the computer without
// following symbolic links on the way: what is installed is what is in the
// folder. It must be a file, or a .app folder.
func appFile(dir, rel string) (string, error) {
	cur := dir
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("“%s” is not in the phone's folder on the computer.", rel)
		}
		if err != nil {
			return "", fmt.Errorf("“%s” cannot be read: %v.", rel, err)
		}
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			return "", fmt.Errorf("“%s” is a symbolic link, which Pinky does not follow.", strings.Join(parts[:i+1], "/"))
		case i < len(parts)-1 && !fi.IsDir():
			return "", fmt.Errorf("“%s” is not in the phone's folder on the computer.", rel)
		case i == len(parts)-1 && fi.IsDir() && !strings.EqualFold(filepath.Ext(part), ".app"):
			return "", fmt.Errorf("“%s” is a folder; only a .app folder can be installed.", rel)
		case i == len(parts)-1 && !fi.IsDir() && !fi.Mode().IsRegular():
			return "", fmt.Errorf("“%s” is not a regular file.", rel)
		}
	}
	return cur, nil
}

// fromText names an install's files: “app-debug.apk”, or “base.apk” and 2
// more files.
func fromText(rels []string) string {
	if len(rels) == 1 {
		return "“" + rels[0] + "”"
	}
	return fmt.Sprintf("“%s” and %s", rels[0], count(len(rels)-1, "more file"))
}

// appGrantKey identifies what Allow for this task covers: the same op on
// the same app, and for an install the same signer.
func appGrantKey(t appTarget) string {
	key := string(t.op) + "\x00" + t.app.ID
	if t.op == policy.OpInstall {
		key += "\x00" + t.pkg.SignerKey
	}
	return key
}

// appTitle is the approval's title.
func appTitle(t appTarget, device string) string {
	switch t.op {
	case policy.OpInstall:
		ver := ""
		if t.pkg.Version != "" {
			ver = " " + t.pkg.Version
		}
		if t.update() {
			if ver != "" {
				ver = " to" + ver
			}
			return fmt.Sprintf("Update “%s”%s on %s?", t.name, ver, device)
		}
		return fmt.Sprintf("Install “%s”%s on %s?", t.name, ver, device)
	case policy.OpUninstall:
		return fmt.Sprintf("Uninstall “%s” from %s?", t.name, device)
	}
	return fmt.Sprintf("Stop “%s” on %s?", t.name, device)
}

// appDetail is the approval's detail: the facts the person judges by.
func appDetail(agent, intent string, t appTarget) string {
	parts := []string{agent, t.app.ID}
	switch t.op {
	case policy.OpInstall:
		if t.pkg.Signer != "" {
			parts = append(parts, "signed by "+t.pkg.Signer)
		}
		parts = append(parts, "from "+t.from)
		if t.update() {
			parts = append(parts, "replaces "+t.pkg.Installed)
		}
	case policy.OpUninstall:
		parts = append(parts, "its data on the phone is deleted too")
	}
	if c := strings.TrimSpace(intent); c != "" {
		parts = append(parts, c)
	}
	return strings.Join(parts, " · ")
}

// installStanding is the Always allow an install's approval may keep: for
// the agent on the phone, the app id and the signer. None when another
// rule of the person's asks too (it would keep asking), or when the
// package names no app id or signer.
func installStanding(l *lease, t appTarget, v policy.Verdict) *standingAsk {
	if t.op != policy.OpInstall || len(v.Asks) != 1 || v.Asks[0] != policy.RuleInstall ||
		t.pkg.ID == "" || t.pkg.SignerKey == "" {
		return nil
	}
	return &standingAsk{rule: policy.RuleInstall, agent: l.session.agent.Name, device: l.dev.id, deviceName: l.dev.name(),
		app: t.app, signer: t.pkg.SignerKey, signerName: t.pkg.Signer}
}

// askApp asks the person, unless they allowed the same for the task, or
// always allow the agent to install the app by this signer on the phone.
// It returns nil once allowed.
func (g *Gateway) askApp(ctx context.Context, l *lease, req AppRequest, t appTarget, v policy.Verdict, args string) *Result {
	key := appGrantKey(t)
	g.mu.Lock()
	granted := l.appGrants[key]
	g.mu.Unlock()
	if granted {
		return nil
	}
	st := installStanding(l, t, v)
	if st != nil && g.policy.Standing(st.grant()) {
		return nil
	}
	s := l.session
	bind := key + "\x00" + t.pkg.Version + "\x00" + strings.Join(t.files, "\x00")
	b := approval.Binding{Session: s.id, Device: l.dev.id, Lease: l.id, Tool: "phone_app", Args: digest(bind)}
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
		title := appTitle(t, l.dev.name())
		info = g.approvals.Open(approval.Request{Kind: approval.KindAction, Binding: b, Rule: v.Rule, Title: title,
			Detail: appDetail(s.agent.Name, req.Intent, t), App: t.name, Agent: s.agent.Name, TaskID: l.task.id,
			DeviceName: l.dev.name(), Data: &appAsk{key: key, standing: st}})
		if res := g.afterOpen(l, info); res != nil {
			return res
		}
		g.openStep(l, info, "phone_app", args, "Waiting for you: "+strings.TrimSuffix(title, "?"), screen.Rect{}, screen.Point{}, "")
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
		return &Result{Status: StatusDenied, Rule: v.Rule, Note: denyNote(out),
			Message: fmt.Sprintf("The person did not allow this (%s). Nothing was done.", strings.TrimSuffix(info.Title, "?"))}
	}
	g.closeStep(info.ID, "allowed", out.Waited)
	if out.Decision != approval.AllowOnce {
		g.mu.Lock()
		if l.appGrants == nil {
			l.appGrants = map[string]bool{}
		}
		l.appGrants[key] = true
		g.mu.Unlock()
	}
	return nil
}

// runApp does what was allowed. An install runs from a copy of the files
// that is read again first, so the files cannot change between the
// person's answer and the install.
func (g *Gateway) runApp(ctx context.Context, l *lease, am driver.AppManager, t appTarget, args string, start time.Time) *Result {
	d := l.dev
	d.act.Lock()
	defer d.act.Unlock()
	var err error
	switch t.op {
	case policy.OpInstall:
		var tmp string
		var files []string
		tmp, files, err = stageFiles(t.files)
		if err != nil {
			res := errResult(ErrInternal, "Copying the app's files failed: %v. Nothing was installed.", err)
			g.recordApp(l, args, "Failed: "+appDoing(t), res, start)
			return res
		}
		defer os.RemoveAll(tmp)
		again, err2 := am.InspectPackage(ctx, files)
		if err2 == nil && (again.ID != t.pkg.ID || again.Version != t.pkg.Version || again.SignerKey != t.pkg.SignerKey) {
			res := errResult(ErrInvalidArgument, "%s changed after it was read: it now holds %s %s. Nothing was installed. Call phone_app again without approval_id.",
				t.from, again.ID, again.Version)
			g.recordApp(l, args, "Failed: "+appDoing(t), res, start)
			return res
		}
		err = err2
		if err == nil {
			err = am.InstallPackage(ctx, files)
		}
	case policy.OpUninstall:
		err = am.UninstallApp(ctx, t.id)
	case policy.OpStop:
		err = am.StopApp(ctx, t.id)
	}
	if err != nil {
		res := g.appError(l, err, appFailedVerb(t.op))
		g.recordApp(l, args, "Failed: "+appDoing(t), res, start)
		return res
	}
	res := &Result{Status: StatusSettled, Apps: []driver.App{{ID: t.app.ID, Name: t.name}}}
	switch t.op {
	case policy.OpInstall:
		if t.update() {
			res.Message = fmt.Sprintf("Updated “%s” (%s) from %s to %s; its data was kept. Open it with phone_launch.", t.name, t.app.ID, t.pkg.Installed, t.pkg.Version)
		} else {
			res.Message = fmt.Sprintf("Installed “%s” %s (%s). Open it with phone_launch.", t.name, t.pkg.Version, t.app.ID)
		}
	case policy.OpUninstall:
		res.Message = fmt.Sprintf("Uninstalled “%s” (%s); its data on the phone is gone.", t.name, t.app.ID)
	case policy.OpStop:
		res.Message = fmt.Sprintf("Stopped “%s”.", t.name)
	}
	g.recordApp(l, args, appDone(t), res, start)
	if t.op == policy.OpStop {
		return g.withCurrentScreen(l, res)
	}
	return res
}

// stageFiles copies the files to a new temporary folder, keeping their
// names; a .app folder is copied whole, without symbolic links.
func stageFiles(files []string) (string, []string, error) {
	tmp, err := os.MkdirTemp("", "pinky-app-")
	if err != nil {
		return "", nil, err
	}
	var out []string
	for i, f := range files {
		dst := filepath.Join(tmp, fmt.Sprint(i), filepath.Base(f))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			os.RemoveAll(tmp)
			return "", nil, err
		}
		if err := copyTree(f, dst); err != nil {
			os.RemoveAll(tmp)
			return "", nil, err
		}
		out = append(out, dst)
	}
	return tmp, out, nil
}

// copyTree copies a file, or a folder and what it holds; symbolic links
// and special files are refused.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		fi, err := e.Info()
		if err != nil {
			return err
		}
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			return fmt.Errorf("%s holds a symbolic link, %s, which Pinky does not install", filepath.Base(src), filepath.ToSlash(rel))
		case fi.IsDir():
			return os.MkdirAll(to, 0o700)
		case !fi.Mode().IsRegular():
			return fmt.Errorf("%s holds %s, which is not a file", filepath.Base(src), filepath.ToSlash(rel))
		}
		r, err := os.Open(p)
		if err != nil {
			return err
		}
		defer r.Close()
		w, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fi.Mode().Perm()|0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, r); err != nil {
			w.Close()
			return err
		}
		return w.Close()
	})
}

func appFailedVerb(op policy.Op) string {
	switch op {
	case policy.OpInstall:
		return "Installing failed"
	case policy.OpUninstall:
		return "Uninstalling failed"
	}
	return "Stopping the app failed"
}

// appError turns a driver's error into a result.
func (g *Gateway) appError(l *lease, err error, what string) *Result {
	none := map[bool]string{true: "Nothing was installed.", false: "Nothing was done."}[strings.HasPrefix(what, "Install") || strings.HasPrefix(what, "Reading")]
	switch {
	case errors.Is(err, driver.ErrPackage):
		return errResult(ErrInvalidArgument, "%v %s", sentence(err.Error()), none)
	case errors.Is(err, driver.ErrNotFound):
		return errResult(ErrNotFound, "%v %s", sentence(err.Error()), none)
	case errors.Is(err, driver.ErrUnsupported):
		return errResult(ErrUnsupported, "%v", err)
	case errors.Is(err, driver.ErrOffline):
		g.setOffline(l.dev, "")
		return errResult(ErrDeviceOffline, "%s is offline.", l.dev.name())
	case errors.Is(err, driver.ErrLocked):
		g.setLocked(l.dev, true)
		return statusResult(StatusLocked, "The phone is locked. Call phone_handoff so the person can unlock it.")
	case errors.Is(err, context.Canceled):
		return errResult(ErrInternal, "The call was cancelled.")
	case errors.Is(err, context.DeadlineExceeded):
		return errResult(ErrInternal, "%s: the phone did not answer in time (%v). Check the phone before trying again.", what, err)
	}
	return errResult(ErrInternal, "%s: %v", what, err)
}

// sentence ends a message with a full stop.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasSuffix(s, ".") || strings.HasSuffix(s, "?") || strings.HasSuffix(s, "!") {
		return s
	}
	return s + "."
}

// The history's words for phone_app steps (ui/gwtext.go translates them).

// appDoing is the step in the imperative: "install “Dinely” 1.3 (14)".
func appDoing(t appTarget) string {
	switch t.op {
	case policy.OpInstall:
		ver := ""
		if t.pkg.Version != "" {
			ver = " " + t.pkg.Version
		}
		if t.update() {
			if ver != "" {
				ver = " to" + ver
			}
			return fmt.Sprintf("update “%s”%s", t.name, ver)
		}
		return fmt.Sprintf("install “%s”%s", t.name, ver)
	case policy.OpUninstall:
		return fmt.Sprintf("uninstall “%s”", t.name)
	}
	return fmt.Sprintf("stop “%s”", t.name)
}

// appDone is the step once done: "Installed “Dinely” 1.3 (14) from
// “app-debug.apk”".
func appDone(t appTarget) string {
	switch t.op {
	case policy.OpInstall:
		ver := ""
		if t.pkg.Version != "" {
			ver = " " + t.pkg.Version
		}
		if t.update() {
			if ver != "" {
				ver = " to" + ver
			}
			return fmt.Sprintf("Updated “%s”%s from %s", t.name, ver, t.from)
		}
		return fmt.Sprintf("Installed “%s”%s from %s", t.name, ver, t.from)
	case policy.OpUninstall:
		return fmt.Sprintf("Uninstalled “%s”", t.name)
	}
	return fmt.Sprintf("Stopped “%s”", t.name)
}

// appRefused is the step policy refused.
func appRefused(t appTarget, res *Result) string {
	if res.Error == ErrScopeDenied {
		return "Outside the scope: " + appDoing(t)
	}
	return "Blocked by “" + res.Rule + "”: " + appDoing(t)
}

// appFailed is the step that failed before Pinky knew the app.
func appFailed(op policy.Op, rels []string, name string) string {
	if op == policy.OpInstall {
		return "Failed: install from " + fromText(rels)
	}
	return "Failed: " + string(op) + " “" + strings.TrimSpace(name) + "”"
}

// recordApp adds a phone_app step to the task.
func (g *Gateway) recordApp(l *lease, args, text string, res *Result, start time.Time) {
	status := string(res.Status)
	if res.Error != "" {
		status = string(res.Error)
	}
	g.addStep(l, StepView{Tool: "phone_app", Args: args, Text: text, Status: status, At: start, Elapsed: time.Since(start)})
}

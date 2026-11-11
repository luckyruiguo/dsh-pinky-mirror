package ui

import (
	"time"

	"github.com/egoist/mygo"
	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/gateway"
)

// Hand edits of the rules files. The gateway takes in edits of rules.yaml
// and apps.yaml made while Pinky runs, and Settings shows what they hold:
// the lists of Safety read the gateway every few seconds, and the preset,
// Read-only mode, Hide codes and the blocked apps come again from
// Options.GatewaySettings once the policy changed. An edit the gateway
// cannot use changes nothing, and Settings says what is wrong, at the top
// of Safety and under Advanced › Rules file, until the person fixes the
// file. Meanwhile the controls that would change that file are off: the
// gateway refuses such changes, so a switch would show what is not in
// force.

// The rules files, as gateway.FileProblem names them.
const (
	rulesYAML = "rules.yaml"
	appsYAML  = "apps.yaml"
)

// fileProblemsState is what Settings knows of the rules files that cannot
// be used.
type fileProblemsState struct {
	list     []gateway.FileProblem
	seq      int
	lastLoad time.Time
}

// watchFileProblems reads the rules files' problems every two seconds
// while Settings shows.
func (a *app) watchFileProblems(c *mg.Context) {
	fl, ok := a.api.(fileProblemLister)
	if !ok {
		return
	}
	st := &a.files
	if time.Since(st.lastLoad) > 2*time.Second {
		st.seq++
		seq := st.seq
		st.lastLoad = time.Now()
		go func() {
			list := fl.FileProblems()
			a.post(func() {
				if seq == st.seq {
					st.list = list
				}
			})
		}()
	}
	c.After(2 * time.Second)
}

// gwSettingsState is what Settings knows of the settings the gateway
// keeps: the policy's version it shows (-1 before the first read), and
// whether a read is under way.
type gwSettingsState struct {
	version  int
	reading  bool
	lastLoad time.Time
}

// watchGatewaySettings reads what the gateway keeps of the settings every
// two seconds while Settings shows, when the policy changed since the
// version Settings shows: after a hand edit of the rules files, and after
// the person's own changes, which come back as they were made.
func (a *app) watchGatewaySettings(c *mg.Context) {
	read := a.opt.GatewaySettings
	st := &a.gwSet
	if read == nil {
		return
	}
	if !st.reading && time.Since(st.lastLoad) > 2*time.Second {
		st.reading, st.lastLoad = true, time.Now()
		since, changes := st.version, a.changes
		go func() {
			s, v, ok := read(since)
			a.post(func() {
				st.reading = false
				// A change made meanwhile is newer than what was read: read
				// again next time.
				if ok && changes == a.changes {
					a.takeGatewaySettings(s)
					st.version = v
				}
			})
		}()
	}
	c.After(2 * time.Second)
}

// takeGatewaySettings shows what the gateway keeps of the settings, and
// makes it what the next change reports as before it, so that the change
// passes on only what the person changed, never a value a hand edit
// replaced.
func (a *app) takeGatewaySettings(g Settings) {
	for _, s := range []*Settings{&a.settings, &a.reported} {
		s.Preset, s.HideCodes, s.ReadOnly = g.Preset, g.HideCodes, g.ReadOnly
		s.BlockedApps = append([]BlockedApp(nil), g.BlockedApps...)
	}
}

// fileBroken reports whether a rules file (rulesYAML or appsYAML) has a
// hand edit that cannot be used.
func (a *app) fileBroken(file string) bool {
	for _, p := range a.files.list {
		if p.File == file {
			return true
		}
	}
	return false
}

// offWhile dims the controls in it while file cannot be used, and turns
// them off.
func (a *app) offWhile(c *mg.Context, file string) mg.Element {
	col := mg.Column(c)
	if a.fileBroken(file) {
		col.Disabled(true).Opacity(0.5)
	}
	return col
}

// fileProblemNotes shows what is wrong with each rules file that cannot be
// used, with a button that opens it.
func (a *app) fileProblemNotes(c *mg.Context) {
	p := pal(c)
	for _, fp := range a.files.list {
		fp := fp
		title := locales.SettingsFileProblemTitle(fp.File)
		mg.Row(c.Key("file-problem-"+fp.File)).Gap(8).AlignItems(mg.Start).Padding(12, 14).Radius(10).
			Background(p.amberTint).Border(1, p.amberLine).Role(mg.RoleStatus).Label(title).Children(func() {
			mg.Icon(c, icAlert).FontSize(16).TextColor(p.amber).Margin(1, 0, 0, 0)
			mg.Column(c).Gap(4).Grow(1).MinWidth(0).Children(func() {
				mg.Text(c, title).FontWeight(fwSemibold)
				mg.Text(c, locales.SettingsFileProblemDesc()).FontSize(fsSmall).TextColor(p.ink2)
				mg.Text(c, locales.SettingsFileProblemError(fp.Error)).FontSize(fsSmall).Selectable()
				mg.Row(c).Margin(4, 0, 0, 0).Children(func() {
					if button(c, locales.SettingsFileProblemEdit(fp.File), btnOpt{small: true}).Clicked() {
						a.openRulesFile(fp.File, fp.Path)
					}
				})
			})
		})
	}
}

// openRulesFile opens a rules file in the computer's editor.
func (a *app) openRulesFile(file, path string) {
	go func() {
		if err := mygo.Shell.OpenPath(path); err != nil {
			a.post(func() { a.toasts = append(a.toasts, locales.SettingsFileProblemOpenFailed(file, err.Error())) })
		}
	}()
}

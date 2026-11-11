package ui

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/agents"
	"github.com/DIYgod/pinky/internal/gateway"
)

// agentProgram is the program that agents on this computer run to reach
// this Pinky: its own executable, by its full path. No other program can
// stand in for a path, as one named pinky can on the PATH (coreutils has a
// pinky command, and Homebrew's coreutils links it), and agents started
// from the Dock or the Start menu find it without the terminal's PATH,
// where the cask's pinkyctl and the Linux packages' are.
func agentProgram() string {
	exe, err := os.Executable()
	if err != nil {
		return "pinkyctl"
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	// macOS runs an app opened from a disk image or the Downloads folder
	// from a copy of its own, gone once it quits; the app belongs in
	// Applications.
	if runtime.GOOS == "darwin" && strings.Contains(exe, "/AppTranslocation/") {
		exe = "/Applications/Pinky.app/Contents/MacOS/" + filepath.Base(exe)
	}
	return exe
}

// AgentServer is the MCP server that Settings › Agents adds to agents:
// this Pinky's executable, by its full path, with mcp.
func AgentServer() agents.Server {
	return agents.Server{Command: agentProgram(), Args: []string{"mcp"}}
}

// shellWord quotes s for the shell people paste a command into, when it
// needs it: in single quotes on macOS and Linux, in double quotes on
// Windows.
func shellWord(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"'$`\\!*?[]{}()<>|&;#~=%,") {
		return s
	}
	if runtime.GOOS == "windows" {
		return `"` + s + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// agentCommand is the command line that starts Pinky's MCP server over
// stdio for an agent on this computer.
func agentCommand() string { return shellWord(agentProgram()) + " mcp" }

// AgentSetup finds the agents on this computer and adds Pinky to them, as
// internal/agents.Setup does. Its methods block: the client calls them off
// the main thread.
type AgentSetup interface {
	Find(ctx context.Context) []agents.Status
	Connect(ctx context.Context, id string) (agents.Result, error)
	Remove(ctx context.Context, id string) (agents.Result, error)
}

// setupState is Settings › Agents › Connect an agent's, and the sidebar
// card's.
type setupState struct {
	list     []agents.Status
	loaded   bool
	seq      int
	lastLoad time.Time
	loading  bool
	busy     map[string]bool          // agents being changed
	errs     map[string]string        // why the last change failed, by agent
	done     map[string]agents.Result // the last change that worked, by agent
	removed  map[string]bool          // ... and it was a removal
	later    bool                     // the card's Not now, until Pinky starts again
	byHand   bool                     // the commands to add an agent by hand show
}

// agentURLs are where to get the agents the card suggests to a person who
// has none.
var agentURLs = []struct{ id, name, url string }{
	{"claude-code", "Claude Code", "https://claude.com/product/claude-code"},
	{"claude-desktop", "Claude Desktop", "https://claude.com/download"},
}

// loadSetup finds the agents again, off the main thread, unless that was
// done less than every ago.
func (a *app) loadSetup(every time.Duration) {
	s := &a.setup
	if a.opt.AgentSetup == nil || s.loading || len(s.busy) > 0 || !s.lastLoad.IsZero() && time.Since(s.lastLoad) < every {
		return
	}
	s.seq++
	seq := s.seq
	s.lastLoad, s.loading = time.Now(), true
	setup := a.opt.AgentSetup
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 20*time.Second)
		defer cancel()
		list := setup.Find(ctx)
		a.post(func() {
			s.loading = false
			if seq != s.seq {
				return
			}
			s.list, s.loaded, s.lastLoad = list, true, time.Now()
		})
	}()
}

// changeAgent connects an agent, or removes Pinky from it, off the main
// thread. Only a click of the person's does this.
func (a *app) changeAgent(id string, remove bool) {
	s := &a.setup
	if a.opt.AgentSetup == nil || s.busy[id] {
		return
	}
	if s.busy == nil {
		s.busy, s.errs, s.done, s.removed = map[string]bool{}, map[string]string{}, map[string]agents.Result{}, map[string]bool{}
	}
	s.busy[id] = true
	delete(s.errs, id)
	delete(s.done, id)
	setup := a.opt.AgentSetup
	name := id
	for _, st := range s.list {
		if st.ID == id {
			name = st.Name
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 90*time.Second)
		defer cancel()
		var res agents.Result
		var err error
		if remove {
			res, err = setup.Remove(ctx, id)
		} else {
			res, err = setup.Connect(ctx, id)
		}
		list := setup.Find(ctx)
		a.post(func() {
			delete(s.busy, id)
			s.seq++ // a Find started before the change reads the old state
			s.list, s.loaded, s.lastLoad = list, true, time.Now()
			if err != nil {
				s.errs[id] = err.Error()
				return
			}
			s.done[id], s.removed[id] = res, remove
			switch {
			case res.Opened != "":
				a.toasts = append(a.toasts, locales.ConnectToastOpened(name))
			case !remove:
				a.toasts = append(a.toasts, locales.ConnectToastConnected(name))
			}
		})
	}()
}

// agentsConnected reports whether some agent can reach Pinky: its
// settings have Pinky, or one has connected (it is in the gateway's list of
// agents, or ran a task).
func (a *app) agentsConnected() bool {
	for _, st := range a.setup.list {
		if st.State != agents.NotConnected {
			return true
		}
	}
	if len(a.agents.list) > 0 {
		return true
	}
	for _, t := range append(append([]gateway.TaskView(nil), a.tasks...), a.act.tasks...) {
		if t.Agent != "" {
			return true
		}
	}
	return false
}

// foundAgents are the agents on this computer.
func (a *app) foundAgents() []agents.Status {
	var out []agents.Status
	for _, st := range a.setup.list {
		if st.Found || st.State != agents.NotConnected {
			out = append(out, st)
		}
	}
	return out
}

// connectAgents is the Connect an agent part of Settings › Agents: the
// agents on this computer, each with Connect or Remove, then the commands
// to add one by hand.
func (a *app) connectAgents(c *mg.Context) {
	p := pal(c)
	s := &a.setup
	a.loadSetup(10 * time.Second)
	if a.opt.AgentSetup != nil {
		c.After(10 * time.Second)
	}
	mg.Column(c).Gap(10).Label(locales.ConnectTitle()).Children(func() {
		intro := locales.ConnectIntro()
		if a.opt.AgentSetup == nil {
			intro = locales.ConnectIntroByHand()
		}
		settingsHeading(c, locales.ConnectTitle(), intro)
		if a.opt.AgentSetup != nil {
			found := a.foundAgents()
			switch {
			case !s.loaded:
				mg.Text(c, locales.ConnectLooking()).FontSize(fsSmall).TextColor(p.ink2).Padding(10, 0)
			case len(found) == 0:
				a.noAgents(c)
			default:
				group(c).Label(locales.ConnectFoundLabel()).Children(func() {
					for i, st := range found {
						a.agentRow(c, st, i == len(found)-1)
					}
				})
			}
			var missing []string
			for _, st := range s.list {
				if !st.Found && st.State == agents.NotConnected {
					missing = append(missing, st.Name)
				}
			}
			if s.loaded && len(missing) > 0 && len(found) > 0 {
				mg.Text(c, locales.ConnectNotHere(joinAnd(missing))).FontSize(fsCaption).TextColor(p.ink3)
			}
			if linkButton(c, byHandLabel(s.byHand)).AlignSelf(mg.Start).Clicked() {
				s.byHand = !s.byHand
			}
			if !s.byHand {
				return
			}
		}
		cmd := agentCommand()
		group(c).Children(func() {
			a.commandBlock(c, "Claude Code", locales.ConnectClaudeHint(), "claude mcp add --scope user pinky -- "+cmd,
				locales.RemoteCommandLabel(), locales.ConnectCopyClaude(), false)
			a.commandBlock(c, locales.ConnectOtherTitle(), locales.ConnectOtherHint(), cmd,
				locales.ConnectCommandLabel(), locales.ConnectCopyOther(), true)
		})
	})
}

func byHandLabel(open bool) string {
	if open {
		return locales.ConnectByHandHide()
	}
	return locales.ConnectByHand()
}

// agentRow is an agent on this computer: its name, whether it has Pinky,
// and the button that changes that.
func (a *app) agentRow(c *mg.Context, st agents.Status, last bool) {
	p := pal(c)
	s := &a.setup
	box := groupRow(c, mg.Column(c.Key("setup-"+st.ID)).Gap(6).Justify(mg.Center).Label(st.Name), last)
	box.Children(func() {
		mg.Row(c).Gap(sp3).AlignItems(mg.Center).Children(func() {
			appIcon(c, st.Name, 32, rControl, 14, agentColor(st.ID))
			mg.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				mg.Text(c, st.Name).FontWeight(fwMedium)
				line, col := agentLine(st), p.ink2
				if st.Problem != "" {
					col = p.red
				}
				mg.Text(c, line).FontSize(fsSmall).LineHeight(1.4).TextColor(col)
			})
			switch {
			case s.busy[st.ID]:
				mg.Spinner(c).FontSize(16).Label(locales.ConnectWorking(st.Name))
			case st.Manual || st.Problem != "" || st.Extension:
				// Only the agent's app removes or turns on its extension.
			case st.State == agents.Connected:
				if button(c, locales.ConnectRemove(), btnOpt{}).Label(locales.ConnectRemoveLabel(st.Name)).
					Tooltip(locales.ConnectRemoveTip(tildePath(st.Settings))).Clicked() {
					a.changeAgent(st.ID, true)
				}
			case st.State == agents.Outdated:
				mg.Row(c).Gap(8).Children(func() {
					if button(c, locales.ConnectRemove(), btnOpt{}).Label(locales.ConnectRemoveLabel(st.Name)).
						Tooltip(locales.ConnectRemoveTip(tildePath(st.Settings))).Clicked() {
						a.changeAgent(st.ID, true)
					}
					if button(c, locales.ConnectUpdate(), btnOpt{kind: btnPrimary}).Label(locales.ConnectUpdateLabel(st.Name)).
						Tooltip(locales.ConnectTip(tildePath(st.Settings))).Clicked() {
						a.changeAgent(st.ID, false)
					}
				})
			default:
				if button(c, locales.ConnectConnect(), btnOpt{kind: btnPrimary}).Label(locales.ConnectConnectLabel(st.Name)).
					Tooltip(connectTip(st)).Clicked() {
					a.changeAgent(st.ID, false)
				}
			}
		})
		if st.Problem != "" && !st.Manual && st.Settings != "" {
			if linkButton(c, locales.ConnectShowFile()).Label(locales.ConnectShowFileLabel(st.Name)).Margin(0, 0, 0, 44).AlignSelf(mg.Start).Clicked() {
				a.reveal(st.Settings)
			}
		}
		if msg := s.errs[st.ID]; msg != "" {
			mg.Text(c, locales.ConnectFailed(msg)).FontSize(fsSmall).TextColor(p.red).Role(mg.RoleStatus).Padding(0, 0, 0, 44)
		} else if res, ok := s.done[st.ID]; ok && !(res.Opened != "" && st.State == agents.Connected) {
			mg.Column(c).Gap(4).Padding(0, 0, 0, 44).Children(func() {
				mg.Text(c, doneLine(st, res, s.removed[st.ID])).FontSize(fsSmall).TextColor(p.ink2).Role(mg.RoleStatus)
				if len(res.Backups) > 0 {
					path := res.Backups[len(res.Backups)-1]
					mg.Row(c).Gap(8).AlignItems(mg.Center).Children(func() {
						mg.Text(c, locales.ConnectKeptCopy()).FontSize(fsCaption).TextColor(p.ink3)
						if linkButton(c, locales.ConnectShowCopy()).Label(locales.ConnectShowCopyLabel(st.Name)).Clicked() {
							a.reveal(path)
						}
					})
				}
			})
		}
	})
}

// connectTip says what Connect does for an agent.
func connectTip(st agents.Status) string {
	if st.Opens {
		return locales.ConnectTipOpens(st.Name)
	}
	return locales.ConnectTip(tildePath(st.Settings))
}

// agentLine is an agent's second line: whether it has Pinky, and what is
// wrong with it.
func agentLine(st agents.Status) string {
	switch {
	case st.Problem != "" && st.Manual:
		return locales.ConnectManual()
	case st.Problem != "":
		return locales.ConnectProblem(st.Problem)
	case st.Extension && st.State == agents.Connected:
		return locales.ConnectStateExtension(st.Name)
	case st.State == agents.Connected:
		return locales.ConnectStateConnected()
	case st.State == agents.Outdated:
		switch st.Why {
		case agents.WhyOff:
			return locales.ConnectWhyOff(st.Name)
		case agents.WhyMissing:
			return locales.ConnectWhyMissing()
		case agents.WhyCoreutils:
			return locales.ConnectWhyCoreutils()
		case agents.WhyOtherPinky:
			return locales.ConnectWhyOtherPinky()
		case agents.WhySkill:
			return locales.ConnectWhySkill()
		}
		return locales.ConnectWhyOther(st.Command)
	}
	return locales.ConnectStateNotConnected()
}

// doneLine says what a change did and what the person does next.
func doneLine(st agents.Status, res agents.Result, removed bool) string {
	if res.Opened != "" {
		return locales.ConnectDoneOpened(st.Name)
	}
	if removed {
		if res.Restart == agents.RestartApp {
			return locales.ConnectRemovedRestart(st.Name)
		}
		return locales.ConnectRemoved()
	}
	switch res.Restart {
	case agents.RestartApp:
		return locales.ConnectDoneRestart(st.Name)
	case agents.RestartSession:
		return locales.ConnectDoneSession(st.Name)
	}
	return locales.ConnectDoneNow(st.Name)
}

// noAgents is Connect an agent without an agent on this computer: where to
// get one.
func (a *app) noAgents(c *mg.Context) {
	p := pal(c)
	group(c).Gap(sp3).Padding(sp4).Children(func() {
		mg.Text(c, locales.ConnectNoneFound()).FontSize(fsSmall).LineHeight(1.4)
		mg.Row(c).Gap(8).Wrap().Children(func() {
			for _, g := range agentURLs {
				if button(c, locales.ConnectGet(g.name), btnOpt{small: true}).Clicked() {
					openURL(c, g.url, func(error) {})
				}
			}
		})
		var others []string
		for _, st := range a.setup.list {
			if st.ID != agentURLs[0].id && st.ID != agentURLs[1].id {
				others = append(others, st.Name)
			}
		}
		if len(others) > 0 {
			mg.Text(c, locales.ConnectAlsoWorks(joinAnd(others))).FontSize(fsCaption).TextColor(p.ink3)
		}
	})
}

// agentColor is an agent's icon color.
func agentColor(id string) string {
	switch id {
	case "claude-code", "claude-desktop":
		return "#c96442"
	case "codex":
		return "#10a37f"
	case "cursor":
		return "#26251e"
	case "vscode":
		return "#0078d4"
	case "windsurf":
		return "#0b8f75"
	case "gemini":
		return "#4285f4"
	case "copilot":
		return "#24292f"
	}
	return "#5450d6"
}

// tildePath writes a path in the home folder as ~/…, as people know it.
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || p == "" {
		return p
	}
	if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.Join("~", rel)
	}
	return p
}

// agentCard is the sidebar's card while no agent can reach Pinky: the
// agents found here to connect, where to get one when there is none, or an
// agent whose entry no longer starts Pinky.
func (a *app) agentCard(c *mg.Context) {
	s := &a.setup
	if a.opt.AgentSetup == nil {
		return
	}
	a.loadSetup(30 * time.Second)
	if !s.loaded || s.later {
		return
	}
	if a.page == pageSettings && a.set.sec == secAgents {
		return // the page says it all
	}
	// The gateway's agents count too (one added by hand under another
	// name): wait for its list before suggesting one.
	if _, ok := a.api.(agentLister); ok && !a.agents.loaded {
		if a.agents.lastLoad.IsZero() {
			a.agents.load(a)
		}
		return
	}
	p := pal(c)
	// An agent whose entry starts nothing that works: fix it.
	for _, st := range s.list {
		if st.State == agents.Outdated && (st.Why == agents.WhyMissing || st.Why == agents.WhyCoreutils) && st.Problem == "" {
			sideCard(c, "agent-card", locales.ConnectCardBroken(st.Name), func() {
				cardHead(c, locales.ConnectCardBroken(st.Name))
				mg.Text(c, agentLine(st)).FontSize(fsSmall).TextColor(p.ink2)
				a.cardButtons(c, st, locales.ConnectFix(), locales.ConnectFixLabel(st.Name))
			})
			return
		}
	}
	if a.agentsConnected() {
		return
	}
	found := a.foundAgents()
	sideCard(c, "agent-card", locales.ConnectTitle(), func() {
		cardHead(c, locales.ConnectTitle())
		switch len(found) {
		case 0:
			mg.Text(c, locales.ConnectCardNone()).FontSize(fsSmall).TextColor(p.ink2)
			mg.Row(c).Gap(10).AlignItems(mg.Center).Children(func() {
				if button(c, locales.ConnectCardGet(), btnOpt{kind: btnPrimary, small: true}).Clicked() {
					a.page, a.set.sec = pageSettings, secAgents
				}
				if quietButton(c, locales.ConnectCardLater()).Clicked() {
					s.later = true
				}
			})
		case 1:
			mg.Text(c, locales.ConnectCardFoundOne(found[0].Name)).FontSize(fsSmall).TextColor(p.ink2)
			if msg := s.errs[found[0].ID]; msg != "" {
				mg.Text(c, locales.ConnectFailed(msg)).FontSize(fsSmall).TextColor(p.red).Role(mg.RoleStatus)
			} else if res, ok := s.done[found[0].ID]; ok && res.Opened != "" {
				mg.Text(c, locales.ConnectDoneOpened(found[0].Name)).FontSize(fsSmall).TextColor(p.ink2).Role(mg.RoleStatus)
			}
			a.cardButtons(c, found[0], locales.ConnectConnectLabel(found[0].Name), locales.ConnectConnectLabel(found[0].Name))
		default:
			names := make([]string, len(found))
			for i, st := range found {
				names[i] = st.Name
			}
			mg.Text(c, locales.ConnectCardFound(joinAnd(names))).FontSize(fsSmall).TextColor(p.ink2)
			mg.Row(c).Gap(10).AlignItems(mg.Center).Children(func() {
				if button(c, locales.ConnectCardChoose(), btnOpt{kind: btnPrimary, small: true}).Clicked() {
					a.page, a.set.sec = pageSettings, secAgents
				}
				if quietButton(c, locales.ConnectCardLater()).Clicked() {
					s.later = true
				}
			})
		}
	})
}

func cardHead(c *mg.Context, title string) {
	p := pal(c)
	mg.Row(c).Gap(7).AlignItems(mg.Center).Children(func() {
		mg.Icon(c, icPlug).FontSize(15).TextColor(p.accentInk)
		mg.Text(c, title).FontSize(fsBody).FontWeight(fwSemibold).Grow(1).MinWidth(0)
	})
}

// cardButtons are the card's button that connects one agent, and Not now.
func (a *app) cardButtons(c *mg.Context, st agents.Status, text, label string) {
	s := &a.setup
	mg.Row(c).Gap(10).AlignItems(mg.Center).Wrap().Children(func() {
		if s.busy[st.ID] {
			mg.Spinner(c).FontSize(16).Label(locales.ConnectWorking(st.Name))
			return
		}
		if button(c, text, btnOpt{kind: btnPrimary, small: true}).Label(label).Tooltip(connectTip(st)).Clicked() {
			a.changeAgent(st.ID, false)
		}
		if quietButton(c, locales.ConnectCardLater()).Clicked() {
			s.later = true
		}
	})
}

// commandBlock shows a command under a title and a hint, with a button
// that copies it; label names the command and copyLabel the button. It is
// a row of a group, the last one when last is set.
func (a *app) commandBlock(c *mg.Context, title, hint, cmd, label, copyLabel string, last bool) {
	commandRow(c, title, hint, cmd, label, last, func() {
		if button(c, "", btnOpt{small: true, icon: icCopy, label: copyLabel}).Clicked() {
			a.copy(c, cmd, locales.RemoteCopiedCommand())
		}
	})
}

// commandRow is a row of a group with a command: a title and a hint with
// buttons at the end, then the command as shown.
func commandRow(c *mg.Context, title, hint, shown, label string, last bool, buttons func()) {
	p := pal(c)
	groupRow(c, mg.Column(c).Gap(sp2), last).Children(func() {
		mg.Row(c).Gap(sp3).AlignItems(mg.Center).Children(func() {
			mg.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				mg.Text(c, title).FontWeight(fwMedium)
				mg.Text(c, hint).FontSize(fsSmall).LineHeight(1.4).TextColor(p.ink2)
			})
			buttons()
		})
		mg.Text(c, shown).Font("monospace").FontSize(fsCaption).LineHeight(1.5).Padding(10, sp3).Radius(rControl).
			Background(p.bg).Border(1, p.line).Selectable().Label(label)
	})
}

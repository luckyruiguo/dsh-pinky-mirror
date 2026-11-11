package agents

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
)

// State says whether an agent starts this Pinky.
type State int

const (
	// NotConnected: the agent has no entry for Pinky.
	NotConnected State = iota
	// Connected: it starts this Pinky, and has its skill where it reads
	// one.
	Connected
	// Outdated: it has an entry for Pinky that starts something else (a
	// Pinky that moved, coreutils' pinky), or an old skill. Connecting
	// again sets it right.
	Outdated
)

// Status is an agent as Settings › Agents shows it.
type Status struct {
	ID, Name string
	// URL is where to get the agent.
	URL string
	// Found: the agent is on this computer.
	Found bool
	State State
	// Command is the command line its entry for Pinky starts, as written.
	Command string
	// Why says what is out of date, for State Outdated.
	Why Why
	// Settings is the file that holds its MCP servers.
	Settings string
	// Skills is the folder of skills Pinky's goes into; "" when the agent
	// reads none.
	Skills string
	// Restart: the agent reads its settings when it starts, so a change
	// takes effect once it starts again.
	Restart RestartKind
	// Problem is why Pinky cannot read the agent's settings or change
	// them, such as a file with a mistake in it.
	Problem string
	// Manual: Pinky cannot change this agent's settings (Problem says
	// why); the person adds the command by hand.
	Manual bool
	// Extension: Pinky is installed as an extension of the agent's app
	// (Claude Desktop), which the app's own settings remove.
	Extension bool
	// Opens: Connect opens Pinky's extension in the agent's app, which asks
	// the person to install it, rather than change a file.
	Opens bool
}

// Why is what is out of date about an agent's entry.
type Why int

const (
	WhyNone         Why = iota
	WhyOtherPinky       // it starts a Pinky at another path
	WhyMissing          // the program it starts is not there
	WhyCoreutils        // it starts "pinky", which is coreutils' command on many computers
	WhySkill            // the entry is right, the skill is old or missing
	WhyOtherCommand     // it starts something else under the name pinky
	WhyOff              // Pinky's extension is turned off in the app
)

// RestartKind is what the person does for a change to take effect.
type RestartKind int

const (
	RestartNone    RestartKind = iota // it reads the change at once
	RestartSession                    // in its next session (a CLI)
	RestartApp                        // once the app is quit and opened again
)

// Result is what Connect or Remove did.
type Result struct {
	// Backups are the copies of files as they were before the change.
	Backups []string
	// Restart says what the person does for the change to take effect.
	Restart RestartKind
	// Opened is Pinky's MCP bundle, opened in the agent's app, which asks
	// the person to install it there.
	Opened string
}

// Setup finds agents and changes their settings. Its methods may block
// on files and the agents' commands: call them off the main thread.
type Setup struct {
	Env    *Env
	Server Server
	// Skill is the text of SKILL.md; empty installs none.
	Skill string
	// Dir is a folder of Pinky's own: copies of the files Pinky changes
	// (backups/<agent>/), and the MCP bundle it opens in Claude Desktop.
	Dir string
	// Bundle is what goes in that bundle beside the command (Version, Icon,
	// Tools).
	Bundle BundleOptions

	mu sync.Mutex
}

func (s *Setup) backups() string {
	if s.Dir == "" {
		return ""
	}
	return filepath.Join(s.Dir, "backups")
}

// Find reports on every agent Pinky knows, on this computer or not.
func (s *Setup) Find(ctx context.Context) []Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Status, 0, len(catalog))
	for _, a := range catalog {
		if ctx.Err() != nil {
			break
		}
		out = append(out, s.status(a))
	}
	return out
}

func agentByID(id string) *agent {
	for _, a := range catalog {
		if a.id == id {
			return a
		}
	}
	return nil
}

func (s *Setup) status(a *agent) Status {
	e := s.Env
	st := Status{ID: a.id, Name: a.name(e), URL: a.url, Found: a.found(e), Restart: a.restart}
	if a.skills != nil {
		st.Skills = a.skills(e)
	}
	pl, entry, ok, err := a.active(e)
	st.Settings = pl.file
	switch {
	case err != nil:
		st.Problem = s.tilde(err.Error())
		return st
	case st.Settings == "":
		st.Manual, st.Problem = true, "Pinky does not know where this agent keeps its settings here"
		return st
	case !ok && a.extension != nil:
		st.Opens = a.bundle != nil
		if installed, enabled := a.extension(s); installed {
			st.State, st.Extension = Connected, true
			if !enabled {
				st.State, st.Why = Outdated, WhyOff
			}
		}
		return st
	case !ok:
		return st
	}
	cmd, args := commandOf(entry)
	st.Command = strings.TrimSpace(strings.Join(append([]string{cmd}, args...), " "))
	st.State, st.Why = Connected, s.compare(cmd, args)
	if st.Why == WhyNone && st.Skills != "" && s.Skill != "" && readSkill(st.Skills, s.Skill) != skillCurrent {
		st.Why = WhySkill
	}
	if st.Why != WhyNone {
		st.State = Outdated
	}
	return st
}

// compare says what differs between an entry's command and this Pinky's.
func (s *Setup) compare(cmd string, args []string) Why {
	if !reflect.DeepEqual(args, s.Server.Args) {
		return WhyOtherCommand
	}
	if cmd == s.Server.Command {
		return WhyNone
	}
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(cmd)), ".exe")
	switch {
	case cmd == "pinky":
		return WhyCoreutils
	case base == "pinkyctl" && !filepath.IsAbs(cmd):
		if s.Env.lookPath("pinkyctl") != "" {
			return WhyNone // the cask's or a package's command, which works too
		}
		return WhyMissing
	case !filepath.IsAbs(cmd):
		return WhyOtherCommand
	case !exists(cmd):
		return WhyMissing
	case base == "pinky" || base == "pinkyctl":
		if base == "pinkyctl" {
			return WhyNone
		}
		return WhyOtherPinky
	}
	return WhyOtherCommand
}

// commandOf reads the command and arguments of an entry: {"command":
// "…", "args": […]}, or opencode's {"command": ["…", …]}.
func commandOf(entry any) (string, []string) {
	m, _ := entry.(map[string]any)
	var cmd string
	var args []string
	switch c := m["command"].(type) {
	case string:
		cmd = c
	case []any:
		for i, x := range c {
			if s, ok := x.(string); ok {
				if i == 0 {
					cmd = s
				} else {
					args = append(args, s)
				}
			}
		}
	}
	if list, ok := m["args"].([]any); ok {
		for _, x := range list {
			if s, ok := x.(string); ok {
				args = append(args, s)
			}
		}
	}
	return cmd, args
}

// ErrUnknown is the error for an agent Pinky does not know.
var ErrUnknown = errors.New("Pinky does not know this agent")

// Connect adds Pinky to an agent, or sets its entry right, and installs
// its skill.
func (s *Setup) Connect(ctx context.Context, id string) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := agentByID(id)
	if a == nil {
		return Result{}, ErrUnknown
	}
	res, err := a.connect(ctx, s)
	if err != nil {
		return res, s.said(err)
	}
	if a.skills != nil && s.Skill != "" && res.Opened == "" {
		if dir := a.skills(s.Env); dir != "" {
			if err := installSkill(dir, s.Skill); err != nil {
				return res, s.said(fmt.Errorf("Pinky was added, but its guide was not: %w", err))
			}
		}
	}
	return res, nil
}

// ErrExtension is Remove's error for Pinky installed as an extension of
// the agent's app (Claude Desktop), which only the app removes.
var ErrExtension = errExtension

// Remove takes Pinky out of an agent: its entries, and its skill unless
// another agent that reads the same folder of skills still has Pinky.
func (s *Setup) Remove(ctx context.Context, id string) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := agentByID(id)
	if a == nil {
		return Result{}, ErrUnknown
	}
	res, err := a.remove(ctx, s)
	if err != nil {
		if errors.Is(err, errExtension) {
			return res, err
		}
		return res, s.said(err)
	}
	if a.skills == nil {
		return res, nil
	}
	dir := a.skills(s.Env)
	if dir == "" {
		return res, nil
	}
	for _, o := range catalog {
		if o != a && o.skills != nil && o.skills(s.Env) == dir {
			if _, _, ok, _ := o.active(s.Env); ok {
				return res, nil // it still reads the skill
			}
		}
	}
	if err := removeSkill(dir); err != nil {
		return res, s.said(fmt.Errorf("Pinky was taken out, but its guide was not: %w", err))
	}
	return res, nil
}

func (s *Setup) now() time.Time {
	if s.Env.Now != nil {
		return s.Env.Now()
	}
	return time.Now()
}

// tilde writes the home folder in msg as ~, as people know their files.
func (s *Setup) tilde(msg string) string {
	h := s.Env.Home
	if h == "" || h == string(filepath.Separator) {
		return msg
	}
	return strings.ReplaceAll(msg, h+string(filepath.Separator), "~"+string(filepath.Separator))
}

// shownError is an error as the person reads it, its paths under ~.
type shownError struct {
	msg string
	err error
}

func (e *shownError) Error() string { return e.msg }
func (e *shownError) Unwrap() error { return e.err }

func (s *Setup) said(err error) error {
	if err == nil {
		return nil
	}
	return &shownError{msg: s.tilde(err.Error()), err: err}
}

// RefreshSkills rewrites every copy of Pinky's skill that an older Pinky
// installed with this Pinky's text, so agents read the guide of the Pinky
// they start without the person connecting them again. It changes only
// Pinky's own copies: a folder without one, or with someone else's
// SKILL.md, stays as it is, and a folder several agents read is written
// once. It returns the files it rewrote, and what went wrong with others.
func (s *Setup) RefreshSkills(ctx context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Skill == "" {
		return nil, nil
	}
	var done []string
	var errs []error
	seen := map[string]bool{}
	for _, a := range catalog {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		if a.skills == nil {
			continue
		}
		dir := a.skills(s.Env)
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		if readSkill(dir, s.Skill) != skillOld {
			continue
		}
		file := skillFile(dir)
		if err := writeFile(file, []byte(s.Skill)); err != nil {
			errs = append(errs, s.said(err))
			continue
		}
		done = append(done, file)
	}
	return done, errors.Join(errs...)
}

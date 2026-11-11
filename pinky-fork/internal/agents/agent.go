package agents

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
)

// agent is a kind of agent Pinky can add itself to.
type agent struct {
	id   string
	name func(e *Env) string
	url  string // where to get it
	// found reports whether it is on this computer.
	found func(e *Env) bool
	// places are the settings files that may hold its MCP servers, the one
	// Pinky adds itself to first: an entry in another one is changed where
	// it is, so that the agent never has two.
	places func(e *Env) []place
	// cli is its command for MCP servers, which writes the first place;
	// Pinky runs it, when it is there, in place of changing that file.
	cli *cli
	// skills is the folder of skills (SKILL.md in a folder each) it reads.
	// Several agents read ~/.agents/skills; Pinky's skill there stays
	// while one of them has Pinky.
	skills  func(e *Env) string
	restart RestartKind
	// bundle: without an entry, Connect opens Pinky's MCP bundle in the
	// agent's app, which asks the person to install it (Claude Desktop),
	// and extension finds it installed.
	bundle    *bundleApp
	extension func(s *Setup) (installed, enabled bool)
}

// place is a settings file and the way MCP servers sit in it.
type place struct {
	file  string
	store store
}

// errExtension says Pinky is an extension of the agent's app, which only
// the app removes.
var errExtension = errors.New("Pinky is an extension of this app: remove it in the app's settings")

// active is the place that holds Pinky's entry, or the one Pinky would
// add it to; a settings file Pinky cannot read stops it, since that file
// may hold the entry.
func (a *agent) active(e *Env) (p place, entry any, ok bool, err error) {
	places := a.places(e)
	if len(places) == 0 {
		return place{}, nil, false, nil
	}
	for _, pl := range places {
		entry, ok, err := pl.store.read(pl.file)
		if err != nil {
			return pl, nil, false, err
		}
		if ok {
			return pl, entry, true, nil
		}
	}
	return places[0], nil, false, nil
}

// connect adds Pinky's entry, or sets it right: with the agent's command
// when it has one, else in the file. The file is backed up either way.
func (a *agent) connect(ctx context.Context, s *Setup) (Result, error) {
	res := Result{Restart: a.restart}
	pl, entry, ok, err := a.active(s.Env)
	if err != nil {
		return res, err
	}
	if pl.file == "" {
		return res, fmt.Errorf("Pinky does not know where %s keeps its settings here", a.name(s.Env))
	}
	if !ok && a.bundle != nil {
		path, err := s.openBundle(ctx, a.bundle)
		res.Opened, res.Restart = path, RestartNone
		return res, err
	}
	if a.cli != nil && pl.file == a.places(s.Env)[0].file {
		if path := a.cli.find(s.Env); path != "" {
			res.Backups, err = a.connectWith(ctx, s, path, pl, entry, ok)
			return res, err
		}
	}
	res.Backups, err = s.change(a, pl.file, func(data []byte) ([]byte, error) { return pl.store.set(data, s.Server) })
	return res, err
}

// connectWith adds the entry with the agent's command: it removes an
// entry that is not right first, as most such commands refuse to add a
// name they have.
func (a *agent) connectWith(ctx context.Context, s *Setup, path string, pl place, entry any, had bool) ([]string, error) {
	var backups []string
	if data, existed, err := readFile(pl.file); err != nil {
		return nil, err
	} else if existed && len(data) > 0 {
		b, err := backup(s.backups(), a.id, pl.file, data, s.now())
		if err != nil {
			return nil, fmt.Errorf("Pinky did not change %s: it could not keep a copy of its settings: %w", a.name(s.Env), err)
		}
		backups = append(backups, b)
	}
	if had {
		cmd, args := commandOf(entry)
		if cmd == s.Server.Command && reflect.DeepEqual(args, s.Server.Args) {
			return backups, nil // already right
		}
		if err := a.cli.run(ctx, s.Env, path, a.cli.remove); err != nil {
			return backups, err
		}
	}
	if err := a.cli.run(ctx, s.Env, path, a.cli.add(s.Server)); err != nil {
		return backups, err
	}
	entry, ok, err := pl.store.read(pl.file)
	if err != nil {
		return backups, err
	}
	if cmd, args := commandOf(entry); !ok || cmd != s.Server.Command || !reflect.DeepEqual(args, s.Server.Args) {
		return backups, fmt.Errorf("%s: %w (%s)", filepath.Base(path), errNoEntry, pl.file)
	}
	return backups, nil
}

// remove takes Pinky's entries out, from every place that has one.
func (a *agent) remove(ctx context.Context, s *Setup) (Result, error) {
	res := Result{Restart: a.restart}
	places := a.places(s.Env)
	removed := false
	for i, pl := range places {
		_, ok, err := pl.store.read(pl.file)
		if err != nil {
			return res, err
		}
		if !ok {
			continue
		}
		removed = true
		if i == 0 && a.cli != nil {
			if path := a.cli.find(s.Env); path != "" {
				b, err := a.removeWith(ctx, s, path, pl)
				res.Backups = append(res.Backups, b...)
				if err != nil {
					return res, err
				}
				continue
			}
		}
		b, err := s.change(a, pl.file, func(data []byte) ([]byte, error) {
			out, _, err := pl.store.unset(data)
			return out, err
		})
		res.Backups = append(res.Backups, b...)
		if err != nil {
			return res, err
		}
	}
	if !removed && a.extension != nil {
		if installed, _ := a.extension(s); installed {
			return res, errExtension
		}
	}
	return res, nil
}

func (a *agent) removeWith(ctx context.Context, s *Setup, path string, pl place) ([]string, error) {
	var backups []string
	if data, existed, err := readFile(pl.file); err != nil {
		return nil, err
	} else if existed && len(data) > 0 {
		b, err := backup(s.backups(), a.id, pl.file, data, s.now())
		if err != nil {
			return nil, fmt.Errorf("Pinky did not change %s: it could not keep a copy of its settings: %w", a.name(s.Env), err)
		}
		backups = append(backups, b)
	}
	if err := a.cli.run(ctx, s.Env, path, a.cli.remove); err != nil {
		return backups, err
	}
	_, ok, err := pl.store.read(pl.file)
	if err != nil {
		return backups, err
	}
	if ok {
		return backups, fmt.Errorf("%s: %w (%s)", filepath.Base(path), errNoEntry, pl.file)
	}
	return backups, nil
}

package agents

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// store is a kind of settings file, and how Pinky's entry sits in it.
type store interface {
	// read returns Pinky's entry, decoded, and whether there is one.
	read(file string) (entry any, ok bool, err error)
	// set returns the file with Pinky's entry for srv.
	set(data []byte, srv Server) ([]byte, error)
	// unset returns the file without Pinky's entry.
	unset(data []byte) ([]byte, bool, error)
}

// jsonStore keeps MCP servers in a JSON file, in the object at path.
type jsonStore struct {
	path  []string
	entry func(Server) any
}

func (j jsonStore) read(file string) (any, bool, error) {
	v, ok, err := cached(file, func(data []byte) (any, error) { return readJSONC(data) })
	if err != nil || !ok {
		return nil, false, err
	}
	e, ok := lookup(v, append(append([]string(nil), j.path...), Name)...)
	return e, ok, nil
}

func (j jsonStore) set(data []byte, srv Server) ([]byte, error) {
	return setJSONMember(data, j.path, Name, j.entry(srv))
}

func (j jsonStore) unset(data []byte) ([]byte, bool, error) {
	return deleteJSONMember(data, j.path, Name)
}

// tomlStore keeps MCP servers in a TOML file, as tables under key.
type tomlStore struct{ key []string }

func (t tomlStore) read(file string) (any, bool, error) {
	v, ok, err := cached(file, func(data []byte) (any, error) {
		m, err := readTOML(data)
		return m, err
	})
	if err != nil || !ok {
		return nil, false, err
	}
	e, ok := lookup(v, append(append([]string(nil), t.key...), Name)...)
	return e, ok, nil
}

// readCache keeps what settings files read as, by path, while their
// contents stay: ~/.claude.json can be megabytes, and Settings reads the
// agents every few seconds.
var readCache = struct {
	sync.Mutex
	m map[string]cachedFile
}{m: map[string]cachedFile{}}

type cachedFile struct {
	data []byte
	v    any
	err  error
}

// cached reads a file with parse, or returns what it read as last while
// its contents are the same; ok is false for a missing file.
func cached(file string, parse func([]byte) (any, error)) (v any, ok bool, err error) {
	data, ok, err := readFile(file)
	if err != nil || !ok {
		return nil, false, err
	}
	readCache.Lock()
	c, hit := readCache.m[file]
	readCache.Unlock()
	if hit && bytes.Equal(c.data, data) {
		return c.v, true, c.err
	}
	v, err = parse(data)
	if err != nil {
		err = fmt.Errorf("%s: %w", file, err)
	}
	readCache.Lock()
	readCache.m[file] = cachedFile{data: data, v: v, err: err}
	readCache.Unlock()
	return v, true, err
}

func (t tomlStore) set(data []byte, srv Server) ([]byte, error) {
	return setTOMLServer(data, append(append([]string(nil), t.key...), Name), srv)
}

func (t tomlStore) unset(data []byte) ([]byte, bool, error) {
	return deleteTOMLServer(data, append(append([]string(nil), t.key...), Name))
}

// change rewrites an agent's settings file with fn, after a backup of the
// file as it was. A file fn leaves as it is is not written.
func (s *Setup) change(a *agent, file string, fn func([]byte) ([]byte, error)) ([]string, error) {
	data, existed, err := readFile(file)
	if err != nil {
		return nil, err
	}
	out, err := fn(data)
	if err != nil {
		return nil, fmt.Errorf("Pinky left %s as it is: %w", file, err)
	}
	if bytes.Equal(out, data) && existed {
		return nil, nil
	}
	var backups []string
	if existed && len(data) > 0 {
		b, err := backup(s.backups(), a.id, file, data, s.now())
		if err != nil {
			return nil, fmt.Errorf("Pinky left %s as it is: it could not keep a copy of it: %w", file, err)
		}
		backups = append(backups, b)
	}
	// The agent may have written the file meanwhile: write only over what
	// was read.
	if again, _, _ := readFile(file); !bytes.Equal(again, data) {
		return backups, fmt.Errorf("%s changed while Pinky read it; try again", file)
	}
	if err := writeFile(file, out); err != nil {
		return backups, err
	}
	return backups, nil
}

// cli is an agent's own command for its MCP servers.
type cli struct {
	name string
	// at lists more places than the PATH where the command may be.
	at func(e *Env) []string
	// add are the arguments that add srv; remove the ones that remove it.
	add    func(srv Server) []string
	remove []string
}

// find returns the command's path, or "".
func (c *cli) find(e *Env) string {
	if p := e.lookPath(c.name); p != "" {
		return p
	}
	if c.at != nil {
		for _, p := range c.at(e) {
			if exists(p) {
				return p
			}
		}
	}
	return ""
}

// run runs the command; its output goes into the error when it fails.
func (c *cli) run(ctx context.Context, e *Env, path string, args []string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := e.Run(ctx, path, args...)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 400 {
			msg = msg[:400] + "…"
		}
		if msg != "" {
			return fmt.Errorf("%s %s: %w: %s", c.name, strings.Join(args[:min(2, len(args))], " "), err, msg)
		}
		return fmt.Errorf("%s %s: %w", c.name, strings.Join(args[:min(2, len(args))], " "), err)
	}
	return nil
}

// errNoEntry says a change did not show in the agent's settings.
var errNoEntry = errors.New("the agent's settings do not show the change")

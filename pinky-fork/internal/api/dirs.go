// Package api is the gateway's local API: a Unix socket (a named pipe on
// Windows) that only the current user can reach. Each connection starts with a one-line hello that
// picks its role:
//
//   - "mcp": the rest of the connection is an MCP session (newline-delimited
//     JSON-RPC), served by the gateway's MCP server. `pinky mcp` is a thin
//     stdio shim that forwards to it. It must present the agent token.
//   - "control": JSON-RPC 2.0 for clients such as `pinky devices`. It can read
//     devices, tasks, needs and history, and stop. Approving, handing back
//     and unlocking need the in-process UI or a client that proves user
//     presence, so the socket refuses them (except for a gateway that runs
//     only fake devices, for tests).
//
// A lock file in the data folder keeps a second gateway for the same user
// from starting.
package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Dirs are Pinky's per-user directories.
type Dirs struct {
	Config string // rules, app table, agent token
	Data   string // history database, media, daemon log
	Run    string // socket
}

// HomeEnv overrides every directory with one base folder, for tests and
// side-by-side gateways: $PINKY_HOME/config, /data and /run.
const HomeEnv = "PINKY_HOME"

// DefaultDirs returns the directories for the current user.
func DefaultDirs() (Dirs, error) {
	if home := os.Getenv(HomeEnv); home != "" {
		abs, err := filepath.Abs(home)
		if err != nil {
			return Dirs{}, err
		}
		return Dirs{Config: filepath.Join(abs, "config"), Data: filepath.Join(abs, "data"), Run: filepath.Join(abs, "run")}, nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return Dirs{}, err
	}
	switch runtime.GOOS {
	case "darwin":
		base := filepath.Join(userHome, "Library", "Application Support", "Pinky")
		return Dirs{Config: base, Data: base, Run: base}, nil
	case "windows":
		cfg, err := os.UserConfigDir()
		if err != nil {
			return Dirs{}, err
		}
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			local = cfg
		}
		return Dirs{Config: filepath.Join(cfg, "Pinky"), Data: filepath.Join(local, "Pinky"), Run: filepath.Join(local, "Pinky")}, nil
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(userHome, ".config")
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(userHome, ".local", "share")
	}
	run := os.Getenv("XDG_RUNTIME_DIR")
	if run == "" {
		run = runtimeDir(userHome)
	}
	return Dirs{Config: filepath.Join(cfg, "pinky"), Data: filepath.Join(data, "pinky"), Run: filepath.Join(run, "pinky")}, nil
}

// runtimeBase is where systemd-logind makes each logged-in user's runtime
// folder, /run/user/<uid>; tests change it.
var runtimeBase = "/run/user"

// runtimeDir is the runtime folder of a Pinky started without
// XDG_RUNTIME_DIR (by a service or a program that clears it): the user's
// /run/user/<uid> when it is theirs alone, as a Pinky started in the
// desktop session finds it, so that both take the same lock and socket and
// a second gateway does not start beside the first on the same data;
// ~/.local/state on systems without one.
func runtimeDir(userHome string) string {
	dir := filepath.Join(runtimeBase, strconv.Itoa(os.Getuid()))
	if ownPrivateDir(dir) {
		return dir
	}
	return filepath.Join(userHome, ".local", "state")
}

// Socket is where the gateway listens: a Unix socket in the runtime folder,
// or on Windows a named pipe named after that folder. Packaged (MSIX) apps
// on Windows, such as the Claude and Codex desktop apps, see a virtualized
// copy of AppData, so the agents they start cannot reach a socket file
// there; named pipes are not virtualized.
func (d Dirs) Socket() string {
	if runtime.GOOS == "windows" {
		return pipeName(d.Run)
	}
	return d.unixSocket()
}

// unixSocket is the socket file in the runtime folder. Pinky on Windows
// listened there before it used a named pipe; clients still try it so that
// they reach an older gateway that is running.
func (d Dirs) unixSocket() string { return filepath.Join(d.Run, "pinky.sock") }

// pipeName is the named pipe for a runtime folder: one per folder, as with
// the socket, so that gateways with their own PINKY_HOME stay apart. Windows
// paths ignore case.
func pipeName(run string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(run))))
	return `\\.\pipe\pinky-` + hex.EncodeToString(sum[:8])
}

// socketPathLimit is the longest Unix socket path the system takes, in
// bytes: sun_path holds 108 bytes on Linux and 104 on macOS and the BSDs,
// and Go keeps room for the final NUL on both.
func socketPathLimit(goos string) int {
	if goos == "linux" {
		return 107
	}
	return 103
}

// PathTooLongError means the socket path does not fit in a Unix socket
// address, which the system reports only as "invalid argument".
type PathTooLongError struct {
	Path  string
	Limit int
}

func (e *PathTooLongError) Error() string {
	return fmt.Sprintf("the socket path %s is %d bytes, longer than the %d this system allows: set %s to a shorter folder", e.Path, len(e.Path), e.Limit, HomeEnv)
}

// checkSocketPath returns a PathTooLongError for a path too long to be a
// Unix socket.
func checkSocketPath(path string) error {
	if limit := socketPathLimit(runtime.GOOS); len(path) > limit {
		return &PathTooLongError{Path: path, Limit: limit}
	}
	return nil
}

// LockFile keeps one gateway per data folder. It lives with the data, not
// with the socket: a runtime folder can differ between starts (logind
// removes /run/user/<uid> at logout; a service may not have
// XDG_RUNTIME_DIR), and two gateways must not share one history and the
// same phones.
func (d Dirs) LockFile() string { return filepath.Join(d.Data, "pinky.lock") }

// TokenFile holds the agent token, readable only by the user.
func (d Dirs) TokenFile() string { return filepath.Join(d.Config, "agent-token") }

// Rules is the policy rules file.
func (d Dirs) Rules() string { return filepath.Join(d.Config, "rules.yaml") }

// Apps is the app category overrides file.
func (d Dirs) Apps() string { return filepath.Join(d.Config, "apps.yaml") }

// DaemonLog is where a daemon started by `pinky mcp` writes.
func (d Dirs) DaemonLog() string { return filepath.Join(d.Data, "daemon.log") }

// Ensure creates the directories, private to the user.
func (d Dirs) Ensure() error {
	for _, dir := range []string{d.Config, d.Data, d.Run} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if runtime.GOOS != "windows" {
			if err := os.Chmod(dir, 0o700); err != nil {
				return err
			}
		}
	}
	return nil
}

// EnsureToken returns the agent token, creating it (mode 0600) when missing.
// The gateway and agents' `pinky mcp` may call it at the same time on a
// first run, so the file appears with its whole token at once, and every
// caller gets the same one.
func EnsureToken(path string) (string, error) {
	tok, err := readToken(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return tok, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	tok = hex.EncodeToString(b[:])
	switch err := placeToken(path, tok); {
	case err == nil:
		return tok, nil
	case errors.Is(err, fs.ErrExist):
		// Another process won the race; use its token.
		return readToken(path)
	default:
		return "", err
	}
}

// readToken reads the agent token. A file still empty is being written by
// another process (where placeToken cannot link), so it waits a moment
// for the token.
func readToken(path string) (string, error) {
	for wait := 0; ; wait++ {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if tok := strings.TrimSpace(string(b)); tok != "" {
			if runtime.GOOS != "windows" {
				if st, err := os.Stat(path); err == nil && st.Mode().Perm()&0o077 != 0 {
					if err := os.Chmod(path, 0o600); err != nil {
						return "", fmt.Errorf("agent token %s is readable by others and cannot be fixed: %w", path, err)
					}
				}
			}
			return tok, nil
		}
		if wait == 50 {
			return "", fmt.Errorf("agent token %s is empty; delete it and restart Pinky", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// placeToken creates the token file holding tok, or fails with
// fs.ErrExist. It writes a temporary file and links it into place, so
// nobody reads a half-written token. Where links do not work, it creates
// the file and then writes it, and readers wait for the token.
func placeToken(path, tok string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agent-token-*")
	if err != nil {
		return err
	}
	_, err = tmp.WriteString(tok + "\n")
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Link(tmp.Name(), path)
	}
	os.Remove(tmp.Name())
	if err == nil || errors.Is(err, fs.ErrExist) {
		return err
	}
	var le *os.LinkError
	if !errors.As(err, &le) {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(tok + "\n"); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

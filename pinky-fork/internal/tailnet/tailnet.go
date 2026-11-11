// Package tailnet reads what Tailscale on this computer knows about its
// tailnet, the private network of the person's own devices that Tailscale
// keeps reachable wherever each of them is: whether Tailscale runs here,
// and the devices on the tailnet with their addresses. It runs Tailscale's
// command line (`tailscale status --json`), so it needs no credentials of
// its own and works with every kind of install: the Mac app from the
// website or the App Store, Homebrew's, the Linux packages and Windows'.
package tailnet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

// State is Tailscale's on this computer.
type State string

// States, from the least ready to the most.
const (
	// NotInstalled: no Tailscale command line was found.
	NotInstalled State = "not_installed"
	// NotRunning: Tailscale is installed, but its service does not answer,
	// as when the Mac app is not open.
	NotRunning State = "not_running"
	// NeedsLogin: Tailscale runs but is not signed in, or its key expired.
	NeedsLogin State = "needs_login"
	// Stopped: signed in, but turned off (Disconnected in the app).
	Stopped State = "stopped"
	// Starting: turned on, not yet connected.
	Starting State = "starting"
	// Running: connected to the tailnet.
	Running State = "running"
)

// Status is what Tailscale on this computer reports.
type Status struct {
	State State
	// Detail says what is wrong in Tailscale's own words, when it is not
	// running: the command's error, or the first health warning.
	Detail string
	// Tailnet is the tailnet's name, such as "example.com" or
	// "someone@gmail.com".
	Tailnet string
	// Self is this computer on the tailnet; Peers the other devices.
	Self  Device
	Peers []Device
	// Command is the Tailscale command line that was run.
	Command string
}

// Device is a device on the tailnet.
type Device struct {
	ID string // Tailscale's stable node id
	// Name is the device's host name, such as "pixel-9" or "Mi 10 Pro".
	Name string
	// DNSName is its MagicDNS name without the final dot, such as
	// "pixel-9.tail1234.ts.net"; "" without MagicDNS.
	DNSName string
	// OS is Tailscale's: "android", "iOS", "macOS", "linux", "windows".
	OS string
	// Addrs are its Tailscale addresses, IPv4 first.
	Addrs []netip.Addr
	// Online reports whether the coordination server sees it connected.
	Online   bool
	LastSeen time.Time
	// Active reports whether traffic flowed between it and this computer
	// lately. Direct is the address this computer reaches it at without a
	// relay, "" while the traffic goes through a relay (or none flows);
	// Relay is the nearest relay's region code, such as "sin".
	Active bool
	Direct string
	Relay  string
	// RxBytes and TxBytes count what this computer received from it and
	// sent to it since Tailscale started here.
	RxBytes, TxBytes int64
}

// IPv4 returns the device's Tailscale IPv4 address, the zero Addr when it
// has none.
func (d Device) IPv4() netip.Addr {
	for _, a := range d.Addrs {
		if a.Is4() {
			return a
		}
	}
	return netip.Addr{}
}

// Relayed reports whether the traffic with the device goes through one of
// Tailscale's relay servers rather than straight to it.
func (d Device) Relayed() bool { return d.Active && d.Direct == "" }

// Android reports whether the device is an Android phone or tablet.
func (d Device) Android() bool { return strings.EqualFold(d.OS, "android") }

// Has reports whether addr is one of the device's addresses.
func (d Device) Has(addr netip.Addr) bool {
	return addr.IsValid() && slices.Contains(d.Addrs, addr.Unmap())
}

// Running reports whether Tailscale is connected to the tailnet here.
func (s Status) Running() bool { return s.State == Running }

// Device returns the peer with this node id or address, and whether there
// is one.
func (s Status) Device(idOrAddr string) (Device, bool) {
	addr, _ := netip.ParseAddr(idOrAddr)
	for _, d := range s.Peers {
		if d.ID == idOrAddr || d.Has(addr) {
			return d, true
		}
	}
	return Device{}, false
}

// AndroidPeers returns the Android devices on the tailnet, online first,
// then by name.
func (s Status) AndroidPeers() []Device {
	var out []Device
	for _, d := range s.Peers {
		if d.Android() && d.IPv4().IsValid() {
			out = append(out, d)
		}
	}
	slices.SortStableFunc(out, func(a, b Device) int {
		if a.Online != b.Online {
			if a.Online {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out
}

// Tailscale's address ranges: 100.64.0.0/10 (shared with carrier-grade
// NAT, which Tailscale takes over on the devices that run it) and its own
// IPv6 range.
var (
	cgnat = netip.MustParsePrefix("100.64.0.0/10")
	ula   = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// Contains reports whether addr is in Tailscale's address ranges.
func Contains(addr netip.Addr) bool {
	addr = addr.Unmap()
	return cgnat.Contains(addr) || ula.Contains(addr)
}

// ErrNotInstalled is returned by Read when there is no Tailscale command
// line on this computer.
var ErrNotInstalled = errors.New("tailscale is not installed on this computer")

// Reader reads Tailscale's status, keeping the last answer for a while so
// that callers that ask often (a page redrawn every second, a reconnect
// loop) run the command at most once per MaxAge.
type Reader struct {
	// MaxAge is how long an answer is kept; zero means 3 s.
	MaxAge time.Duration
	// Run runs the command line; nil runs it for real. Tests replace it.
	Run func(ctx context.Context, path string, args ...string) ([]byte, error)
	// Find returns the command line's path; nil looks in the usual places.
	Find func() (string, error)

	mu   sync.Mutex
	at   time.Time
	last Status
	err  error
	busy chan struct{} // closed when the read in flight ends
}

// Default is the Reader everyone shares.
var Default = &Reader{}

// Read returns Tailscale's status from Default.
func Read(ctx context.Context) (Status, error) { return Default.Status(ctx) }

// Status returns Tailscale's status: State NotInstalled (with
// ErrNotInstalled) when there is no Tailscale here, NotRunning when its
// service does not answer, else what it reports. The error is only for
// a failure to read: a Tailscale that is off is a Status, not an error.
func (r *Reader) Status(ctx context.Context) (Status, error) {
	for {
		r.mu.Lock()
		maxAge := r.MaxAge
		if maxAge <= 0 {
			maxAge = 3 * time.Second
		}
		if !r.at.IsZero() && time.Since(r.at) < maxAge {
			st, err := r.last, r.err
			r.mu.Unlock()
			return st, err
		}
		if r.busy == nil {
			done := make(chan struct{})
			r.busy = done
			r.mu.Unlock()
			st, err := r.read(ctx)
			r.mu.Lock()
			if ctx.Err() == nil {
				r.at, r.last, r.err = time.Now(), st, err
			}
			r.busy = nil
			close(done)
			r.mu.Unlock()
			return st, err
		}
		wait := r.busy
		r.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return Status{}, ctx.Err()
		}
	}
}

// Forget drops the kept answer, so the next Status reads again (after
// the person turned Tailscale on, say).
func (r *Reader) Forget() {
	r.mu.Lock()
	r.at = time.Time{}
	r.mu.Unlock()
}

func (r *Reader) read(ctx context.Context) (Status, error) {
	find := r.Find
	if find == nil {
		find = findCommand
	}
	path, err := find()
	if err != nil {
		return Status{State: NotInstalled}, err
	}
	run := r.Run
	if run == nil {
		run = runCommand
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, err := run(ctx, path, "status", "--json")
	st, perr := Parse(out)
	st.Command = path
	if perr == nil {
		// Tailscale prints the status, and exits with an error, when it
		// is stopped or signed out on some systems.
		return st, nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return Status{State: NotRunning, Command: path, Detail: "tailscale status did not answer"}, nil
		}
		return Status{State: NotRunning, Command: path, Detail: commandError(err, out)}, nil
	}
	return Status{Command: path}, fmt.Errorf("tailscale status: %w", perr)
}

// commandError is a failed command's error with what it printed, such as
// "failed to connect to local Tailscale service; is Tailscale running?".
func commandError(err error, out []byte) string {
	msg := strings.TrimSpace(string(out))
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		msg = strings.TrimSpace(string(ee.Stderr))
	}
	if msg == "" {
		return err.Error()
	}
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	return msg
}

// rawStatus is the part of `tailscale status --json` (ipnstate.Status)
// Pinky reads.
type rawStatus struct {
	BackendState   string
	Health         []string
	Self           *rawPeer
	Peer           map[string]*rawPeer
	MagicDNSSuffix string
	CurrentTailnet *struct {
		Name            string
		MagicDNSSuffix  string
		MagicDNSEnabled bool
	}
}

type rawPeer struct {
	ID           string
	HostName     string
	DNSName      string
	OS           string
	TailscaleIPs []string
	Online       bool
	LastSeen     time.Time
	Active       bool
	CurAddr      string
	Relay        string
	RxBytes      int64
	TxBytes      int64
}

// Parse reads the output of `tailscale status --json`.
func Parse(b []byte) (Status, error) {
	var raw rawStatus
	if err := json.Unmarshal(b, &raw); err != nil {
		return Status{}, err
	}
	if raw.BackendState == "" {
		return Status{}, errors.New("no backend state")
	}
	st := Status{State: backendState(raw.BackendState)}
	if raw.CurrentTailnet != nil {
		st.Tailnet = raw.CurrentTailnet.Name
	}
	if raw.Self != nil {
		st.Self = raw.Self.device()
	}
	for _, p := range raw.Peer {
		if p != nil {
			st.Peers = append(st.Peers, p.device())
		}
	}
	slices.SortFunc(st.Peers, func(a, b Device) int { return strings.Compare(a.ID, b.ID) })
	if st.State != Running {
		for _, h := range raw.Health {
			if h = strings.TrimSpace(h); h != "" {
				st.Detail = h
				break
			}
		}
	}
	return st, nil
}

// backendState maps ipn.State's names to States.
func backendState(s string) State {
	switch s {
	case "Running":
		return Running
	case "Stopped":
		return Stopped
	case "Starting":
		return Starting
	case "NeedsLogin", "NeedsMachineAuth":
		return NeedsLogin
	}
	// "NoState" and "InUseOtherUser": the service answers but cannot be
	// used yet, or is another user's.
	return NotRunning
}

func (p *rawPeer) device() Device {
	d := Device{
		ID: p.ID, Name: p.HostName, DNSName: strings.TrimSuffix(p.DNSName, "."), OS: p.OS,
		Online: p.Online, LastSeen: p.LastSeen, Active: p.Active, Direct: p.CurAddr, Relay: p.Relay,
		RxBytes: p.RxBytes, TxBytes: p.TxBytes,
	}
	for _, s := range p.TailscaleIPs {
		if a, err := netip.ParseAddr(s); err == nil {
			d.Addrs = append(d.Addrs, a.Unmap())
		}
	}
	slices.SortStableFunc(d.Addrs, func(a, b netip.Addr) int {
		switch {
		case a.Is4() == b.Is4():
			return 0
		case a.Is4():
			return -1
		}
		return 1
	})
	return d
}

func runCommand(ctx context.Context, path string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	if runtime.GOOS == "darwin" && os.Getenv("TERM") == "" {
		// The Mac app is also the CLI. Without a terminal environment (as
		// when Pinky starts from Finder), it can try to launch its GUI and
		// print "The Tailscale GUI failed to start" instead of JSON.
		cmd.Env = append(cmd.Environ(), "TERM=dumb")
	}
	hideWindow(cmd)
	return cmd.Output()
}

// findCommand returns Tailscale's command line: tailscale on PATH, else
// where the installers put it. An app started from the Finder or the
// Start menu has a short PATH, and the Mac app keeps its command line
// inside the app.
func findCommand() (string, error) {
	return find(runtime.GOOS, os.Getenv, exec.LookPath, isFile)
}

func find(goos string, getenv func(string) string, lookPath func(string) (string, error), isFile func(string) bool) (string, error) {
	if p, err := lookPath("tailscale"); err == nil {
		return p, nil
	}
	for _, p := range candidates(goos, getenv) {
		if isFile(p) {
			return p, nil
		}
	}
	return "", ErrNotInstalled
}

// candidates are where Tailscale's installers put its command line.
func candidates(goos string, getenv func(string) string) []string {
	switch goos {
	case "darwin":
		return []string{
			// The app (standalone or App Store) is also the command line.
			"/Applications/Tailscale.app/Contents/MacOS/Tailscale",
			"/usr/local/bin/tailscale",    // the app's "Install CLI", or the open-source tailscaled
			"/opt/homebrew/bin/tailscale", // Homebrew on Apple silicon
		}
	case "windows":
		var out []string
		for _, env := range []string{"ProgramFiles", "ProgramW6432", "ProgramFiles(x86)"} {
			if dir := getenv(env); dir != "" {
				out = append(out, filepath.Join(dir, "Tailscale", "tailscale.exe"))
			}
		}
		return append(out, `C:\Program Files\Tailscale\tailscale.exe`)
	}
	return []string{"/usr/bin/tailscale", "/usr/sbin/tailscale", "/usr/local/bin/tailscale", "/snap/bin/tailscale"}
}

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

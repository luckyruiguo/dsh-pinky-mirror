package android

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

// Is it the same phone? (Config.VerifyWiFi.) Anything on the network can
// answer as a phone: a connection that Wireless debugging's discovery
// makes goes to whatever advertises the phone's mDNS name, one by address
// to whatever has the address now, and adb reconnects a dropped connection
// made by address in place, under the same serial and transport id, to
// whatever answers there. adb authenticates the computer to the phone, not
// the phone to the computer, and adbd makes a new TLS key each time it
// starts, so the phone's certificate tells nothing either
// (docs/notes/android.md, "Is it the same phone?").
//
// So Pinky leaves a mark on each phone it trusts: 32 random bytes in a
// file that only adb's shell user may read, /data/local/tmp/.pinky-id-<host>
// (<host> is this computer's, so that two computers keep a mark each), and
// keeps their hash with the phone's id (Config.IdentityFile). A connection
// over the network is the phone when the file it serves has those bytes. A
// device that only claims the phone's serial cannot know them: the phone's
// apps cannot read the file, and only a computer the phone trusts with adb
// can.
//
// Pinky trusts, and marks, a phone on a cable, an emulator and a
// connection on this computer, once the gateway says the phone is the
// person's (UseOverWiFi); a phone the person paired or connected in Pinky;
// one the person says is theirs (TrustPhone); and the first time, a phone
// Pinky knew before this check came in. Any other phone on the network
// with no record waits for the person to say it is theirs
// (driver.SetupConfirmWiFi); one without its mark is refused
// (driver.SetupPhoneChanged) until they trust it or plug it in.
//
// List and Open check a connection once, and a device's adb client
// (adb.Client.Guard) before each request, so that a connection that adb
// made again in place is checked again before anything reaches it: a
// watch on the server's device list (idWatch) sees the connection go and
// come back.

const (
	markDir  = "/data/local/tmp/"
	markName = ".pinky-id-"
	markLen  = 32
	// markMax bounds what is read of a mark file; a longer one is not a
	// mark of Pinky's.
	markMax = 4096
)

// idVerdict is what a check found about a connection over the network.
type idVerdict uint8

const (
	idSame    idVerdict = iota + 1 // it has the phone's mark
	idUnknown                      // Pinky has no record of the phone
	idChanged                      // Pinky has one, and it lacks the mark
)

// --- the records ---

// idStore keeps the phones' marks in a file, or in memory when it has no
// path.
type idStore struct {
	path string
	logf func(string, ...any)
	now  func() time.Time

	mu     sync.Mutex
	loaded bool
	file   idFile
}

type idFile struct {
	// Host names this computer's mark files: random, made once.
	Host   string              `json:"host"`
	Phones map[string]idRecord `json:"phones"`
}

type idRecord struct {
	Mark  string    `json:"mark"` // the SHA-256 of the mark, in hex
	Since time.Time `json:"since"`
}

func (s *idStore) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.file.Phones = map[string]idRecord{}
	if s.path == "" {
		return
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.logf("android: reading the phones' identity marks: %v", err)
		}
		return
	}
	var f idFile
	if err := json.Unmarshal(b, &f); err != nil {
		// Start again: the phones Pinky knows are marked anew on first
		// use (UseOverWiFi), and the others wait for the person.
		s.logf("android: reading %s: %v; starting a new one", s.path, err)
		return
	}
	s.file.Host = f.Host
	for id, r := range f.Phones {
		if id != "" && len(r.Mark) == 2*sha256.Size {
			s.file.Phones[id] = r
		}
	}
}

func (s *idStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(s.file, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// host returns this computer's name for its mark files, making it the
// first time.
func (s *idStore) host() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	if validHost(s.file.Host) {
		return s.file.Host, nil
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	s.file.Host = hex.EncodeToString(b[:])
	return s.file.Host, s.saveLocked()
}

// validHost reports whether a host name is safe in a file name on the
// phone.
func validHost(h string) bool {
	if len(h) < 8 || len(h) > 64 {
		return false
	}
	for _, c := range h {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (s *idStore) get(id string) (idRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	r, ok := s.file.Phones[id]
	return r, ok
}

// set records a phone's mark; it reports whether the record changed.
func (s *idStore) set(id string, mark []byte) (bool, error) {
	sum := sha256.Sum256(mark)
	h := hex.EncodeToString(sum[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	if r, ok := s.file.Phones[id]; ok && r.Mark == h {
		return false, nil
	}
	s.file.Phones[id] = idRecord{Mark: h, Since: s.now().UTC().Truncate(time.Second)}
	return true, s.saveLocked()
}

// matches reports whether mark is the one recorded for a phone.
func (r idRecord) matches(mark []byte) bool {
	if len(mark) != markLen {
		return false
	}
	sum := sha256.Sum256(mark)
	want, err := hex.DecodeString(r.Mark)
	return err == nil && subtle.ConstantTimeCompare(sum[:], want) == 1
}

// --- the checks ---

// identity checks the phones reached over the network.
type identity struct {
	p     *Provider
	store *idStore
	watch *idWatch
	// readMark and writeMark reach the mark file on the phone over the
	// adb server, without the guard; tests replace them. readMark reports
	// whether the file is there.
	readMark  func(ctx context.Context, serial, path string) ([]byte, bool, error)
	writeMark func(ctx context.Context, serial, path string, mark []byte) error

	markMu sync.Mutex // one mark written at a time

	mu      sync.Mutex
	known   map[string]bool    // the person's phones, by id (UseOverWiFi)
	checks  map[string]idCheck // the last check of each connection, by serial
	marked  map[string]bool    // the trusted connections marked, by serial#transport
	flights map[string]*idFlight
	noted   map[string]string // the last thing logged about a connection
}

// idCheck is what a check found about a connection, valid while the
// connection stays (idStamp).
type idCheck struct {
	transport string
	stamp     idStamp
	id        string
	verdict   idVerdict
}

// idFlight is a guard's check under way, which the others for the same
// connection wait for.
type idFlight struct {
	want string
	done chan struct{}
	err  error
}

func newIdentity(p *Provider) *identity {
	s := &identity{
		p:       p,
		store:   &idStore{path: p.cfg.IdentityFile, logf: p.cfg.logf, now: time.Now},
		known:   map[string]bool{},
		checks:  map[string]idCheck{},
		marked:  map[string]bool{},
		flights: map[string]*idFlight{},
		noted:   map[string]string{},
	}
	s.readMark = s.readMarkADB
	s.writeMark = s.writeMarkADB
	s.watch = &idWatch{c: p.cfg.Client, gone: s.gone}
	return s
}

// trusted reports whether a connection needs no check: a cable's, an
// emulator's, or one on this computer.
func trusted(e adb.DeviceEntry) bool {
	if !e.Wireless() {
		return true
	}
	h := serialHost(e.Serial)
	if h == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(h)
	return err == nil && a.IsLoopback()
}

// use says a phone is the person's (driver.WiFiUser).
func (s *identity) use(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	if !s.known[id] {
		s.known[id] = true
		s.dropChecksLocked(id)
	}
	s.mu.Unlock()
}

func (s *identity) isKnown(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.known[id]
}

func (s *identity) dropChecksLocked(id string) {
	for serial, c := range s.checks {
		if c.id == id {
			delete(s.checks, serial)
		}
	}
}

// gone forgets what was found about a connection that went away or
// changed: adb may make it again to another device under the same serial.
func (s *identity) gone(serial string) {
	s.mu.Lock()
	delete(s.checks, serial)
	for k := range s.marked {
		if strings.HasPrefix(k, serial+"#") {
			delete(s.marked, k)
		}
	}
	s.mu.Unlock()
	s.p.dropProps(serial)
}

// verify checks a connection over the network to the phone listed under
// id: once while it stays, at once after it was made again. A phone Pinky
// knew before this check came in is marked at its first check.
func (s *identity) verify(ctx context.Context, e adb.DeviceEntry, id string) (idVerdict, error) {
	st, live := s.watch.stamp(e.Serial) // before looking at the phone
	s.mu.Lock()
	c, ok := s.checks[e.Serial]
	s.mu.Unlock()
	if ok && live && c.transport == e.TransportID && c.stamp == st && c.id == id {
		return c.verdict, nil
	}
	v, err := s.check(ctx, e.Serial, id)
	if err != nil {
		return 0, err
	}
	if live {
		s.mu.Lock()
		s.checks[e.Serial] = idCheck{transport: e.TransportID, stamp: st, id: id, verdict: v}
		s.mu.Unlock()
	}
	return v, nil
}

func (s *identity) check(ctx context.Context, serial, id string) (idVerdict, error) {
	rec, ok := s.store.get(id)
	if !ok {
		if !s.isKnown(id) {
			return idUnknown, nil
		}
		// Known before Pinky checked phones: this first check marks it.
		if err := s.mark(ctx, serial, id); err != nil {
			return 0, err
		}
		s.note(serial, fmt.Sprintf("android: %s at %s: marked as the phone Pinky knows (first check)", id, serial))
		return idSame, nil
	}
	host, err := s.store.host()
	if err != nil {
		return 0, err
	}
	mark, _, err := s.readMark(ctx, serial, markDir+markName+host)
	if err != nil {
		return 0, err
	}
	if !rec.matches(mark) {
		s.note(serial, fmt.Sprintf("android: %s at %s lacks the mark Pinky left on that phone: refused until the person trusts it or plugs it in", id, serial))
		return idChanged, nil
	}
	return idSame, nil
}

// mark leaves Pinky's mark on a phone it trusts, under its id: it keeps
// the mark the phone has, and writes a new one when it has none.
func (s *identity) mark(ctx context.Context, serial, id string) error {
	if id == "" {
		return errors.New("android: no phone id to mark")
	}
	s.markMu.Lock()
	defer s.markMu.Unlock()
	host, err := s.store.host()
	if err != nil {
		return fmt.Errorf("android: identity marks: %w", err)
	}
	path := markDir + markName + host
	mark, there, err := s.readMark(ctx, serial, path)
	if err != nil || !there || len(mark) != markLen {
		// None, or not one of Pinky's (a file root left there, say): a
		// new one.
		mark = make([]byte, markLen)
		if _, err := rand.Read(mark); err != nil {
			return err
		}
		if err := s.writeMark(ctx, serial, path, mark); err != nil {
			return fmt.Errorf("android: %s: writing Pinky's mark: %w", id, wrapErr(err))
		}
		got, there, err := s.readMark(ctx, serial, path)
		if err != nil {
			return fmt.Errorf("android: %s: reading Pinky's mark back: %w", id, wrapErr(err))
		}
		if !there || !bytes.Equal(got, mark) {
			return fmt.Errorf("android: %s: Pinky's mark did not stay on the phone", id)
		}
	}
	changed, err := s.store.set(id, mark)
	if err != nil {
		s.p.cfg.logf("android: saving the identity of %s: %v", id, err)
	}
	if changed {
		s.p.cfg.logf("android: %s: marked at %s", id, serial)
	}
	s.mu.Lock()
	s.dropChecksLocked(id)
	s.mu.Unlock()
	return nil
}

// markTrusted marks a phone on a trusted connection once per connection.
func (s *identity) markTrusted(ctx context.Context, e adb.DeviceEntry, id string) {
	key := e.Serial + "#" + e.TransportID
	s.mu.Lock()
	done := s.marked[key]
	s.mu.Unlock()
	if done {
		return
	}
	if err := s.mark(ctx, e.Serial, id); err != nil {
		s.note(e.Serial, fmt.Sprintf("android: %s: %v", id, err))
		return
	}
	s.mu.Lock()
	s.marked[key] = true
	s.mu.Unlock()
}

// note logs a message about a connection when it differs from the last.
func (s *identity) note(serial, msg string) {
	s.mu.Lock()
	same := s.noted[serial] == msg
	s.noted[serial] = msg
	s.mu.Unlock()
	if !same {
		s.p.cfg.logf("%s", msg)
	}
}

// apply checks the connections a List found and sets what it lists
// about those over the network that are not the phone (or not yet known
// to be): it marks the person's phones on trusted connections first.
func (s *identity) apply(ctx context.Context, entries []adb.DeviceEntry, infos []driver.Info, props []*deviceProps) {
	var wg sync.WaitGroup
	for i, e := range entries {
		if props[i] != nil && e.State == adb.StateDevice && trusted(e) && s.isKnown(infos[i].ID) {
			wg.Go(func() {
				mctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				s.markTrusted(mctx, e, infos[i].ID)
			})
		}
	}
	wg.Wait()
	for i, e := range entries {
		if props[i] == nil || e.State != adb.StateDevice || trusted(e) {
			continue
		}
		wg.Go(func() {
			vctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			v, err := s.verify(vctx, e, infos[i].ID)
			setVerdict(&infos[i], v, err)
		})
	}
	wg.Wait()
}

// setVerdict lists a connection over the network as the check found it.
func setVerdict(info *driver.Info, v idVerdict, err error) {
	switch {
	case err != nil:
		info.State = driver.StateOffline
		info.Detail = "Pinky could not check that this is the phone it set up: " + err.Error()
	case v == idUnknown:
		info.State = driver.StateNeedsSetup
		info.Setup = driver.SetupConfirmWiFi
		info.Detail = "Pinky has not used this phone before: say whether it is yours."
	case v == idChanged:
		info.State = driver.StateNeedsSetup
		info.Setup = driver.SetupPhoneChanged
		info.Detail = "This is not the phone Pinky set up under this name: it lacks the mark Pinky left on it. Trust it if you reset it or paired it again, or plug it in."
	}
}

// guard is the device's adb.Client.Guard: a request reaches a connection
// over the network only while it is the phone want, as checked since adb
// last made it.
func (s *identity) guard(ctx context.Context, serial, want string) error {
	for {
		if s.passes(serial, want) {
			return nil
		}
		s.mu.Lock()
		f := s.flights[serial]
		if f == nil {
			f = &idFlight{want: want, done: make(chan struct{})}
			s.flights[serial] = f
			s.mu.Unlock()
			f.err = s.checkNow(ctx, serial, want)
			s.mu.Lock()
			delete(s.flights, serial)
			s.mu.Unlock()
			close(f.done)
			return f.err
		}
		s.mu.Unlock()
		select {
		case <-f.done:
			// The same check, unless its caller gave up: then this one
			// checks.
			if f.want == want && !isCtxErr(f.err) {
				return f.err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func isCtxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// passes reports whether a connection was found to be the phone want
// since adb last made it.
func (s *identity) passes(serial, want string) bool {
	e, ok := s.watch.entry(serial)
	if !ok || e.State != adb.StateDevice {
		return false
	}
	if trusted(e) {
		return true
	}
	st, live := s.watch.stamp(serial)
	s.mu.Lock()
	c, ok := s.checks[serial]
	s.mu.Unlock()
	return ok && live && c.transport == e.TransportID && c.stamp == st && c.id == want && c.verdict == idSame
}

// checkNow checks a connection for the guard.
func (s *identity) checkNow(ctx context.Context, serial, want string) error {
	entries, err := s.p.devices(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(entries, func(e adb.DeviceEntry) bool { return e.Serial == serial })
	if i < 0 {
		return fmt.Errorf("adb: device %q: %w", serial, adb.ErrDeviceNotFound)
	}
	e := entries[i]
	if e.State != adb.StateDevice {
		return fmt.Errorf("adb: device %q is %s: %w", serial, e.State, adb.ErrDeviceOffline)
	}
	if trusted(e) {
		return nil
	}
	pr, err := s.p.deviceProps(ctx, e)
	if err != nil {
		return err
	}
	if id := s.p.phoneID(e, pr); id != want {
		return fmt.Errorf("android: the connection at %s is now %s, not %s: %w", serial, id, want, driver.ErrOffline)
	}
	v, err := s.verify(ctx, e, want)
	if err != nil {
		return err
	}
	if v != idSame {
		return fmt.Errorf("android: the phone at %s is not the phone Pinky set up as %s: %w", serial, want, driver.ErrOffline)
	}
	return nil
}

// --- the phone's side ---

// readMarkADB reads the mark file: no more than markMax bytes. A file
// that is not there is no error.
func (s *identity) readMarkADB(ctx context.Context, serial, path string) ([]byte, bool, error) {
	c := s.p.cfg.Client
	var w cappedBuffer
	_, err := c.Pull(ctx, serial, path, &w)
	if err == nil {
		return w.b, true, nil
	}
	var ae *adb.Error
	if !errors.As(err, &ae) || errors.Is(err, adb.ErrDeviceNotFound) || errors.Is(err, adb.ErrDeviceOffline) ||
		errors.Is(err, adb.ErrUnauthorized) {
		return nil, false, err
	}
	// The phone refused: no such file, or one Pinky may not read.
	fi, serr := c.Stat(ctx, serial, path)
	if serr != nil {
		return nil, false, serr
	}
	if !fi.Exists() {
		return nil, false, nil
	}
	return nil, true, err
}

// writeMarkADB writes the mark file, readable by adb's shell user only.
// adbd gives a pushed file the owner's permissions for everyone too (0600
// arrives as 0666), so a chmod follows the push.
func (s *identity) writeMarkADB(ctx context.Context, serial, path string, mark []byte) error {
	c := s.p.cfg.Client
	q := adb.Quote(path)
	if _, err := c.Shell(ctx, serial, "rm -f "+q); err != nil {
		return err
	}
	if err := c.Push(ctx, serial, bytes.NewReader(mark), path, 0o600, time.Now()); err != nil {
		return err
	}
	out, err := c.Shell(ctx, serial, "chmod 600 "+q)
	if err == nil && len(bytes.TrimSpace(out)) > 0 {
		err = fmt.Errorf("chmod: %s", bytes.TrimSpace(out))
	}
	if err != nil {
		c.Shell(ctx, serial, "rm -f "+q)
	}
	return err
}

// cappedBuffer keeps the first markMax bytes written to it.
type cappedBuffer struct{ b []byte }

func (w *cappedBuffer) Write(p []byte) (int, error) {
	if room := markMax - len(w.b); room > 0 {
		w.b = append(w.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// --- watching the server's connections ---

// idWatch follows the adb server's device list, so that a check of a
// connection holds only while the connection stays: adb reconnects a
// connection made by address in place, under the same serial and
// transport id, and the list shows it offline meanwhile (the server
// tells its trackers of every change of state before it reconnects).
type idWatch struct {
	c    *adb.Client
	gone func(serial string) // a connection left the list or changed

	mu      sync.Mutex
	running bool
	started time.Time
	live    bool   // the watch has the server's list now
	gen     uint64 // counts the watch's starts
	epochs  map[string]uint64
	entries map[string]adb.DeviceEntry
}

// idStamp is a connection's place in the watch's history: it differs
// after the watch started again, or the connection went and came back.
type idStamp struct{ gen, epoch uint64 }

// stamp returns a connection's stamp, and whether the watch follows the
// server now; it starts the watch.
func (w *idWatch) stamp(serial string) (idStamp, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.startLocked()
	return idStamp{gen: w.gen, epoch: w.epochs[serial]}, w.live
}

// entry returns a connection as the watch saw it last.
func (w *idWatch) entry(serial string) (adb.DeviceEntry, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.startLocked()
	if !w.live {
		return adb.DeviceEntry{}, false
	}
	e, ok := w.entries[serial]
	return e, ok
}

// watchRetry is how long the watch waits before it starts again.
var watchRetry = time.Second

func (w *idWatch) startLocked() {
	if w.running || time.Since(w.started) < watchRetry {
		return
	}
	w.running = true
	w.started = time.Now()
	go w.run()
}

func (w *idWatch) run() {
	first := true
	_ = w.c.TrackDevices(context.Background(), func(es []adb.DeviceEntry) {
		w.update(es, first)
		first = false
	})
	// The server went away or restarted: the next check starts the watch
	// again, and the checks made before then do not count.
	w.mu.Lock()
	w.live = false
	w.running = false
	w.mu.Unlock()
}

// update takes a new list: a connection that left it, went offline or
// changed its transport has a new epoch.
func (w *idWatch) update(es []adb.DeviceEntry, first bool) {
	w.mu.Lock()
	if w.epochs == nil {
		w.epochs = map[string]uint64{}
	}
	old := w.entries
	w.entries = make(map[string]adb.DeviceEntry, len(es))
	for _, e := range es {
		w.entries[e.Serial] = e
	}
	var gone []string
	if first {
		// Anything may have happened while the watch was away.
		w.gen++
		w.live = true
		for s := range old {
			gone = append(gone, s)
		}
	} else {
		for s, o := range old {
			n, ok := w.entries[s]
			if o.State == adb.StateDevice && (!ok || n.State != adb.StateDevice || n.TransportID != o.TransportID) {
				w.epochs[s]++
				gone = append(gone, s)
			}
		}
	}
	w.mu.Unlock()
	for _, s := range gone {
		w.gone(s)
	}
}

// --- the person's say ---

// UseOverWiFi implements driver.WiFiUser: the phone is the person's. One
// on a cable is then marked, and one Pinky has no record of yet is marked
// at its first check over the network.
func (p *Provider) UseOverWiFi(id string) {
	if p.ids != nil {
		p.ids.use(id)
	}
}

// TrustPhone implements driver.PhoneTruster: the person says the phone now
// listed under id is theirs, after they reset it or paired it again. It is
// marked anew over its connection on the computer's network (Tailscale's
// when it has none).
func (p *Provider) TrustPhone(ctx context.Context, id string) error {
	if p.ids == nil {
		return fmt.Errorf("android: checking phones: %w", driver.ErrUnsupported)
	}
	entries, err := p.devices(ctx)
	if err != nil {
		return wrapErr(err)
	}
	var pick *adb.DeviceEntry
	for _, e := range entries {
		if e.State != adb.StateDevice {
			continue
		}
		pr, err := p.deviceProps(ctx, e)
		if err != nil || p.phoneID(e, pr) != id {
			continue
		}
		if pick == nil || trusted(e) && !trusted(*pick) || viaTailnet(pick.Serial) && !viaTailnet(e.Serial) {
			pick = &e
		}
	}
	if pick == nil {
		return fmt.Errorf("android: %s is not connected: %w", id, driver.ErrOffline)
	}
	if err := p.ids.mark(ctx, pick.Serial, id); err != nil {
		return err
	}
	p.ids.use(id)
	p.cfg.logf("android: %s at %s: trusted by the person", id, pick.Serial)
	return nil
}

// trustConn marks the phone at a connection the person made in Pinky:
// they typed its address.
func (p *Provider) trustConn(ctx context.Context, e adb.DeviceEntry) {
	if p.ids == nil || trusted(e) {
		return
	}
	pr, err := p.deviceProps(ctx, e)
	if err != nil {
		p.cfg.logf("android: %s: reading the phone to mark it: %v", e.Serial, err)
		return
	}
	id := p.phoneID(e, pr)
	if err := p.ids.mark(ctx, e.Serial, id); err != nil {
		p.cfg.logf("android: %s at %s: %v", id, e.Serial, err)
		return
	}
	p.ids.use(id)
}

// trustPaired marks the phone that just paired, at the connection made to
// it, when that connection goes to the address the phone paired from: the
// pairing code proved the phone there, while its connect service may be
// anything on the network that names itself after the phone. One that
// goes elsewhere waits for the person, as any phone found on Wi-Fi.
func (p *Provider) trustPaired(ctx context.Context, e adb.DeviceEntry, guid, host string) {
	if p.ids == nil || trusted(e) {
		return
	}
	at := serialHost(e.Serial)
	if at == "" && guid != "" {
		// A connection by name: where the server's discovery found it.
		listed, _ := p.cfg.Client.MDNSServices(ctx)
		at = serialHost(serviceAddr(listed, guid))
	}
	if host != "" && at != "" && !sameHost(at, host) {
		p.cfg.logf("android: the phone paired from %s, but its connection %s goes to %s: not marked; the person says whether it is theirs", host, e.Serial, at)
		return
	}
	p.trustConn(ctx, e)
}

// sameHost reports whether two hosts are the same address.
func sameHost(a, b string) bool {
	x, err1 := netip.ParseAddr(a)
	y, err2 := netip.ParseAddr(b)
	if err1 != nil || err2 != nil {
		return strings.EqualFold(a, b)
	}
	return x.Unmap() == y.Unmap()
}

var (
	_ driver.WiFiUser     = (*Provider)(nil)
	_ driver.PhoneTruster = (*Provider)(nil)
)

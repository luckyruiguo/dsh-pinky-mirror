package netmux

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver/android/mdns"
)

// The tag for a made-up HostID and identifier, worked out apart from this
// code (Python's hmac: HKDF-SHA512 with an empty salt and info, 32 bytes,
// then HMAC-SHA256, the first 8 bytes, base64).
const (
	testHostID     = "8E0A1B2C-0000-4000-8000-123456789ABC"
	testIdentifier = "pinky-test-identifier"
	testTag        = "2PqSPmQCI0g="
)

func TestAuthTag(t *testing.T) {
	tag := AuthTag(testHostID, testIdentifier)
	if got := base64.StdEncoding.EncodeToString(tag[:]); got != testTag {
		t.Errorf("tag = %s, want %s", got, testTag)
	}
}

func TestMatch(t *testing.T) {
	pairs := map[string]PairInfo{
		"IPHONE": {HostID: testHostID, WiFiMAC: "08:87:C7:2A:8A:DB"},
		"IPAD":   {HostID: "OTHER-HOST", WiFiMAC: "f0:2f:4b:e8:d3:4a"},
	}
	cases := []struct {
		name string
		svc  mdns.Service
		want string
	}{
		{"by TXT, private address in the name", mdns.Service{
			Instance: "fa:df:90:f6:dd:2a@fe80::f8df:90ff:fef6:dd2a-supportsRP-26",
			TXT:      []string{"identifier=" + testIdentifier, "authTag=AAAAAAAAAAA=", "authTag#1= " + testTag + " "},
		}, "IPHONE"},
		{"by the Wi-Fi address in the name (before iOS 26.4)", mdns.Service{
			Instance: "f0:2f:4b:e8:d3:4a@fe80::1-supportsRP-24",
		}, "IPAD"},
		{"addresses compare in any case", mdns.Service{Instance: "08:87:c7:2a:8a:db@fe80::2"}, "IPHONE"},
		{"a tag for another computer", mdns.Service{
			Instance: "aa:bb:cc:dd:ee:ff@fe80::3",
			TXT:      []string{"identifier=" + testIdentifier, "authTag=AAAAAAAAAAA="},
		}, ""},
		{"no TXT, unknown address", mdns.Service{Instance: "aa:bb:cc:dd:ee:ff@fe80::3"}, ""},
	}
	for _, c := range cases {
		if got := Match(c.svc, pairs); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

type fakeSource struct {
	mu   sync.Mutex
	svcs []mdns.Service
}

func (f *fakeSource) Services() []mdns.Service {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mdns.Service(nil), f.svcs...)
}
func (f *fakeSource) Err() error   { return nil }
func (f *fakeSource) Close() error { return nil }

func iphoneService(addr string) mdns.Service {
	return mdns.Service{
		Instance: "fa:df:90:f6:dd:2a@fe80::f8df:90ff:fef6:dd2a-supportsRP-26", Type: ServiceType,
		Port: 32498, Addrs: []netip.Addr{netip.MustParseAddr("fe80::55"), netip.MustParseAddr(addr)},
		TXT: []string{"identifier=" + testIdentifier, "authTag=" + testTag},
	}
}

func TestFinder(t *testing.T) {
	m := New("unix:///nonexistent", nil)
	src := &fakeSource{svcs: []mdns.Service{iphoneService("192.168.33.187")}}
	now := time.Unix(1_000_000, 0)
	answers := true
	reads := 0
	f := &Finder{
		Mux:      m,
		UDIDs:    func() []string { return []string{"IPHONE", "BROKEN"} },
		BrowseFn: func() (Source, error) { return src, nil },
		PairFn: func(udid string) (PairInfo, error) {
			reads++
			if udid == "BROKEN" {
				return PairInfo{}, errors.New("no record")
			}
			return PairInfo{HostID: testHostID}, nil
		},
		ProbeFn: func(ctx context.Context, addr netip.Addr) bool { return answers },
		NowFn:   func() time.Time { return now },
	}
	s := newFindState(f, f.logOrDiscard())
	ctx := context.Background()

	s.tick(ctx)
	if got := m.Phones(); len(got) != 1 || got[0].UDID != "IPHONE" || got[0].Addr.String() != "192.168.33.187" {
		t.Fatalf("phones = %v", got)
	}
	if reads != 2 {
		t.Errorf("pair records read %d times, want 2", reads)
	}
	// Pair records are read once; one that failed again after pairRetry.
	now = now.Add(3 * time.Second)
	s.tick(ctx)
	if reads != 2 {
		t.Errorf("pair records read again: %d", reads)
	}
	now = now.Add(pairRetry)
	s.tick(ctx)
	if reads != 3 {
		t.Errorf("the failed pair record was not read again: %d", reads)
	}

	listed := func() bool { return len(m.Phones()) == 1 }
	setSvcs := func(svcs ...mdns.Service) {
		src.mu.Lock()
		src.svcs = svcs
		src.mu.Unlock()
	}

	// Lockdown does not answer: still listed for dropAfter from the first
	// look that asked it, which comes within probeSteady, then dropped.
	answers = false
	now = now.Add(probeSteady)
	s.tick(ctx)
	now = now.Add(dropAfter - 3*time.Second)
	s.tick(ctx)
	if !listed() {
		t.Error("dropped before dropAfter")
	}
	now = now.Add(3 * time.Second)
	s.tick(ctx)
	if listed() {
		t.Error("still listed after dropAfter")
	}
	// It answers again: listed again, at once.
	answers = true
	s.tick(ctx)
	if !listed() {
		t.Fatal("not listed again")
	}

	// The record lacks its TXT strings for a while, as on arch at 01:25
	// (the iPad then dropped, and Pinky's session on it with it): matched
	// by the name it had, it stays listed.
	noTXT := iphoneService("192.168.33.187")
	noTXT.TXT = nil
	setSvcs(noTXT)
	now = now.Add(3 * time.Second)
	s.tick(ctx)
	if !listed() {
		t.Error("dropped when its record lost its TXT strings")
	}
	// It has no address for a while, or goes away: lockdown is asked at
	// the address it had, and it stays listed while that answers.
	noAddr := iphoneService("192.168.33.187")
	noAddr.Addrs = nil
	setSvcs(noAddr)
	s.tick(ctx)
	setSvcs()
	now = now.Add(3 * time.Second)
	s.tick(ctx)
	if got := m.Phones(); len(got) != 1 || got[0].Addr.String() != "192.168.33.187" {
		t.Errorf("without its record: %v", got)
	}
	// Without a record and without answers, it is dropped after dropAfter
	// and not asked again.
	answers = false
	now = now.Add(probeSteady)
	s.tick(ctx)
	now = now.Add(dropAfter + time.Second)
	s.tick(ctx)
	if listed() {
		t.Error("listed without a record or answers")
	}
	answers = true
	now = now.Add(3 * time.Second)
	s.tick(ctx)
	if listed() {
		t.Error("listed again without a record")
	}

	// A record with only an IPv6 address, of a phone not listed, is not
	// listed.
	v6 := iphoneService("1.2.3.4")
	v6.Addrs = []netip.Addr{netip.MustParseAddr("fe80::1")}
	setSvcs(v6)
	s.tick(ctx)
	if listed() {
		t.Error("listed with only a link-local address")
	}
}

// TestFinderProbePace: lockdown on a phone that keeps answering is asked
// less and less often, up to every probeSteady; once it stops, at every
// look until it is dropped; and at once when it shows up again.
func TestFinderProbePace(t *testing.T) {
	m := New("unix:///nonexistent", nil)
	src := &fakeSource{svcs: []mdns.Service{iphoneService("192.168.33.187")}}
	now := time.Unix(1_000_000, 0)
	answers, probes := true, 0
	f := &Finder{
		Mux:      m,
		UDIDs:    func() []string { return []string{"IPHONE"} },
		BrowseFn: func() (Source, error) { return src, nil },
		PairFn:   func(string) (PairInfo, error) { return PairInfo{HostID: testHostID}, nil },
		ProbeFn:  func(context.Context, netip.Addr) bool { probes++; return answers },
		NowFn:    func() time.Time { return now },
	}
	s := newFindState(f, f.logOrDiscard())
	ctx := context.Background()
	look := func(d time.Duration) {
		for end := now.Add(d); now.Before(end); now = now.Add(f.interval()) {
			s.tick(ctx)
		}
	}
	// Five minutes of looks every 3 s: 100 looks, a dozen questions.
	look(5 * time.Minute)
	if probes > 15 {
		t.Errorf("lockdown asked %d times in 5 minutes", probes)
	}
	if len(m.Phones()) != 1 {
		t.Fatal("not listed")
	}
	// A drop shorter than dropAfter (the computer's Wi-Fi renewing its
	// address) does not drop it, wherever it falls between two answers.
	answers = false
	for range 13 {
		now = now.Add(f.interval())
		s.tick(ctx)
		if len(m.Phones()) != 1 {
			t.Fatal("dropped during a 39 s drop")
		}
	}
	answers = true
	look(time.Minute)
	// It stops answering: asked at every look from its next turn, which
	// comes within probeSteady, and dropped dropAfter later.
	answers, probes = false, 0
	look(probeSteady + dropAfter + 6*time.Second)
	if len(m.Phones()) != 0 {
		t.Fatal("still listed")
	}
	if n := int(dropAfter / f.interval()); probes < n-2 {
		t.Errorf("lockdown asked %d times while the phone was gone, want about %d or more", probes, n)
	}
	// Its record stays (it sleeps, say): asked less and less often.
	probes = 0
	look(5 * time.Minute)
	if probes > 15 {
		t.Errorf("lockdown asked %d times in 5 minutes of no answers", probes)
	}
	// Its record goes, and comes back with the phone: asked at once.
	src.mu.Lock()
	svcs := src.svcs
	src.svcs = nil
	src.mu.Unlock()
	look(f.interval())
	src.mu.Lock()
	src.svcs = svcs
	src.mu.Unlock()
	answers, probes = true, 0
	s.tick(ctx)
	if probes != 1 || len(m.Phones()) != 1 {
		t.Errorf("back: asked %d times, listed %v", probes, m.Phones())
	}
}

// TestFinderLastAddressOnlyWhileItAnswers: a phone whose records name only
// the cable's link (172.20.10.1) stays at its Wi-Fi address while lockdown
// answers there, and moves to the link at once when it does not, rather
// than waiting out dropAfter at a dead address.
func TestFinderLastAddressOnlyWhileItAnswers(t *testing.T) {
	m := New("unix:///nonexistent", nil)
	src := &fakeSource{svcs: []mdns.Service{iphoneService("192.168.89.20")}}
	now := time.Unix(1_000_000, 0)
	wifiUp := true
	f := &Finder{
		Mux:      m,
		UDIDs:    func() []string { return []string{"IPHONE"} },
		BrowseFn: func() (Source, error) { return src, nil },
		PairFn:   func(string) (PairInfo, error) { return PairInfo{HostID: testHostID}, nil },
		ProbeFn: func(_ context.Context, a netip.Addr) bool {
			return a.String() == "172.20.10.1" || wifiUp
		},
		NowFn: func() time.Time { return now },
	}
	s := newFindState(f, f.logOrDiscard())
	ctx := context.Background()
	at := func() string {
		if ps := m.Phones(); len(ps) == 1 {
			return ps[0].Addr.String()
		}
		return fmt.Sprint(m.Phones())
	}
	s.tick(ctx)
	if got := at(); got != "192.168.89.20" {
		t.Fatalf("listed at %s", got)
	}
	// Plugged in: its records name the cable's link only for a moment, and
	// it still answers on Wi-Fi: it stays.
	src.mu.Lock()
	src.svcs = []mdns.Service{iphoneService("172.20.10.1")}
	src.mu.Unlock()
	now = now.Add(3 * time.Second)
	s.tick(ctx)
	if got := at(); got != "192.168.89.20" {
		t.Errorf("moved to %s while it answered on Wi-Fi", got)
	}
	// Its Wi-Fi went off: at the link at once.
	wifiUp = false
	now = now.Add(3 * time.Second)
	s.tick(ctx)
	if got := at(); got != "172.20.10.1" {
		t.Errorf("listed at %s once Wi-Fi stopped answering", got)
	}
}

// addrTest runs a Finder for one phone whose lockdown answers at the
// addresses up, and counts where lockdown was asked.
type addrTest struct {
	t     *testing.T
	m     *Mux
	src   *fakeSource
	s     *findState
	now   time.Time
	every time.Duration
	mu    sync.Mutex
	up    map[string]bool
	asked map[string]int
}

func newAddrTest(t *testing.T, own []netip.Addr, svcs ...mdns.Service) *addrTest {
	a := &addrTest{t: t, m: New("unix:///nonexistent", nil), src: &fakeSource{svcs: svcs},
		now: time.Unix(1_000_000, 0), up: map[string]bool{}, asked: map[string]int{}}
	f := &Finder{
		Mux:      a.m,
		UDIDs:    func() []string { return []string{"IPHONE"} },
		BrowseFn: func() (Source, error) { return a.src, nil },
		PairFn:   func(string) (PairInfo, error) { return PairInfo{HostID: testHostID}, nil },
		ProbeFn: func(_ context.Context, addr netip.Addr) bool {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.asked[addr.String()]++
			return a.up[addr.String()]
		},
		NowFn:   func() time.Time { return a.now },
		LocalFn: func() []netip.Addr { return own },
	}
	a.s = newFindState(f, f.logOrDiscard())
	a.every = f.interval()
	return a
}

func (a *addrTest) records(svcs ...mdns.Service) {
	a.src.mu.Lock()
	a.src.svcs = svcs
	a.src.mu.Unlock()
}

// answer makes lockdown answer at addrs, and nowhere else.
func (a *addrTest) answer(addrs ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.up = map[string]bool{}
	for _, addr := range addrs {
		a.up[addr] = true
	}
}

// tick looks once and says where the phone is listed then, "" for nowhere.
func (a *addrTest) tick() string {
	a.s.tick(context.Background())
	a.now = a.now.Add(a.every)
	switch ps := a.m.Phones(); len(ps) {
	case 0:
		return ""
	case 1:
		return ps[0].Addr.String()
	default:
		return fmt.Sprint(ps)
	}
}

// look looks for d, and fails unless the phone is listed at want ("" for
// nowhere) after every look.
func (a *addrTest) look(d time.Duration, want string) {
	a.t.Helper()
	for end := a.now.Add(d); a.now.Before(end); {
		if got := a.tick(); got != want {
			a.t.Fatalf("listed at %q, want %q", got, want)
		}
	}
}

// otherInstance is the phone's record under another instance name, as
// someone else on the network could send it.
func otherInstance(addr string) mdns.Service {
	svc := iphoneService(addr)
	svc.Instance = "02:00:00:00:00:01@fe80::1-supportsRP-26"
	return svc
}

// TestFinderStaysWhileItAnswers: records that name another address for a
// listed phone, someone replaying its name and TXT strings, say, do not
// move it while lockdown answers where it is listed, nor while the phone
// naps; lockdown is not even asked at the other address. A phone that was
// dropped goes back to its last address while its records name that one
// among others.
func TestFinderStaysWhileItAnswers(t *testing.T) {
	const phone, other = "192.168.89.20", "192.168.89.5"
	a := newAddrTest(t, nil, iphoneService(phone))
	a.answer(phone, other)
	a.look(time.Minute, phone)
	// Another record next to the phone's, and then alone (sent with the
	// cache-flush bit, it drops the phone's address from the cache).
	a.records(iphoneService(phone), otherInstance(other))
	a.look(time.Minute, phone)
	a.records(iphoneService(other))
	a.look(5*time.Minute, phone)
	// It naps: lockdown does not answer for 30 s.
	a.answer(other)
	a.look(30*time.Second, phone)
	a.answer(phone, other)
	a.look(time.Minute, phone)
	// It is away long enough to be dropped, while its records name both
	// addresses, and comes back.
	a.records(iphoneService(phone), otherInstance(other))
	a.answer(other)
	for range int((probeSteady + dropAfter + 2*a.every) / a.every) {
		a.tick()
	}
	if got := a.tick(); got != "" {
		t.Fatalf("still listed at %s while away", got)
	}
	a.answer(phone, other)
	for range int(probeSteady/a.every) + 1 {
		a.tick()
	}
	if got := a.tick(); got != phone {
		t.Errorf("back at %q, want %s", got, phone)
	}
	if n := a.asked[other]; n != 0 {
		t.Errorf("lockdown asked %d times at %s", n, other)
	}
}

// TestFinderFollowsAMovedPhone: a phone that got another address (a new
// DHCP lease) stays listed at the one it had until lockdown there has not
// answered for dropAfter, and then goes to the new one at once, without a
// look where it is not listed. To and from the cable's link it goes at
// once (TestFinderLastAddressOnlyWhileItAnswers).
func TestFinderFollowsAMovedPhone(t *testing.T) {
	const old, moved = "192.168.89.20", "192.168.89.31"
	a := newAddrTest(t, nil, iphoneService(old))
	a.answer(old)
	a.look(time.Minute, old)
	a.records(iphoneService(moved))
	a.answer(moved)
	start := a.now
	movedAt := time.Duration(-1)
	for range int((probeSteady + dropAfter + 4*a.every) / a.every) {
		when := a.now.Sub(start)
		got := a.tick()
		switch {
		case got == old && movedAt < 0:
		case got == moved && movedAt < 0:
			movedAt = when
		case got == moved:
		default:
			t.Fatalf("%v after the move: listed at %q", when, got)
		}
	}
	// Lockdown at the old address is next asked within probeSteady.
	if movedAt < dropAfter || movedAt > probeSteady+dropAfter+a.every {
		t.Errorf("moved %v after it did, want after %v and within %v", movedAt, dropAfter, probeSteady+dropAfter)
	}
}

// TestFinderSkipsAddressesNoPhoneHas: an address no phone on the network
// can have, this computer's own among them, is neither listed nor asked
// about, alone in a record or next to the phone's.
func TestFinderSkipsAddressesNoPhoneHas(t *testing.T) {
	const phone = "192.168.89.20"
	own := []netip.Addr{netip.MustParseAddr("192.168.89.2"), netip.MustParseAddr("fe80::2")}
	for _, bad := range []string{"127.0.0.1", "127.3.2.1", "0.0.0.0", "0.1.2.3", "224.0.0.251", "239.255.255.250", "255.255.255.255", "192.168.89.2"} {
		t.Run(bad, func(t *testing.T) {
			a := newAddrTest(t, own, iphoneService(bad))
			a.answer(bad, phone)
			a.look(10*time.Second, "")
			both := iphoneService(phone)
			both.Addrs = append(both.Addrs, netip.MustParseAddr(bad))
			a.records(both)
			a.look(10*time.Second, phone)
			if n := a.asked[bad]; n != 0 {
				t.Errorf("lockdown asked %d times at %s", n, bad)
			}
		})
	}
}

func TestFinderRunStops(t *testing.T) {
	m := New("unix:///nonexistent", nil)
	src := &fakeSource{svcs: []mdns.Service{iphoneService("192.168.33.187")}}
	f := &Finder{
		Mux:      m,
		UDIDs:    func() []string { return []string{"IPHONE"} },
		Interval: 10 * time.Millisecond,
		BrowseFn: func() (Source, error) { return src, nil },
		PairFn:   func(string) (PairInfo, error) { return PairInfo{HostID: testHostID}, nil },
		ProbeFn:  func(context.Context, netip.Addr) bool { return true },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for len(m.Phones()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(m.Phones()) != 1 {
		t.Fatal("not found")
	}
	cancel()
	<-done
	if len(m.Phones()) != 0 {
		t.Error("phones listed after the search stopped")
	}
}

func TestPairedUDIDs(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"00008140-001E79483A00401C.plist", "SystemConfiguration.plist", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(PairedUDIDs(dir), ","); got != "00008140-001E79483A00401C" {
		t.Errorf("UDIDs = %q", got)
	}
	if got := PairedUDIDs(filepath.Join(dir, "missing")); got != nil {
		t.Errorf("UDIDs of a missing folder = %q", got)
	}
}

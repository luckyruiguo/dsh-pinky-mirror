//go:build device

package android

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
	"github.com/DIYgod/pinky/internal/tailnet"
)

// TestDeviceWiFiSwitch moves a real phone to Wi-Fi (adb tcpip, then a
// connection at its Wi-Fi address), drops that connection and has the
// reconnect pass bring it back, then gives the phone a wrong address and
// has the pass find the right one with mDNS. With PINKY_WIFI_OFF=1 it ends
// with the Wi-Fi connection stopped (adb usb).
//
//	PINKY_DEVICE=<phone id> go test -tags device -run TestDeviceWiFiSwitch
//
// On macOS the test binary needs the Local Network for the mDNS step:
// sign it with an identifier, as docs/notes/android.md says.
func TestDeviceWiFiSwitch(t *testing.T) {
	id := os.Getenv("PINKY_DEVICE")
	if id == "" {
		t.Skip("PINKY_DEVICE not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	p := NewProvider(Config{Logf: t.Logf, NetworkFile: filepath.Join(t.TempDir(), "network-phones.json")})
	if _, err := p.List(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := p.PhoneWiFi(ctx, id)
	t.Logf("before: %+v, %v", st, err)
	start := time.Now()
	st, err = p.SetPhoneWiFi(ctx, id, true)
	if err != nil || !st.On || !st.Connected {
		t.Fatalf("on: %+v, %v", st, err)
	}
	t.Logf("on in %v: %+v", time.Since(start).Round(time.Millisecond), st)
	n, _ := p.net.get(id)

	// Dropped: the pass connects again.
	if err := p.cfg.Client.Disconnect(ctx, n.lanSerial()); err != nil {
		t.Fatal(err)
	}
	entries, _ := p.cfg.Client.Devices(ctx)
	start = time.Now()
	if missing := p.link.reconnect(ctx, p.net.list(), entries); missing {
		t.Fatalf("not reconnected at %s", n.lanSerial())
	}
	t.Logf("reconnected at %s in %v", n.lanSerial(), time.Since(start).Round(time.Millisecond))

	// A wrong address: mDNS finds the right one.
	if err := p.cfg.Client.Disconnect(ctx, n.lanSerial()); err != nil {
		t.Fatal(err)
	}
	p.net.update(id, func(m *netPhone) { m.LAN = "192.168.5.250" })
	entries, _ = p.cfg.Client.Devices(ctx)
	start = time.Now()
	missing := p.link.reconnect(ctx, p.net.list(), entries)
	got, _ := p.net.get(id)
	t.Logf("after a wrong address: missing %v, kept %s, in %v", missing, got.LAN, time.Since(start).Round(time.Millisecond))
	if missing || got.LAN != n.LAN {
		t.Errorf("not found again: missing %v, kept %+v", missing, got)
	}
	infos, err := p.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range infos {
		if in.ID == id {
			t.Logf("listed: %s %s via=%q state=%s", in.ID, in.Connection, in.Via, in.State)
			if in.State != driver.StateOnline {
				t.Errorf("listed as %s", in.State)
			}
		}
	}
	if os.Getenv("PINKY_WIFI_OFF") == "1" {
		st, err = p.SetPhoneWiFi(ctx, id, false)
		t.Logf("off: %+v, %v", st, err)
		if err != nil || st.On {
			t.Errorf("off: %+v, %v", st, err)
		}
	}
}

// TestDeviceTailnetRoam watches a phone connected through Tailscale while
// the person moves it to another network: a background display scrolls
// Settings, and every second the test logs the frames that arrived, the
// longest gap between two, and whether a shell command answers, so the
// log shows how long the picture stopped and whether the connection
// survived. It also logs what Tailscale counted for the phone.
//
//	PINKY_DEVICE=100.x.y.z:5555 PINKY_ROAM_SECONDS=180 go test -tags device -run TestDeviceTailnetRoam
//
// It needs the phone's screen off and the phone locked, as
// TestDeviceWifiStream does.
func TestDeviceTailnetRoam(t *testing.T) {
	serial := os.Getenv("PINKY_DEVICE")
	if serial == "" || !viaTailnet(serial) {
		t.Skip("PINKY_DEVICE is not a connection through Tailscale")
	}
	seconds := 120
	if s := os.Getenv("PINKY_ROAM_SECONDS"); s != "" {
		fmt.Sscan(s, &seconds)
	}
	// How long each swipe and shell command may take: a relayed path
	// needs several round trips of 0.1 to 0.5 s to start a session.
	wait := 900 * time.Millisecond
	if s := os.Getenv("PINKY_ROAM_WAIT"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			wait = d
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(seconds+120)*time.Second)
	defer cancel()
	d := openOff(t, ctx)
	// PINKY_ROAM_BITRATE (bits a second) and PINKY_ROAM_FPS try another
	// stream than the one Pinky picks.
	fps := 0
	if s := os.Getenv("PINKY_ROAM_BITRATE"); s != "" {
		fmt.Sscan(s, &d.cfg.VideoBitRate)
	}
	if s := os.Getenv("PINKY_ROAM_FPS"); s != "" {
		fmt.Sscan(s, &fps)
	}
	t.Logf("stream: %+v, fps %d", d.baseOptions(), fps)
	b, err := d.OpenDisplay(ctx, DisplayOptions{App: "com.android.settings", MaxFPS: fps})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	st, err := b.Stream(ctx, driver.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	type sec struct {
		frames, bytes int
		gap           time.Duration
	}
	var mu sync.Mutex
	secs := map[int]*sec{}
	start := time.Now()
	last := start
	go func() {
		for p := range st.Packets() {
			if p.Config {
				continue
			}
			now := time.Now()
			mu.Lock()
			i := int(now.Sub(start) / time.Second)
			s := secs[i]
			if s == nil {
				s = &sec{}
				secs[i] = s
			}
			s.frames++
			s.bytes += len(p.Data)
			s.gap = max(s.gap, now.Sub(last))
			last = now
			mu.Unlock()
		}
	}()
	host := serialHost(serial)
	peerBytes := func() string {
		s, err := tailnet.Read(ctx)
		if err != nil {
			return err.Error()
		}
		for _, p := range s.Peers {
			if p.IPv4().String() == host {
				path := "direct " + p.Direct
				if p.Direct == "" {
					path = "relay " + p.Relay
				}
				return fmt.Sprintf("rx %d tx %d, %s", p.RxBytes, p.TxBytes, path)
			}
		}
		return "not on the tailnet"
	}
	t.Logf("tailscale at the start: %s", peerBytes())
	for i := 0; i < seconds; i++ {
		from, to := screen.Point{X: 540, Y: 1500}, screen.Point{X: 540, Y: 600}
		if i%2 == 1 {
			from, to = to, from
		}
		sctx, scancel := context.WithTimeout(ctx, wait)
		swipeErr := b.Swipe(sctx, from, to, 300*time.Millisecond)
		scancel()
		ectx, ecancel := context.WithTimeout(ctx, wait)
		t0 := time.Now()
		_, echoErr := d.client.Shell(ectx, serial, "echo")
		rtt := time.Since(t0)
		ecancel()
		time.Sleep(time.Until(start.Add(time.Duration(i+1) * time.Second)))
		mu.Lock()
		s := secs[i]
		if s == nil {
			s = &sec{}
		}
		silent := time.Since(last)
		mu.Unlock()
		line := fmt.Sprintf("t=%3ds (%5.1fs) frames %2d, %5.0f kbit/s, longest gap %4dms, silent %5dms, echo ", i, time.Since(start).Seconds(), s.frames, float64(s.bytes)*8/1000, s.gap.Milliseconds(), silent.Milliseconds())
		if echoErr != nil {
			line += "failed: " + echoErr.Error()
		} else {
			line += rtt.Round(time.Millisecond).String()
		}
		if swipeErr != nil {
			line += "; swipe failed: " + swipeErr.Error()
		}
		t.Log(line)
		if i%30 == 29 {
			t.Logf("tailscale: %s", peerBytes())
		}
	}
	t.Logf("tailscale at the end: %s", peerBytes())
}

// TestDeviceTailnetIdle counts what a phone's stream sends through
// Tailscale while nothing on its screen moves, then while it scrolls, to
// estimate the data an hour of each takes.
//
//	PINKY_DEVICE=100.x.y.z:5555 go test -tags device -run TestDeviceTailnetIdle
func TestDeviceTailnetIdle(t *testing.T) {
	serial := os.Getenv("PINKY_DEVICE")
	if serial == "" || !viaTailnet(serial) {
		t.Skip("PINKY_DEVICE is not a connection through Tailscale")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	d := openOff(t, ctx)
	b, err := d.OpenDisplay(ctx, DisplayOptions{App: "com.android.settings"})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	st, err := b.Stream(ctx, driver.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var mu sync.Mutex
	var bytes, frames int
	go func() {
		for p := range st.Packets() {
			mu.Lock()
			bytes += len(p.Data)
			frames++
			mu.Unlock()
		}
	}()
	host := serialHost(serial)
	peer := func() (rx, tx int64) {
		s, err := tailnet.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range s.Peers {
			if p.IPv4().String() == host {
				return p.RxBytes, p.TxBytes
			}
		}
		t.Fatalf("%s is not on the tailnet", host)
		return 0, 0
	}
	measure := func(name string, dur time.Duration, act func(i int)) {
		time.Sleep(3 * time.Second) // let the last part settle
		tailnet.Default.Forget()
		rx0, tx0 := peer()
		mu.Lock()
		b0, f0 := bytes, frames
		mu.Unlock()
		start := time.Now()
		for i := 0; time.Since(start) < dur; i++ {
			if act != nil {
				act(i)
			}
			time.Sleep(time.Until(start.Add(time.Duration(i+1) * time.Second)))
		}
		el := time.Since(start)
		tailnet.Default.Forget()
		rx1, tx1 := peer()
		mu.Lock()
		b1, f1 := bytes, frames
		mu.Unlock()
		rx, tx := rx1-rx0, tx1-tx0
		perHour := func(n int64) float64 { return float64(n) / el.Hours() / 1e6 }
		t.Logf("%s, %v: video %d frames, %.0f kbit/s; Tailscale received %d B (%.0f kbit/s, %.0f MB/h), sent %d B (%.0f MB/h)",
			name, el.Round(time.Second), f1-f0, float64(b1-b0)*8/el.Seconds()/1000, rx, float64(rx)*8/el.Seconds()/1000, perHour(rx), tx, perHour(tx))
	}
	measure("still screen", 60*time.Second, nil)
	measure("scrolling every second", 30*time.Second, func(i int) {
		from, to := screen.Point{X: 540, Y: 1500}, screen.Point{X: 540, Y: 600}
		if i%2 == 1 {
			from, to = to, from
		}
		b.Swipe(ctx, from, to, 300*time.Millisecond)
	})
	measure("a tap every 5 s", 30*time.Second, func(i int) {
		if i%5 == 0 {
			b.Swipe(ctx, screen.Point{X: 540, Y: 1500}, screen.Point{X: 540, Y: 1100}, 200*time.Millisecond)
		}
	})
}

// TestDeviceWiFiWatch lists the phones every 2 s for a while, with the
// network phones kept in PINKY_NET_FILE and reconnected as Pinky does,
// and logs each change of how the phone is listed: so the person can
// unplug it, let it sleep, wake it or restart it, and the log shows what
// Pinky made of it.
//
//	PINKY_DEVICE=<phone id> PINKY_NET_FILE=/tmp/x.json PINKY_WATCH_SECONDS=300 go test -tags device -run TestDeviceWiFiWatch
func TestDeviceWiFiWatch(t *testing.T) {
	id, file := os.Getenv("PINKY_DEVICE"), os.Getenv("PINKY_NET_FILE")
	if id == "" || file == "" {
		t.Skip("PINKY_DEVICE or PINKY_NET_FILE not set")
	}
	seconds := 300
	if s := os.Getenv("PINKY_WATCH_SECONDS"); s != "" {
		fmt.Sscan(s, &seconds)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(seconds+60)*time.Second)
	defer cancel()
	p := NewProvider(Config{Logf: t.Logf, NetworkFile: file})
	start := time.Now()
	last := ""
	for time.Since(start) < time.Duration(seconds)*time.Second {
		infos, err := p.List(ctx)
		now := "not listed"
		if err != nil {
			now = "list failed: " + err.Error()
		}
		for _, in := range infos {
			if in.ID == id {
				now = fmt.Sprintf("%s over %s via=%q setup=%q", in.State, in.Connection, in.Via, in.Setup)
			}
		}
		if now != last {
			t.Logf("t=%5.1fs %s", time.Since(start).Seconds(), now)
			last = now
		}
		time.Sleep(2 * time.Second)
	}
	if st, err := p.PhoneWiFi(ctx, id); err == nil {
		t.Logf("at the end: %+v", st)
	}
}

// TestDeviceAway lists the tailnet's phones and sets a real phone up for
// away from home: adb tcpip over the connection it has, then a connection
// at its Tailscale address. The phones it sets up go to PINKY_NET_FILE.
//
//	PINKY_DEVICE=<phone id> PINKY_NET_FILE=/tmp/x.json go test -tags device -run TestDeviceAway
func TestDeviceAway(t *testing.T) {
	id, file := os.Getenv("PINKY_DEVICE"), os.Getenv("PINKY_NET_FILE")
	if id == "" || file == "" {
		t.Skip("PINKY_DEVICE or PINKY_NET_FILE not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	p := NewProvider(Config{Logf: t.Logf, NetworkFile: file})
	if _, err := p.List(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := p.Tailnet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("tailnet %q, this computer %q, state %s", st.Tailnet, st.Self, st.State)
	for _, ph := range st.Phones {
		t.Logf("  phone %s %s online=%v connected=%v relayed=%v id=%q", ph.Name, ph.Addr, ph.Online, ph.Connected, ph.Relayed, ph.ID)
	}
	a, err := p.Away(ctx, id)
	t.Logf("before: %+v, %v", a, err)
	start := time.Now()
	a, err = p.SetAway(ctx, id, true)
	if err != nil || !a.On || !a.Listening {
		t.Fatalf("on: %+v, %v", a, err)
	}
	t.Logf("on in %v: %+v", time.Since(start).Round(time.Millisecond), a)
	infos, _ := p.List(ctx)
	for _, in := range infos {
		if in.ID == id {
			t.Logf("listed: %s over %s via=%q", in.State, in.Connection, in.Via)
		}
	}
}

// TestDeviceAwayFailover follows a phone set up for away from home as the
// gateway does while the person takes it off the computer's network: the
// phone is opened by its id (the best connection: the local one at home),
// and every second the test asks whether it is locked, as the gateway's
// checks do; an offline error closes it and the next second opens it again
// by its id, which picks whatever connection is left. The log shows each
// change of the connections adb lists, which connection the phone is
// opened on, and how long it did not answer.
//
//	PINKY_DEVICE=<phone id> PINKY_NET_FILE=/tmp/x.json PINKY_WATCH_SECONDS=240 go test -tags device -run TestDeviceAwayFailover
func TestDeviceAwayFailover(t *testing.T) {
	id, file := os.Getenv("PINKY_DEVICE"), os.Getenv("PINKY_NET_FILE")
	if id == "" || file == "" {
		t.Skip("PINKY_DEVICE or PINKY_NET_FILE not set")
	}
	seconds := 240
	if s := os.Getenv("PINKY_WATCH_SECONDS"); s != "" {
		fmt.Sscan(s, &seconds)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(seconds+60)*time.Second)
	defer cancel()
	p := NewProvider(Config{Logf: t.Logf, NetworkFile: file})
	var d *Device
	defer func() {
		if d != nil {
			d.Close()
		}
	}()
	start := time.Now()
	at := func() string { return fmt.Sprintf("t=%5.1fs", time.Since(start).Seconds()) }
	lastConns, lastAns := "", time.Now()
	for time.Since(start) < time.Duration(seconds)*time.Second {
		tick := time.Now()
		p.List(ctx) // kicks the reconnect pass, as the gateway's discovery does
		entries, _ := p.cfg.Client.Devices(ctx)
		conns := ""
		for _, e := range entries {
			if pr, err := p.deviceProps(ctx, e); (err == nil && phoneID(e, pr) == id) || e.Serial == id || serialHost(e.Serial) != "" {
				conns += " " + e.Serial + "=" + e.State
			}
		}
		if conns != lastConns {
			t.Logf("%s adb lists:%s", at(), conns)
			lastConns = conns
		}
		if d == nil {
			nd, err := p.OpenDevice(ctx, id)
			if err != nil {
				t.Logf("%s open: %v", at(), err)
			} else {
				d = nd
				t.Logf("%s opened on %s (%s, via %q)", at(), d.Serial(), d.info.Connection, d.info.Via)
			}
		}
		if d != nil {
			cctx, ccancel := context.WithTimeout(ctx, 2*time.Second)
			_, err := d.Locked(cctx)
			ccancel()
			switch {
			case err == nil:
				if gap := time.Since(lastAns); gap > 2500*time.Millisecond {
					t.Logf("%s answers again on %s after %.1fs", at(), d.Serial(), gap.Seconds())
				}
				lastAns = time.Now()
			case errors.Is(err, driver.ErrOffline):
				t.Logf("%s offline on %s: %v; opening again", at(), d.Serial(), err)
				d.Close()
				d = nil
			default:
				t.Logf("%s no answer on %s for %.1fs: %v", at(), d.Serial(), time.Since(lastAns).Seconds(), err)
			}
		}
		time.Sleep(time.Until(tick.Add(time.Second)))
	}
}

// TestDeviceWiFiAgain plays "Use Wi-Fi again" on a phone kept in
// PINKY_NET_FILE whose adb stopped listening when it restarted, now
// plugged in: the switch says it stopped, and turning it on again moves
// the phone to Wi-Fi. PINKY_WIFI_OFF=1 then stops it.
func TestDeviceWiFiAgain(t *testing.T) {
	id, file := os.Getenv("PINKY_DEVICE"), os.Getenv("PINKY_NET_FILE")
	if id == "" || file == "" {
		t.Skip("PINKY_DEVICE or PINKY_NET_FILE not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	p := NewProvider(Config{Logf: t.Logf, NetworkFile: file})
	if _, err := p.List(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := p.PhoneWiFi(ctx, id)
	t.Logf("plugged in after the restart: %+v, %v", st, err)
	if !st.On || !st.Stopped {
		t.Errorf("not said to have stopped: %+v", st)
	}
	start := time.Now()
	st, err = p.SetPhoneWiFi(ctx, id, true)
	if err != nil || !st.Connected {
		t.Fatalf("again: %+v, %v", st, err)
	}
	t.Logf("on again in %v: %+v", time.Since(start).Round(time.Millisecond), st)
	if os.Getenv("PINKY_WIFI_OFF") == "1" {
		st, err = p.SetPhoneWiFi(ctx, id, false)
		t.Logf("off: %+v, %v", st, err)
	}
}

// TestDeviceAwayOff turns Use away from home off for a phone kept in
// PINKY_NET_FILE: its adb stops listening, its connection through
// Tailscale goes.
func TestDeviceAwayOff(t *testing.T) {
	id, file := os.Getenv("PINKY_DEVICE"), os.Getenv("PINKY_NET_FILE")
	if id == "" || file == "" {
		t.Skip("PINKY_DEVICE or PINKY_NET_FILE not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p := NewProvider(Config{Logf: t.Logf, NetworkFile: file})
	if _, err := p.List(ctx); err != nil {
		t.Fatal(err)
	}
	a, err := p.SetAway(ctx, id, false)
	t.Logf("off: %+v, %v", a, err)
	if err != nil || a.On {
		t.Fatalf("off: %+v, %v", a, err)
	}
	time.Sleep(3 * time.Second)
	a, err = p.Away(ctx, id)
	t.Logf("after: %+v, %v", a, err)
}

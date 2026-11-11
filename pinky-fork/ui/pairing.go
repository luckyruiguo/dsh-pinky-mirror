package ui

import (
	"context"
	"errors"
	"net"
	"runtime"
	"strconv"
	"strings"
	"time"

	mg "github.com/egoist/mygo/ui"
	"rsc.io/qr"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// Pairing an Android phone over Wi-Fi, by QR code or by pairing code
// (Developer options › Wireless debugging). The gateway's StartPairing
// gives the text of the QR code and waits for the phone; PairWithCode
// pairs with the address and code the phone shows. A phone that paired
// but that Pinky cannot find on the network gets a connect step, which
// takes the address Wireless debugging shows (ConnectWiFi).

// pairStuckAfter is how long a QR pairing waits for the phone before the
// page says what may be wrong and offers the pairing code instead.
const pairStuckAfter = 30 * time.Second

// pairState is a pairing under way on the Add phone page.
type pairState struct {
	seq    int
	cancel context.CancelFunc
	state  string // "", "starting", "waiting", "failed"
	text   string // what the QR code says
	code   *qr.Code
	err    string
	// errCode: pairing with a code may get past what went wrong, so the
	// failed QR pairing offers it.
	errCode bool
	since   time.Time // when the code showed
	// progress is the pairing's (driver.PairingReporter); nil when it
	// reports none.
	progress func() driver.PairingProgress
	foundAt  time.Time // when the phone showed up on the network

	// The pairing-code form, and its connect step.
	form     bool
	addr     string
	pin      string
	formBusy bool
	formErr  string
	connect  bool   // the phone paired; the form asks where to connect to it
	connAddr string // the address typed in the connect step
}

// prog returns the pairing's progress, the zero value when it reports
// none, and notes when the phone showed up.
func (st *pairState) prog() driver.PairingProgress {
	if st.progress == nil {
		return driver.PairingProgress{}
	}
	pr := st.progress()
	if pr.Found && st.foundAt.IsZero() {
		st.foundAt = time.Now()
	}
	return pr
}

// startPairing asks the gateway for a QR code and waits for the phone in
// the background, until it pairs or the person leaves the step.
func (a *app) startPairing() {
	st := &a.add.pair
	pr, ok := a.api.(pairer)
	if !ok {
		return
	}
	a.stopPairing()
	st.seq++
	seq := st.seq
	ctx, cancel := context.WithCancel(a.ctx)
	st.cancel = cancel
	st.state, st.err, st.text, st.code = "starting", "", "", nil
	go func() {
		pg, err := pr.StartPairing(ctx)
		if err != nil {
			a.post(func() {
				if st.seq == seq {
					st.state, st.err, st.errCode = "failed", pairError(err, true), false
				}
			})
			return
		}
		defer pg.Close()
		text := pg.QR()
		code, qerr := qr.Encode(text, qr.M)
		a.post(func() {
			if st.seq != seq {
				return
			}
			if qerr != nil {
				st.state, st.err = "failed", locales.PairingQRDrawError(qerr.Error())
				return
			}
			st.state, st.text, st.code, st.since = "waiting", text, code, time.Now()
			st.progress, st.foundAt = nil, time.Time{}
			if r, ok := pg.(driver.PairingReporter); ok {
				st.progress = r.Progress
			}
		})
		if qerr != nil {
			return
		}
		info, err := pg.Wait(ctx)
		if ctx.Err() != nil {
			return // the person left
		}
		a.paired(seq, info, err, false)
	}()
}

// stopPairing ends a QR pairing the person walked away from.
func (a *app) stopPairing() {
	st := &a.add.pair
	if st.cancel != nil {
		st.cancel()
		st.cancel = nil
	}
	st.seq++
	st.state, st.err, st.errCode = "", "", false
	st.progress = nil
}

// pairWithCode pairs with the address and code the phone shows under Pair
// device with pairing code.
func (a *app) pairWithCode() {
	st := &a.add.pair
	pr, ok := a.api.(pairer)
	if !ok || st.formBusy {
		return
	}
	addr := strings.TrimSpace(st.addr)
	if msg := checkPairAddr(addr); msg != "" {
		st.formErr = msg
		return
	}
	code := keepDigits(st.pin, 6)
	if len(code) != 6 {
		st.formErr = locales.PairingCodeIncomplete()
		return
	}
	st.formBusy, st.formErr = true, ""
	seq := st.seq
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 45*time.Second)
		defer cancel()
		info, err := pr.PairWithCode(ctx, addr, code)
		a.paired(seq, info, err, true)
	}()
}

// connectWiFi connects to a phone that paired but that Pinky could not
// find on the network, at the address Wireless debugging shows.
func (a *app) connectWiFi() {
	st := &a.add.pair
	wc, ok := a.api.(wifiConnector)
	if !ok || st.formBusy {
		return
	}
	addr := strings.TrimSpace(st.connAddr)
	if msg := checkPairAddr(addr); msg != "" {
		st.formErr = msg
		return
	}
	st.formBusy, st.formErr = true, ""
	seq := st.seq
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 45*time.Second)
		defer cancel()
		info, err := wc.ConnectWiFi(ctx, addr)
		a.paired(seq, info, err, true)
	}()
}

// paired finishes a pairing: on success it reads the devices again and
// shows the new phone. A phone that paired but did not show up on the
// network gets the connect step.
func (a *app) paired(seq int, info driver.Info, err error, form bool) {
	var devs []gateway.DeviceView
	if err == nil {
		devs = a.api.Devices()
	}
	a.post(func() {
		st := &a.add.pair
		if form {
			st.formBusy = false
		}
		if errors.Is(err, driver.ErrPairedNotFound) && (form || st.seq == seq) {
			if _, ok := a.api.(wifiConnector); ok {
				if !form {
					a.stopPairing()
				}
				st.form, st.connect, st.formErr = true, true, ""
				return
			}
		}
		if err != nil {
			switch {
			case form && st.connect:
				st.formErr = connectError(err)
			case form:
				st.formErr = pairError(err, false)
			case st.seq == seq:
				st.state, st.err, st.errCode = "failed", pairError(err, true), pairCodeHelps(err)
			}
			return
		}
		a.devices = devs
		id := info.ID
		if a.device(id) == nil {
			if d := a.add.fresh(a, driver.Android); d != nil {
				id = d.Info.ID
			}
		}
		a.stopPairing()
		st.form, st.addr, st.pin, st.connect, st.connAddr = false, "", "", false, ""
		a.add.step = ""
		a.selectPhone(id)
		paired := locales.PairingPairedPhone()
		if info.Name != "" {
			paired = locales.PairingPaired(info.Name)
		}
		if d := a.device(id); d != nil {
			paired = locales.PairingPaired(d.Info.Name)
			// The last step of adding a phone: its checkup.
			a.openCheckup(*d)
		}
		a.toasts = append(a.toasts, paired)
		a.afterRefresh(false)
	})
}

// checkPairAddr says what is wrong with an address typed for pairing, or
// "" when it is an IP address (or name) and port.
func checkPairAddr(addr string) string {
	want := locales.PairingAddrInvalid()
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return want
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return want
	}
	return ""
}

// pairError says in plain words why pairing failed, by QR code (qr) or by
// pairing code, from the reason the driver gives (driver.ErrPairRejected
// and the rest). A QR pairing never blames a code: Pinky made it.
func pairError(err error, qr bool) string {
	msg := strings.ToLower(err.Error())
	pick := func(code, scan string) string {
		if qr {
			return scan
		}
		return code
	}
	switch {
	case errors.Is(err, gateway.ErrNoPairing) || strings.Contains(msg, "adb server not running"):
		return locales.PairingErrNoAdb()
	case errors.Is(err, driver.ErrPairOldTool):
		return locales.CheckHostADBOld()
	case errors.Is(err, driver.ErrPairNoNetwork):
		return locales.PairingErrNoNetwork()
	case errors.Is(err, driver.ErrPairRejected):
		return pick(locales.PairingErrRejected(), locales.PairingErrQRRejected())
	case errors.Is(err, driver.ErrPairNotListening):
		return pick(locales.PairingErrNotListening(), locales.PairingErrQRNotListening())
	case errors.Is(err, driver.ErrPairUnreachable):
		return pick(locales.PairingErrUnreachable(), locales.PairingErrQRUnreachable())
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "timeout") || strings.Contains(msg, "timed out"):
		return locales.PairingErrTimeout()
	case errors.Is(err, driver.ErrUnsupported):
		return locales.PairingErrUnsupported()
	case strings.Contains(msg, "refused") || strings.Contains(msg, "unreachable") || strings.Contains(msg, "no route"):
		return pick(locales.PairingErrUnreachable(), locales.PairingErrQRUnreachable())
	}
	return locales.PairingErrOther(err.Error())
}

// pairCodeHelps reports whether pairing with a code may succeed where a
// QR pairing failed: not when adb cannot pair at all, nor when the network
// keeps the phone out of reach, which a code does not change.
func pairCodeHelps(err error) bool {
	switch {
	case errors.Is(err, gateway.ErrNoPairing), errors.Is(err, driver.ErrPairOldTool),
		errors.Is(err, driver.ErrPairUnreachable), errors.Is(err, driver.ErrUnsupported),
		errors.Is(err, driver.ErrPairNoNetwork):
		return false
	}
	return true
}

// connectError says in plain words why connecting at an address failed.
func connectError(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, driver.ErrPairNoNetwork):
		return locales.PairingErrNoNetwork()
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "timeout") || strings.Contains(msg, "timed out"):
		return locales.PairingErrTimeout()
	case strings.Contains(msg, "refused") || strings.Contains(msg, "unreachable") || strings.Contains(msg, "no route") ||
		strings.Contains(msg, "failed to connect") || strings.Contains(msg, "reset"):
		return locales.PairingConnectErrUnreachable()
	}
	return locales.PairingConnectErrOther(err.Error())
}

// localNetworkSettings is System Settings › Privacy & Security › Local
// Network.
const localNetworkSettings = "x-apple.systempreferences:com.apple.preference.security?Privacy_LocalNetwork"

// pairStuck says, once a QR pairing has waited long, what may be wrong
// and what to do next, with buttons for the pairing code and, on a Mac
// that keeps Pinky off the local network, its settings. It reports
// whether it showed.
func (a *app) pairStuck(c *mg.Context) bool {
	p := pal(c)
	st := &a.add.pair
	if st.state != "waiting" || st.since.IsZero() {
		return false
	}
	c.After(time.Second)
	pr := st.prog()
	var title, text string
	settings := false
	switch {
	case pr.Paired:
		return false // connecting ends by itself, in 30 s at most
	case pr.Found:
		if st.foundAt.IsZero() || time.Since(st.foundAt) < pairStuckAfter/2 {
			return false
		}
		title, text = locales.PairingStuckPairingTitle(), locales.PairingStuckPairing()
	case time.Since(st.since) < pairStuckAfter:
		return false
	case pr.NotAllowed:
		title, text = locales.PairingStuckNotAllowedTitle(), locales.PairingStuckNotAllowed()
		if runtime.GOOS == "darwin" {
			text, settings = locales.PairingStuckNotAllowedMac(), true
		}
	case pr.SearchErr != "":
		title, text = locales.PairingStuckNoSearchTitle(), locales.PairingStuckNoSearch(pr.SearchErr)
	default:
		title, text = locales.PairingStuckNotSeenTitle(), locales.PairingStuckNotSeen()
	}
	mg.Column(c).Padding(sp4, 18).Radius(rCard).Background(p.amberTint).Border(1, p.amberLine).Gap(10).Role(mg.RoleStatus).Children(func() {
		mg.Text(c, title).FontSize(fsHeadline).FontWeight(fwSemibold)
		mg.Text(c, text).FontSize(fsBody).LineHeight(1.45)
		mg.Row(c).Gap(10).Wrap().Children(func() {
			// Allowing Pinky is the fix there; the code goes around it.
			if settings && button(c, locales.PairingOpenLocalNetwork(), btnOpt{small: true, kind: btnPrimary}).Clicked() {
				openURL(c, localNetworkSettings, func(error) {})
			}
			if button(c, locales.PairingUseCode(), btnOpt{small: true}).Clicked() {
				a.stopPairing()
				st.form, st.formErr = true, ""
			}
		})
	})
	return true
}

// qrGeometry places a QR code of size modules in r, with 4 modules of
// quiet zone around it: where its first module starts and how wide each
// is, in whole pixels at scale s, so the code stays sharp.
func qrGeometry(r mg.Rect, s float32, size int) (ox, oy, cell float32) {
	if s <= 0 {
		s = 1
	}
	n := size + 8
	px := float32(int(min(r.W, r.H) * s / float32(n)))
	if px < 1 {
		px = 1
	}
	cell = px / s
	ox = float32(int((r.X+(r.W-cell*float32(n))/2)*s+0.5))/s + 4*cell
	oy = float32(int((r.Y+(r.H-cell*float32(n))/2)*s+0.5))/s + 4*cell
	return ox, oy, cell
}

// qrView draws a QR code with its quiet zone, its modules snapped to whole
// pixels so the code stays sharp at any scale.
func qrView(c *mg.Context, code *qr.Code, size float32) mg.Element {
	box := mg.Box(c).Size(size, size).Radius(12).Background(hex("#ffffff")).Shrink(0).Role(mg.RoleImage).
		Label(locales.PairingQRLabel())
	box.Draw(func(pt *mg.Painter, r mg.Rect) {
		if code == nil {
			return
		}
		ox, oy, cell := qrGeometry(r, pt.Scale(), code.Size)
		black := hex("#000000")
		for y := 0; y < code.Size; y++ {
			// One rectangle per run of dark modules, so no seams show.
			for x := 0; x < code.Size; {
				if !code.Black(x, y) {
					x++
					continue
				}
				x0 := x
				for x < code.Size && code.Black(x, y) {
					x++
				}
				pt.Fill(mg.Rect{X: ox + float32(x0)*cell, Y: oy + float32(y)*cell, W: float32(x-x0) * cell, H: cell}, black, 0)
			}
		}
	})
	return box
}

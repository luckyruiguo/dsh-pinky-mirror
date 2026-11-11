package ui

import (
	"context"
	"errors"
	"maps"
	"strings"
	"time"

	"github.com/egoist/mygo/i18n"
	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/update"
)

// Pinky Companion is the Android app that reads the screen and types for
// Pinky. It ships inside Pinky; a phone without it, or with an older one,
// gets an Install card on its page. Nothing is installed without the
// person's click, except that with Update automatically on Pinky updates
// an older companion by itself while the phone is free (companionTick in
// update.go), and replaces one signed with another key. Its accessibility
// service is on only while an agent works on the phone: the phone's apps
// can see it then, and banking apps refuse to sign in next to one.

// companionState is what the client knows of each phone's companion app.
type companionState struct {
	state      map[string]driver.CompanionState
	checked    map[string]time.Time // when CompanionState last answered, by phone
	loading    map[string]bool
	installing map[string]time.Time // installs under way, by when they began
	err        map[string]string    // why the last install failed
	// failure is the driver's own words for it, in English, for the
	// diagnostics of Send Feedback: err is the person's language, and
	// pinky.log's line about it may have scrolled out of the last lines.
	failure map[string]string
	later   map[string]bool // phones whose card the person put off
	// byHand: phones that refused a companion build over USB, which Pinky
	// leaves to the person, with the APK to install by hand
	// (companionByHand); kept in update.json, so Pinky doesn't try the
	// same build again after a restart. nil until refusals reads it.
	byHand map[string]update.CompanionByHand
	// apk and build are the APK Pinky installs and its versionCode (0 when
	// unknown), as Options.CompanionAPK found them; apkRead once it ran.
	apk                 string
	build               int
	apkRead, apkReading bool
}

// companionByHandDocs is how to install the companion app by hand, in the
// README, and companionByHandDocsZh in its Chinese version.
const (
	companionByHandDocs   = "https://github.com/DIYgod/pinky#install-companion-by-hand"
	companionByHandDocsZh = "https://github.com/DIYgod/pinky/blob/main/README.zh-CN.md#install-companion-by-hand"
)

// refusedOverUSB reports whether an install failed because the phone
// refuses installs over USB, or nobody allowed it on the phone in time:
// Xiaomi's Install via USB, for one, which some phones cannot turn on.
func refusedOverUSB(err error) bool {
	return err != nil && strings.Contains(err.Error(), "INSTALL_FAILED_USER_RESTRICTED")
}

// findCompanionAPK is the APK to show for an install by hand, "" when
// there is none, and its build; it reads the disk, so it runs off the main
// thread.
func (a *app) findCompanionAPK() (string, int) {
	if a.opt.CompanionAPK == nil {
		return "", 0
	}
	p, build, err := a.opt.CompanionAPK()
	if err != nil {
		return "", 0
	}
	return p, build
}

// companionAPK reports whether Pinky knows which companion APK it has,
// reading it in the background the first time.
func (a *app) companionAPK() bool {
	st := &a.comp
	if st.apkRead || a.opt.CompanionAPK == nil {
		return true
	}
	if !st.apkReading {
		st.apkReading = true
		go func() {
			p, build := a.findCompanionAPK()
			a.post(func() { st.apk, st.build, st.apkRead, st.apkReading = p, build, true, false })
		}()
	}
	return false
}

// refusals are the phones that refused a companion build over USB, read
// from update.json the first time.
func (a *app) refusals() map[string]update.CompanionByHand {
	st := &a.comp
	if st.byHand == nil {
		st.byHand = map[string]update.CompanionByHand{}
		if a.opt.Updates != nil {
			maps.Copy(st.byHand, a.opt.Updates.State().CompanionByHand)
		}
	}
	return st.byHand
}

// refusedUSB reports whether a phone refused the companion build Pinky has
// over USB, so Pinky leaves it to the person. A newer build gets one try:
// the person may have allowed installs over USB since.
func (a *app) refusedUSB(id string) bool {
	r, ok := a.refusals()[id]
	return ok && (a.comp.build == 0 || r.Build >= a.comp.build)
}

// noteRefused remembers that a phone refused the companion build over USB,
// in update.json too; seen when the person was there to see it (their own
// Update failed), else the sidebar tells them once (companionByHandCards).
func (a *app) noteRefused(id, name string, seen bool, apk string, build int) {
	st := &a.comp
	if apk != "" {
		st.apk, st.build, st.apkRead = apk, build, true
	}
	r := update.CompanionByHand{Phone: name, Build: st.build, Seen: seen}
	if old, ok := a.refusals()[id]; ok && old.Build == r.Build && old.Seen {
		r.Seen = true // told before about this build
	}
	a.saveRefusal(id, r)
}

// seenRefusal notes that the person dismissed a phone's sidebar card.
func (a *app) seenRefusal(id string) {
	if r, ok := a.refusals()[id]; ok {
		r.Seen = true
		a.saveRefusal(id, r)
	}
}

func (a *app) saveRefusal(id string, r update.CompanionByHand) {
	a.refusals()[id] = r
	if a.opt.Updates != nil {
		a.opt.Updates.Change(func(s *update.State) {
			if s.CompanionByHand == nil {
				s.CompanionByHand = map[string]update.CompanionByHand{}
			}
			s.CompanionByHand[id] = r
		})
	}
}

// forgetRefused drops a phone's refusal: it has a current companion, so
// the next build may install over USB, or the person removed the phone.
func (a *app) forgetRefused(id string) {
	if _, ok := a.refusals()[id]; !ok {
		return
	}
	delete(a.comp.byHand, id)
	if a.opt.Updates != nil {
		a.opt.Updates.Change(func(s *update.State) { delete(s.CompanionByHand, id) })
	}
}

// setCompanionState notes what CompanionState answered for a phone.
func (a *app) setCompanionState(id string, cs driver.CompanionState) {
	a.comp.state[id], a.comp.checked[id] = cs, time.Now()
	if cs == driver.CompanionOn || cs == driver.CompanionOff || cs == driver.CompanionReady {
		a.forgetRefused(id) // installed by hand, or the phone allows installs over USB now
	}
}

// companionByHand offers a phone that refused an install over USB the
// way to install the app by hand: the APK on this computer, and the
// README's steps.
func (a *app) companionByHand(c *mg.Context, d gateway.DeviceView) {
	if a.refusedUSB(d.Info.ID) {
		a.companionByHandLinks(c)
	}
}

// companionByHandLinks are the links to the APK and to the README's steps.
func (a *app) companionByHandLinks(c *mg.Context) {
	a.companionAPK()
	apk := a.comp.apk
	mg.Row(c).Gap(16).Wrap().AlignItems(mg.Center).Children(func() {
		if apk != "" {
			if linkButton(c, locales.CompanionShowAPK()).Tooltip(tildePath(apk)).Clicked() {
				a.reveal(apk)
			}
		}
		if linkButton(c, locales.CompanionByHandHow()).Clicked() {
			openURL(c, companionByHandLink(), func(error) {})
		}
	})
}

// companionByHandLink is the README's steps in the app's language: the
// Chinese README in Chinese, else the English one.
func companionByHandLink() string {
	if _, lang, ok := locales.Catalog.Message(i18n.Locale(), "companionByHandHow"); ok && strings.HasPrefix(lang, "zh") {
		return companionByHandDocsZh
	}
	return companionByHandDocs
}

// companionEvery is how often the client asks again while a phone's page
// shows, as the person may install or turn on the app on the phone.
const companionEvery = 30 * time.Second

// companionFor returns a phone's companion state, asking the gateway in the
// background when it is unknown or old; "" while unknown.
func (a *app) companionFor(d gateway.DeviceView) driver.CompanionState {
	ci, ok := a.api.(companionInstaller)
	if !ok || !d.Features.Companion {
		return ""
	}
	st := &a.comp
	id := d.Info.ID
	if !st.loading[id] && st.installing[id].IsZero() && time.Since(st.checked[id]) > companionEvery {
		st.loading[id] = true
		go func() {
			ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
			defer cancel()
			cs, err := ci.CompanionState(ctx, id)
			a.post(func() {
				st.loading[id] = false
				st.checked[id] = time.Now()
				if err == nil {
					a.setCompanionState(id, cs)
				}
			})
		}()
	}
	return st.state[id]
}

// installCompanion installs or updates the phone's companion app and checks
// that its service comes on. The phone may ask the person to allow the
// install, so it can take a while.
func (a *app) installCompanion(d gateway.DeviceView) {
	ci, ok := a.api.(companionInstaller)
	st := &a.comp
	id, name := d.Info.ID, d.Info.Name
	if !ok || !st.installing[id].IsZero() {
		return
	}
	st.installing[id] = time.Now()
	delete(st.err, id)
	delete(st.failure, id)
	// One signed with the old public key gets replaced: say so after.
	replacing := st.state[id] == driver.CompanionPublicKey
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 5*time.Minute)
		defer cancel()
		err := ci.InstallCompanion(ctx, id)
		// Only this wait running out means the install did not finish: a
		// shorter wait inside it (the service's) says so itself.
		timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
		cs, serr := ci.CompanionState(ctx, id)
		byHand, apk, build := refusedOverUSB(err), "", 0
		if byHand {
			apk, build = a.findCompanionAPK()
		}
		a.post(func() {
			delete(st.installing, id)
			if byHand {
				a.noteRefused(id, name, true, apk, build) // the card shows why
			}
			if serr == nil {
				a.setCompanionState(id, cs)
			}
			// A checkup that showed the app missing or off is stale now.
			if a.checkupOf(id) != nil {
				if dv := a.device(id); dv != nil {
					a.runCheckup(*dv)
				}
			}
			if err != nil {
				st.err[id] = companionError(err, a.chk.family[id], timedOut)
				st.failure[id] = err.Error()
				return
			}
			if replacing {
				a.toasts = append(a.toasts, locales.UpdateCompanionReplacedToast(name))
				return
			}
			a.toasts = append(a.toasts, locales.CompanionOnToast(name))
		})
	}()
}

// companionError says why installing the companion app failed and what to
// do, in the steps of the phone's maker (family, as its checkup read it;
// "" when unknown). timedOut: the install's own wait ran out, while the
// phone may have been asking the person to allow it.
func companionError(err error, family string, timedOut bool) string {
	msg := err.Error()
	var start *driver.CompanionStartError
	switch {
	case timedOut:
		return locales.CompanionErrTimeout()
	case strings.Contains(msg, "INSTALL_FAILED_USER_RESTRICTED") && family == "xiaomi":
		return locales.CompanionErrRestrictedXiaomi()
	case strings.Contains(msg, "INSTALL_FAILED_USER_RESTRICTED"):
		return locales.CompanionErrRestricted()
	case strings.Contains(msg, "INSTALL_FAILED_ABORTED"):
		return locales.CompanionErrDeclined()
	// Turning the app's service on writes a secure setting, which Xiaomi
	// phones refuse without USB debugging (Security settings) and ColorOS
	// with permission monitoring on.
	case strings.Contains(msg, "WRITE_SECURE_SETTINGS") && family == "xiaomi":
		return locales.CompanionErrSettingsXiaomi()
	case strings.Contains(msg, "WRITE_SECURE_SETTINGS") && family == "oppo":
		return locales.CompanionErrSettingsOppo()
	case strings.Contains(msg, "WRITE_SECURE_SETTINGS"):
		return locales.CompanionErrSettings(msg)
	case strings.Contains(msg, "INSTALL_FAILED_VERIFICATION_FAILURE") || strings.Contains(msg, "Play Protect"):
		return locales.CompanionErrPlayProtect()
	case strings.Contains(msg, "INSTALL_FAILED_INSUFFICIENT_STORAGE"):
		return locales.CompanionErrStorage()
	case errors.Is(err, driver.ErrOffline):
		return locales.CompanionErrOffline()
	// The app is installed, and its service did not come on.
	case errors.As(err, &start) && start.Reason == driver.CompanionStartUIAutomation:
		return locales.CompanionErrUIAutomation()
	case errors.As(err, &start) && start.Reason == driver.CompanionStartNotUnlocked:
		return locales.CompanionErrNotUnlocked()
	case errors.As(err, &start):
		return locales.CompanionErrNoAnswer(start.Detail)
	}
	return locales.CompanionErrOther(msg)
}

// companionCard offers to install, update or turn on the companion app on
// a phone's page.
func (a *app) companionCard(c *mg.Context, d gateway.DeviceView) {
	st := &a.comp
	id := d.Info.ID
	cs := a.companionFor(d)
	// auto: Pinky updates the app by itself (companionTick), without the
	// phone asking anything.
	auto := a.companionUpdating(id)
	busy := !st.installing[id].IsZero() || auto
	if st.later[id] && !busy {
		return
	}
	title, body, action, actionLabel, extra := "", "", "", "", ""
	switch cs {
	case driver.CompanionMissing:
		title, action = locales.CompanionInstallTitle(), locales.CompanionInstall()
		actionLabel = locales.CompanionInstallLabel(d.Info.Name)
		body = locales.CompanionInstallBody(d.Info.Name)
	case driver.CompanionOutdated, driver.CompanionPublicKey:
		title, action = locales.CompanionUpdateTitle(), locales.CompanionUpdate()
		actionLabel = locales.CompanionUpdateLabel(d.Info.Name)
		body = locales.CompanionUpdateBody()
		if cs == driver.CompanionPublicKey {
			// Why this update matters: anyone could sign one of the old.
			body = locales.CompanionPublicKeyBody()
		}
		extra = a.companionAutoLine(id)
	case driver.CompanionOff:
		title, action = locales.CompanionTurnOnTitle(), locales.CompanionTurnOn()
		actionLabel = locales.CompanionTurnOnLabel(d.Info.Name)
		body = locales.CompanionTurnOnBody()
	default:
		if !busy {
			return // on, ready, unknown, or no app to install
		}
		title = locales.CompanionInstallingTitle()
	}
	if auto {
		title = locales.CompanionUpdatingTitle()
	}
	p := pal(c)
	mg.Column(c.Key("companion-"+id)).Padding(14).Radius(12).Background(p.bg2).Border(1, p.line).Gap(8).
		Label(locales.CompanionCardLabel(d.Info.Name)).Children(func() {
		mg.Row(c).Gap(8).AlignItems(mg.Center).Children(func() {
			mg.Icon(c, icAndroid).FontSize(16).TextColor(p.ink2)
			mg.Text(c, title).FontSize(fsHeadline).FontWeight(fwSemibold).Grow(1).MinWidth(0)
		})
		if body != "" && !busy {
			mg.Text(c, body).FontSize(fsSmall).TextColor(p.ink2).LineHeight(1.4)
		}
		if extra != "" && !busy {
			mg.Text(c, extra).FontSize(fsSmall).TextColor(p.ink2)
		}
		if auto {
			mg.Progress(c, -1).FillWidth().Label(locales.CompanionUpdatingLabel())
			mg.Text(c, locales.CompanionUpdatingHint()).FontSize(fsSmall).TextColor(p.ink2)
			c.After(time.Second)
			return
		}
		if busy {
			mg.Progress(c, -1).FillWidth().Label(locales.CompanionInstalling())
			mg.Text(c, a.allowHint(id)).FontSize(fsBody).FontWeight(fwSemibold)
			c.After(time.Second)
			if time.Since(st.installing[id]) > 20*time.Second {
				mg.Text(c, locales.CompanionStillWaiting()).FontSize(fsSmall).TextColor(p.ink2)
			}
			return
		}
		if msg := st.err[id]; msg != "" {
			mg.Text(c, msg).FontSize(fsSmall).TextColor(p.red).Role(mg.RoleStatus)
		}
		a.companionByHand(c, d)
		mg.Row(c).Gap(12).AlignItems(mg.Center).Margin(4, 0, 0, 0).Children(func() {
			if button(c, action, btnOpt{kind: btnPrimary}).Label(actionLabel).Clicked() {
				a.installCompanion(d)
			}
			if quietButton(c, locales.CompanionNotNow()).Clicked() {
				st.later[id] = true
			}
		})
		mg.Text(c, a.allowHint(id)).FontSize(fsCaption).TextColor(p.ink3)
	})
}

// allowHint says what to do on the phone while Pinky installs its app:
// Xiaomi's phones ask for each install and give up after 10 seconds.
func (a *app) allowHint(id string) string {
	switch a.chk.family[id] {
	case "xiaomi":
		return locales.CompanionAllowHintXiaomi()
	case "oppo":
		return locales.CompanionAllowHintOppo()
	}
	return locales.CompanionAllowHint()
}

// companionLine is a phone's companion app in Settings › Phones.
func companionLine(cs driver.CompanionState) string {
	switch cs {
	case driver.CompanionOn:
		return locales.CompanionLineOn()
	case driver.CompanionReady:
		return locales.CompanionLineReady()
	case driver.CompanionOff:
		return locales.CompanionLineOff()
	case driver.CompanionMissing:
		return locales.CompanionLineMissing()
	case driver.CompanionOutdated:
		return locales.CompanionLineOutdated()
	case driver.CompanionPublicKey:
		return locales.CompanionLinePublicKey()
	case driver.CompanionNoAPK:
		return locales.CompanionLineNoAPK()
	}
	return ""
}

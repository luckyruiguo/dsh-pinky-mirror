package ui

import (
	"encoding/json"
	"runtime"

	"github.com/egoist/mygo"
	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/telemetry"
)

// usageStatsByDefault is whether usage statistics and crash reports start
// on. They are on, without a note on the first run; Settings › Privacy
// turns them off, and so does DO_NOT_TRACK (docs/TELEMETRY.md).
const usageStatsByDefault = true

// telState is the client's side of usage statistics, crash reports and
// feedback.
type telState struct {
	sharedOpen bool // "What Pinky shares"
	sharedText string

	fb feedbackState
}

func (a *app) tel() *telemetry.Client { return a.opt.Telemetry }

// setUsageStats is the switch in Settings › Privacy; the change goes
// through settingsQueue, so quick changes keep their order.
func (a *app) setUsageStats(on bool) {
	tel := a.tel()
	a.setq.push("usage", func() { tel.SetUsage(on) })
}

// openShared shows the usage report as it would go out now.
func (a *app) openShared() {
	r := a.tel().Report()
	data, _ := json.MarshalIndent(r, "", "  ")
	text := string(data)
	if len(r.Counts) == 0 {
		text += "\n\n" + locales.TelNothingCounted()
	}
	a.t.sharedOpen, a.t.sharedText = true, text
}

// telemetryDialogs are "What Pinky shares" and Send Feedback.
func (a *app) telemetryDialogs(c *mg.Context) {
	if a.tel() == nil {
		return
	}
	a.sharedDialog(c)
	a.feedbackDialog(c)
}

func (a *app) sharedDialog(c *mg.Context) {
	if !a.t.sharedOpen {
		return
	}
	p := pal(c)
	sheet(c, &a.t.sharedOpen, func() {
		mg.Column(c).Width(560).Gap(12).Label(locales.TelSharedTitle()).Children(func() {
			mg.Text(c, locales.TelSharedTitle()).FontSize(fsTitle3).FontWeight(fwSemibold).Role(mg.RoleHeading)
			mg.Text(c, locales.TelSharedDesc()).
				FontSize(fsSmall).TextColor(p.ink2)
			mg.Scroll(c).MaxHeight(320).Padding(10, 12).Radius(8).Background(p.bg2).Border(1, p.line).Children(func() {
				mg.Text(c, a.t.sharedText).Font("monospace").FontSize(12).Selectable()
			})
			mg.Row(c).Justify(mg.End).Children(func() {
				if button(c, locales.TelClose(), btnOpt{kind: btnPrimary}).Clicked() {
					a.t.sharedOpen = false
				}
			})
		})
	})
}

// privacyTelemetry is Settings › Privacy's part about usage statistics and
// crash reports.
func (a *app) privacyTelemetry(c *mg.Context) {
	tel := a.tel()
	if tel == nil {
		return
	}
	p := pal(c)
	st := &a.settings
	mg.Column(c).Gap(10).Label(locales.TelPrivacyGroup()).Children(func() {
		settingsHeading(c, locales.TelPrivacyTitle(), "")
		group(c).Children(func() {
			title := locales.TelShareUsage()
			if telemetry.DoNotTrack() {
				settingRow(c, title, locales.TelDoNotTrack(), false, nil)
			} else if a.toggleRow(c, "usage", title,
				locales.TelShareUsageDesc(),
				false, &st.UsageStats) {
				a.setUsageStats(st.UsageStats)
			}
			settingRow(c, locales.TelWhatsShared(), locales.TelWhatsSharedDesc(), true, func() {
				if button(c, locales.TelShow(), btnOpt{}).Label(locales.TelShowLabel()).Clicked() {
					a.openShared()
				}
			})
		})
		if !tel.CanSend() {
			mg.Text(c, locales.TelSendsNothing()).FontSize(fsCaption).TextColor(p.ink3)
		}
	})
}

// helpMenu is the menu bar's Help menu, with Send Feedback.
func (a *app) helpMenu() *mygo.MenuItem {
	if a.tel() == nil {
		return nil
	}
	items := []*mygo.MenuItem{
		{Label: locales.TelSendFeedbackMenu(), Click: func(*mygo.MenuItem, *mygo.Window) { a.openFeedback() }},
	}
	if runtime.GOOS == "darwin" {
		return &mygo.MenuItem{Role: mygo.RoleHelp, Submenu: items}
	}
	return &mygo.MenuItem{Label: locales.TelHelpMenu(), Submenu: items}
}

// feedbackLink is Send Feedback at the foot of Settings' sections.
func (a *app) feedbackLink(c *mg.Context) {
	if a.tel() == nil {
		return
	}
	p := pal(c)
	mg.Spacer(c)
	b := mg.ButtonBase(c.Key("send-feedback")).Height(32).Padding(0, 12).Radius(8).Justify(mg.Start).AlignItems(mg.Center).
		Label(locales.TelSendFeedbackLabel())
	col := p.ink2
	if b.Hovered() {
		b.Background(p.bg2)
		col = p.ink
	}
	if b.Clicked() {
		a.openFeedback()
	}
	b.TextColor(col).Children(func() { mg.Text(c, locales.TelSendFeedbackLink()).FontSize(fsBody) })
}

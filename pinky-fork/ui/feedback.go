package ui

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/telemetry"
)

// Feedback goes to GitHub: Send Feedback writes it here, then opens a new
// issue with it filled in, which the person reads and submits with their
// GitHub account (telemetry.IssueURL). The issue is public.

// feedbackState is the Send Feedback dialog's.
type feedbackState struct {
	open    bool
	title   string
	message string
	attach  bool // attach the diagnostics
	review  bool // the diagnostics show
	diag    telemetry.Diagnostics
	reading bool
	err     string
	seq     int
}

// openURL opens an address in the browser; e2e builds may log it instead.
var openURL = func(c *mg.Context, u string, done func(error)) { c.OpenURLThen(u, done) }

// openFeedback opens Send Feedback, from the Help menu, the menu bar
// icon's menu and Settings. A draft that was not sent stays.
func (a *app) openFeedback() {
	tel := a.tel()
	if tel == nil {
		return
	}
	fb := &a.t.fb
	if !fb.open && fb.message == "" && fb.title == "" {
		fb.attach = true
	}
	fb.open, fb.err = true, ""
	a.showMain()
	// The diagnostics are read again each time: the phones and the logs
	// change.
	fb.seq++
	seq := fb.seq
	fb.reading = true
	var phones []driver.Info
	for _, d := range a.devices {
		phones = append(phones, d.Info)
	}
	extra := a.diagnosticLines()
	tn, _ := a.api.(tailnetter)
	go func() {
		if tn != nil {
			ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
			extra = append(extra, tailnetDiagnostics(ctx, tn, phones)...)
			cancel()
		}
		d := tel.Diagnostics(phones, extra...)
		a.post(func() {
			if seq == fb.seq {
				fb.diag, fb.reading = d, false
			}
		})
	}()
	if a.main != nil {
		a.main.Invalidate()
	}
}

// diagnosticLines are what the client adds to the diagnostics: settings
// that change how Pinky behaves, and the video decoder.
func (a *app) diagnosticLines() []string {
	s := a.settings
	onOff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	lines := []string{fmt.Sprintf("Settings: preset %d · read-only %s · unlock from computer %s · hide codes %s · record %s · keep %s · remote agents %s · developer mode %s",
		s.Preset, onOff(s.ReadOnly), onOff(s.UnlockFromComputer), onOff(s.HideCodes), onOff(s.Record), keepLabelIn(locales.In("en"), s.KeepDays),
		onOff(s.RemoteAgents), onOff(s.DeveloperMode))}
	if v := videoLineIn(locales.In("en"), a); v != "" {
		lines = append(lines, "Video: "+v)
	}
	// Why the companion's service did not come on, as the phone showed it
	// (issue #56), for the phones connected now: telemetry puts their
	// tags in place of their ids.
	for _, id := range slices.Sorted(maps.Keys(a.comp.failure)) {
		if a.device(id) != nil {
			lines = append(lines, fmt.Sprintf("Companion on %s: %s", id, a.comp.failure[id]))
		}
	}
	return lines
}

// feedback is what the dialog holds, as telemetry.Feedback.
func (a *app) feedback() telemetry.Feedback {
	fb := &a.t.fb
	f := telemetry.Feedback{Title: fb.title, Message: fb.message, Version: a.tel().VersionLine()}
	if fb.attach && !fb.reading {
		d := fb.diag
		f.Diagnostics = &d
	}
	return f
}

// feedbackDialog is Send Feedback: a title, a message and the
// diagnostics, which the person can read, then GitHub.
func (a *app) feedbackDialog(c *mg.Context) {
	fb := &a.t.fb
	if !fb.open {
		return
	}
	p := pal(c)
	sheet(c, &fb.open, func() {
		mg.Column(c).Width(560).Gap(12).Label(locales.FeedbackTitle()).Children(func() {
			mg.Text(c, locales.FeedbackTitle()).FontSize(fsTitle3).FontWeight(fwSemibold).Role(mg.RoleHeading)
			mg.Text(c, locales.FeedbackIntro()).
				FontSize(fsSmall).TextColor(p.ink2)
			mg.TextInput(c.Key("fb-title"), &fb.title).Label(locales.FeedbackTitleField()).Placeholder(locales.FeedbackTitlePlaceholder())
			mg.TextArea(c.Key("fb-message"), &fb.message).Label(locales.FeedbackMessageField()).Placeholder(locales.FeedbackMessagePlaceholder()).Lines(5, 10).AutoFocus()
			cut := false
			mg.Column(c).Gap(4).Children(func() {
				mg.Checkbox(c, &fb.attach, locales.FeedbackAttach())
				if !fb.attach {
					return
				}
				mg.Row(c).Gap(8).Padding(0, 0, 0, 26).AlignItems(mg.Center).Children(func() {
					mg.Text(c, locales.FeedbackRedacted()).
						FontSize(fsCaption).TextColor(p.ink3).Grow(1).MinWidth(0)
					label, aria := locales.FeedbackReview(), locales.FeedbackReviewLabel()
					if fb.review {
						label, aria = locales.FeedbackHide(), locales.FeedbackHideLabel()
					}
					if button(c, label, btnOpt{small: true}).Label(aria).Clicked() {
						fb.review = !fb.review
					}
				})
				if fb.reading {
					return
				}
				_, cut = telemetry.IssueURL(a.feedback())
				if fb.review {
					mg.Scroll(c).MaxHeight(240).Padding(10, 12).Radius(8).Background(p.bg2).Border(1, p.line).Children(func() {
						mg.Text(c, fb.diag.String()).Font("monospace").FontSize(11.5).Selectable()
					})
				}
			})
			note := locales.FeedbackNote()
			if cut {
				note = locales.SentencePair(note, locales.FeedbackCut())
			}
			mg.Text(c, note).FontSize(fsCaption).TextColor(p.ink3)
			if fb.err != "" {
				mg.Text(c, fb.err).FontSize(fsSmall).TextColor(p.red).Role(mg.RoleStatus)
			}
			mg.Row(c).Gap(8).Justify(mg.End).Children(func() {
				if button(c, locales.FeedbackCopy(), btnOpt{icon: icCopy}).Label(locales.FeedbackCopyLabel()).Tooltip(locales.FeedbackCopyTooltip()).Clicked() {
					c.WriteClipboard(a.feedback().Text())
					a.toasts = append(a.toasts, locales.FeedbackCopied())
				}
				if button(c, locales.FeedbackCancel(), btnOpt{}).Clicked() {
					fb.open = false
				}
				open := button(c, locales.FeedbackOpen(), btnOpt{kind: btnPrimary})
				switch {
				case strings.TrimSpace(fb.message) == "" || (fb.attach && fb.reading):
					open.Disabled(true)
				case open.Clicked():
					a.openIssue(c)
				}
			})
		})
	})
}

// openIssue opens the new issue in the browser and closes the dialog.
func (a *app) openIssue(c *mg.Context) {
	fb := &a.t.fb
	u, _ := telemetry.IssueURL(a.feedback())
	fb.err = ""
	openURL(c, u, func(err error) {
		if err != nil {
			fb.err = locales.FeedbackBrowserFailed(err.Error())
			return
		}
		*fb = feedbackState{seq: fb.seq}
		a.toasts = append(a.toasts, locales.FeedbackThanks())
	})
}

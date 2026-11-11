package ui

import (
	"math"
	"strings"
	"time"

	"github.com/egoist/mygo"
	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/gateway"
)

const (
	panelW = 400
	// panelH is the floating panel's height without a screenshot.
	panelH = 330
	// panelChrome is the room around the screenshot in the floating
	// panel: the countdown, the phone, a title and a detail of two lines
	// each, and the buttons or the answer under them.
	panelChrome = 254
	// panelFigure is the screenshot's height for a portrait screen; a
	// landscape one takes the width, at most panelWide high, with its
	// zoom, panelLens high, under it.
	panelFigure = 196
	panelWide   = 180
	panelLens   = 96
)

// panelHeight is the floating panel's height for need n, which it asks or
// confirms: room for words of as many lines as they may take, and the
// screenshot, which grows into what shorter words leave.
func panelHeight(n gateway.NeedView) int {
	if n.Screenshot == nil || n.Kind != gateway.NeedApproval || newAgentNeed(n) {
		return panelH
	}
	b := n.Screenshot.Bounds()
	_, zoom := marksOf(n).focus(b.Dx(), b.Dy())
	fig := figureHeight(panelW-32, panelFigure, panelWide, panelLens, b.Dx(), b.Dy(), zoom)
	return panelChrome + int(math.Ceil(float64(fig)))
}

// panelShows returns what the floating panel shows: the last answer for a
// moment, else the approval at panelIdx.
func (a *app) panelShows() (gateway.NeedView, *answer, bool) {
	asks := a.approvals()
	answers := a.liveAnswers()
	// The last answer shows until it times out, unless more wait.
	if len(answers) > 0 && (len(asks) == 0 || time.Until(answers[len(answers)-1].until) > 1500*time.Millisecond) {
		x := answers[len(answers)-1]
		return x.need, &x, true
	}
	if len(asks) == 0 {
		return gateway.NeedView{}, nil, false
	}
	return asks[min(max(a.panelIdx, 0), len(asks)-1)], nil, true
}

// panelSize is the floating panel's size for what it shows, within its
// display.
func (a *app) panelSize() (int, int) {
	h := panelH
	if n, _, ok := a.panelShows(); ok {
		h = panelHeight(n)
	}
	if a.panelMax > 0 {
		h = min(h, max(a.panelMax, panelH))
	}
	return panelW, h
}

// fitPanel keeps the floating panel at panelSize.
func (a *app) fitPanel() {
	if a.panel == nil {
		return
	}
	w, h := a.panelSize()
	if cw, ch := a.panel.Size(); cw != w || ch != h {
		a.panel.SetSize(w, h)
	}
}

// answer is an approval the person just answered, which the floating
// panel confirms for a moment.
type answer struct {
	need  gateway.NeedView
	d     gateway.Decision
	note  string
	until time.Time
}

func (a *app) remember(n gateway.NeedView, d gateway.Decision, note string) {
	if d == gateway.TakeOver {
		return
	}
	a.answered = append(a.answered, answer{need: n, d: d, note: note, until: time.Now().Add(2500 * time.Millisecond)})
	delete(a.noting, n.ID)
}

func (a *app) liveAnswers() []answer {
	now := time.Now()
	out := a.answered[:0]
	for _, x := range a.answered {
		if now.Before(x.until) {
			out = append(out, x)
		}
	}
	a.answered = out
	return out
}

// newPanel makes the floating approval panel: a panel window that floats
// over every app and shows without taking the keyboard.
func (a *app) newPanel() {
	a.panel = mygo.NewWindow(mygo.WindowOptions{
		Title: locales.PanelWindowTitle(), Width: panelW, Height: panelH,
		Hidden: true, Panel: true, Frameless: true, Transparent: true, CornerRadius: 14,
		DisableResize: true, DisableMinimize: true, DisableMaximize: true, DisableFullScreen: true,
		SkipTaskbar: true, AlwaysOnTop: true,
		Content: mg.View(a.panelView),
	})
	a.panel.OnClose(func(e *mygo.CloseEvent) {
		if !a.quitting {
			e.PreventDefault()
			a.panel.Hide()
		}
	})
}

// syncPanel shows the panel while approvals wait and the person cannot see
// them in the main window, and hides it otherwise. It never takes the
// keyboard: the person clicks it first, and then Return allows and Esc
// denies.
func (a *app) syncPanel() {
	if a.panel == nil {
		return
	}
	a.panelPage = a.page
	want := (len(a.approvals()) > 0 || len(a.liveAnswers()) > 0) && !a.needsInFront()
	switch {
	case want && !a.panel.IsVisible():
		a.placePanel()
		a.fitPanel()
		a.panel.ShowInactive()
	case !want && a.panel.IsVisible():
		a.panel.Hide()
		a.closeShot()
	}
	a.panel.Invalidate()
}

// needsInFront reports whether the person sees the Needs you column: the
// main window is in front and shows the Phones page with a phone. Other
// pages, and the Phones page before any phone is added, have no such
// column, so their approvals go to the panel.
func (a *app) needsInFront() bool {
	return a.main != nil && a.main.IsVisible() && !a.main.IsMinimized() && a.focused &&
		a.page == pagePhones && len(a.devices) > 0
}

// showPanel brings the panel up, from a notification or the flyout.
func (a *app) showPanel(focus bool) {
	if len(a.approvals()) == 0 {
		a.showMain()
		return
	}
	if !a.panel.IsVisible() {
		a.placePanel()
		a.fitPanel()
	}
	if focus {
		a.panel.Show()
	} else {
		a.panel.ShowInactive()
	}
}

// placePanel puts the panel at the top right of the display the pointer
// is on, where notifications show.
func (a *app) placePanel() {
	d := mygo.Screen.DisplayNearestPoint(mygo.Screen.CursorScreenPoint())
	wa := d.WorkArea
	a.panelMax = wa.Height - 32
	a.panel.SetPosition(wa.X+wa.Width-panelW-16, wa.Y+16)
}

// panelView builds the floating panel.
func (a *app) panelView(c *mg.Context) {
	p := applyTheme(c)
	c.Root().Background(mg.Transparent)
	asks := a.approvals()
	answers := a.liveAnswers()
	if len(answers) > 0 {
		c.After(200 * time.Millisecond)
	}
	if len(asks) == 0 && len(answers) == 0 {
		mg.Column(c).Fill().Radius(14).Background(p.panel).Border(1, p.line2)
		if a.main != nil {
			a.main.Update(a.syncPanel) // nothing left to show: hide
		}
		return
	}
	if a.panelIdx >= len(asks) {
		a.panelIdx = max(0, len(asks)-1)
	}
	a.fitPanel()
	card := mg.Column(c).Fill().Radius(14).Background(p.amberTint).Border(1, p.amberLine).Clip()
	n, x, _ := a.panelShows()
	if x != nil {
		card.Children(func() { a.panelAnswered(c, *x) })
		return
	}
	// The large view shows the approval the panel shows.
	if a.viewing != "" && a.viewing != n.ID {
		a.post(func() {
			if a.viewing == "" {
				return
			}
			if n.Screenshot != nil {
				a.openShot(n)
			} else {
				a.closeShot()
			}
		})
	}
	noting := a.noting[n.ID]
	plain := n.Kind == gateway.NeedApproval && !newAgentNeed(n)
	if !plain {
		// Handoffs and agent requests have no Return and Esc: their
		// buttons are not Allow and Deny.
	} else if !noting {
		if c.Shortcut(0, mg.KeyEnter) {
			a.decide(n, gateway.AllowOnce, "")
		}
		if c.Shortcut(0, mg.KeyEscape) {
			a.decide(n, gateway.Deny, "")
		}
	} else if c.Shortcut(0, mg.KeyEscape) {
		delete(a.noting, n.ID)
	}
	card.Children(func() {
		// The time left, as a bar along the top.
		left := time.Until(n.Deadline)
		total := n.Deadline.Sub(n.Created)
		frac := float32(1)
		if total > 0 && !n.Deadline.IsZero() {
			frac = float32(math.Max(0, math.Min(1, left.Seconds()/total.Seconds())))
		}
		mg.Box(c).Height(3).FillWidth().Background(p.amberLine).Draw(func(pt *mg.Painter, r mg.Rect) {
			pt.Fill(mg.Rect{X: r.X, Y: r.Y, W: r.W * frac, H: r.H}, p.amberDot, 0)
		})
		if !n.Deadline.IsZero() {
			c.After(250 * time.Millisecond)
		}
		mg.Column(c).Grow(1).MinHeight(0).Padding(12, 16, 16, 16).Gap(2).Children(func() {
			mg.Row(c).MinHeight(24).Gap(8).AlignItems(mg.Center).Children(func() {
				if len(asks) > 1 {
					mg.Row(c).Gap(2).AlignItems(mg.Center).Role(mg.RoleGroup).Label(locales.PanelWaitingApprovals()).Children(func() {
						if arrowButton(c, icChevL, locales.PanelPrevious(), a.panelIdx == 0).Clicked() {
							a.panelIdx--
						}
						mg.Text(c, locales.PanelPosition(a.panelIdx+1, len(asks))).FontSize(fsSmall).FontWeight(fwSemibold).TextColor(p.ink2)
						if arrowButton(c, icChevR, locales.PanelNext(), a.panelIdx >= len(asks)-1).Clicked() {
							a.panelIdx++
						}
					})
				}
				mg.Spacer(c)
				if !n.Deadline.IsZero() {
					secs := time.Duration(math.Ceil(max(0, left.Seconds()))) * time.Second
					word := locales.PanelDeniesIn(elapsed(secs))
					if n.Kind == gateway.NeedHandoff {
						word = locales.PanelEndsIn(elapsed(secs))
					}
					mg.Text(c, word).FontSize(fsSmall).TextColor(p.ink2)
				}
			})
			needEyebrow(c, n)
			mg.Text(c, needTitle(n)).FontSize(fsHeadline).FontWeight(fwSemibold).LineHeight(1.3).MaxLines(2).Margin(6, 0, 0, 0).Role(mg.RoleHeading)
			detail := needDetail(n)
			if newAgentNeed(n) {
				_, picker := a.api.(agentScoper)
				detail = agentRequestDetail(n, picker)
			}
			if detail != "" {
				mg.Text(c, detail).FontSize(fsSmall).TextColor(p.ink2).MaxLines(2)
			}
			switch {
			case n.Kind == gateway.NeedHandoff:
				mg.Spacer(c)
				a.handoffControls(c, n)
				return
			case newAgentNeed(n):
				mg.Spacer(c)
				a.agentRequestControls(c, n)
				return
			case n.Unblock:
				// Return and Esc still allow (for the task) and deny.
				mg.Spacer(c)
				a.unblockControls(c, n)
				return
			}
			switch {
			case noting:
				mg.Spacer(c)
			case n.Screenshot != nil:
				// The screenshot takes the room the words leave.
				a.approvalFigure(c, n, func() { a.post(func() { a.openShot(n) }) }).
					Margin(12, 0, 0, 0).Grow(1).MinHeight(90)
			default:
				a.missingShotNote(c, n)
				mg.Spacer(c)
			}
			if noting {
				mg.Text(c, locales.NeedsNoteFor(agentOf(n))).FontSize(fsSmall).FontWeight(fwSemibold).Margin(10, 0, 0, 0)
				note := a.notes[n.ID]
				mg.TextArea(c.Key("pnote-"+n.ID), &note).Label(locales.NeedsNoteFor(agentOf(n))).Lines(2, 3).Margin(6, 0, 0, 0).AutoFocus()
				a.notes[n.ID] = note
				mg.Grid(c).Columns(2).Gap(8).Margin(8, 0, 0, 0).Children(func() {
					if button(c, locales.PanelCancel(), btnOpt{}).Clicked() {
						delete(a.noting, n.ID)
					}
					if button(c, locales.PanelDenyWithNote(), btnOpt{kind: btnPrimary}).Clicked() {
						a.decide(n, gateway.Deny, strings.TrimSpace(note))
					}
				})
				return
			}
			mg.Grid(c).Columns(2).Gap(8).Margin(12, 0, 0, 0).Children(func() {
				if button(c, locales.NeedsDeny(), btnOpt{}).Clicked() {
					a.decide(n, gateway.Deny, "")
				}
				if button(c, locales.NeedsAllow(), btnOpt{kind: btnPrimary}).Clicked() {
					a.decide(n, gateway.AllowOnce, "")
				}
			})
			linkButton(c, locales.NeedsMoreOptions()).Margin(10, 0, 0, 0).AlignSelf(mg.Start).PopoverMenuButton(func(m *mg.Menu) {
				// As on the card (approvalControls): each only where the
				// gateway keeps it.
				if n.AllowTask && m.Item(locales.NeedsAllowTask()).Chosen() {
					a.decide(n, gateway.AllowTask, "")
				}
				if n.AlwaysAllow && m.Item(alwaysAllowLabel(n)).Chosen() {
					a.decide(n, gateway.AllowAlways, "")
				}
				if m.Item(locales.PhoneTakeOver()).Chosen() {
					a.decide(n, gateway.TakeOver, "")
				}
				if m.Item(locales.NeedsDenyWithNote()).Chosen() {
					a.noting[n.ID] = true
				}
			})
		})
	})
}

// panelAnswered confirms an answer, as the prototype's allowed and denied
// states do.
func (a *app) panelAnswered(c *mg.Context, x answer) {
	p := pal(c)
	mg.Column(c).Grow(1).MinHeight(0).Padding(16).Gap(2).Children(func() {
		needEyebrow(c, x.need)
		mg.Text(c, needTitle(x.need)).FontSize(fsHeadline).FontWeight(fwSemibold).LineHeight(1.3).MaxLines(2).Margin(6, 0, 0, 0)
		mg.Text(c, needDetail(x.need)).FontSize(fsSmall).TextColor(p.ink2).MaxLines(2)
		if x.need.Screenshot != nil && x.need.Kind == gateway.NeedApproval && !newAgentNeed(x.need) {
			// What was answered, in the room the panel had for it.
			a.approvalFigure(c, x.need, nil).Margin(12, 0, 0, 0).Grow(1).MinHeight(60)
		}
		mg.Column(c).Margin(14, 0, 0, 0).Gap(4).Children(func() {
			title, detail := "", ""
			ic, col := icCheck, p.green
			kind, app := x.need.Rule, x.need.App
			agent := agentOf(x.need)
			withNote := func(s string) string {
				if x.note != "" {
					return locales.PanelWithNote(s, x.note)
				}
				return s
			}
			switch {
			case x.need.Kind == gateway.NeedHandoff && x.d == gateway.AllowOnce:
				title, detail = locales.PanelHandedBack(), withNote(locales.PanelHandedBackDetail(agent))
			case x.need.Kind == gateway.NeedHandoff:
				ic, col = icX, p.ink2
				title, detail = locales.PanelDeclined(), withNote(locales.PanelToldNo(agent))
			case newAgentNeed(x.need) && x.d == gateway.Deny:
				ic, col = icX, p.ink2
				title, detail = locales.PanelDenied(), locales.PanelAgentDenied(agent)
			case newAgentNeed(x.need):
				title, detail = locales.PanelAllowed(), locales.PanelAgentAllowed(agent)
			case x.need.Unblock && x.d != gateway.Deny:
				title, detail = locales.PanelAllowedTask(), locales.PanelUnblockedDetail(agent, nonEmpty(app, locales.PanelThisApp()))
			}
			switch {
			case title != "":
			default:
				switch x.d {
				case gateway.AllowOnce:
					title, detail = locales.PanelAllowedOnce(), locales.PanelAllowedOnceDetail()
				case gateway.AllowTask:
					title = locales.PanelAllowedTask()
					detail = locales.PanelAllowedTaskDetail(nonEmpty(ruleName(kind), locales.PanelActionsLikeThis()), nonEmpty(app, locales.PanelThisApp()))
				case gateway.AllowAlways:
					title = locales.PanelAlwaysAllowed()
					detail = alwaysAllowedDetail(x.need)
					if detail == "" {
						detail = locales.PanelAlwaysAllowedDetail(nonEmpty(ruleName(kind), locales.PanelActionsLikeThis()), nonEmpty(app, locales.PanelThisApp()))
					}
				default:
					ic, col = icX, p.ink2
					title = locales.PanelDenied()
					detail = locales.PanelDeniedDetail(agentOf(x.need))
					if x.note != "" {
						detail = locales.PanelDeniedDetailNote(agentOf(x.need), x.note)
					}
				}
			}
			mg.Row(c).Gap(8).AlignItems(mg.Center).Children(func() {
				mg.Icon(c, ic).FontSize(18).TextColor(col)
				mg.Text(c, title).FontWeight(fwSemibold)
			})
			mg.Text(c, detail).FontSize(fsSmall).TextColor(p.ink2)
		})
	})
}

func nonEmpty(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

func arrowButton(c *mg.Context, ic *mg.SVG, label string, disabled bool) mg.Element {
	p := pal(c)
	b := mg.ButtonBase(c).Size(24, 24).Radius(6).Center().Label(label).Disabled(disabled)
	col := p.ink2
	if disabled {
		b.Opacity(0.35)
	} else if b.Hovered() {
		b.Background(p.line)
		col = p.ink
	}
	b.TextColor(col).Children(func() { mg.Icon(c, ic).FontSize(15) })
	return b
}

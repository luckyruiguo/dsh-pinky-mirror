package ui

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/gateway"
)

// activityState is the Activity screen's: finished tasks by day.
type activityState struct {
	tasks   []gateway.TaskView
	loaded  bool
	query   string
	asked   string // the query of the last load
	sel     string
	replay  bool
	at      int // the step the replay shows
	playing bool
	nextAt  time.Time
	seq     int
	lastRun time.Time
}

func (s *activityState) init() {}

// load reads History again, with the search.
func (s *activityState) load(a *app) {
	s.seq++
	seq := s.seq
	q := s.query
	s.asked = q
	s.lastRun = time.Now()
	go func() {
		tasks, err := a.api.History(a.ctx, gateway.HistoryQuery{Search: q, Limit: 200})
		a.post(func() {
			if seq != s.seq {
				return
			}
			if err != nil {
				a.toasts = append(a.toasts, locales.ActivityReadFailed(err.Error()))
				return
			}
			s.tasks, s.loaded = tasks, true
			found := false
			for _, t := range tasks {
				found = found || t.ID == s.sel
			}
			if !found {
				s.sel = ""
				if len(tasks) > 0 {
					s.sel = tasks[0].ID
				}
			}
		})
	}()
}

func (a *app) activityPage(c *mg.Context) {
	p := pal(c)
	s := &a.act
	if s.seq == 0 {
		s.load(a) // first shown
	}
	// Search as the person types, once they pause.
	if s.query != s.asked && time.Since(s.lastRun) > 250*time.Millisecond {
		s.load(a)
	} else if s.query != s.asked {
		c.After(260 * time.Millisecond)
	}
	// Tasks that finish while Activity shows join it.
	if s.loaded && a.finishedSince(s.lastRun) {
		s.load(a)
	}
	pageHeader(c, func() {
		pageTitle(c, locales.ActivityTitle())
		mg.Spacer(c)
		if button(c, locales.ExportAll(), btnOpt{icon: icExport, small: true}).Tooltip(locales.ExportAllTooltip()).Clicked() {
			a.exportHistory(nil, locales.ExportFolderAll())
		}
		mg.Row(c).Width(280).Height(32).Padding(0, 10).Radius(rControl).Background(p.bg2).Border(1, p.line).Gap(sp2).AlignItems(mg.Center).Children(func() {
			mg.Icon(c, icSearch).FontSize(15).TextColor(p.ink3)
			mg.TextInputBase(c.Key("act-search"), &s.query).Grow(1).Placeholder(locales.ActivitySearchPlaceholder()).Label(locales.ActivitySearchLabel()).FontSize(fsBody)
		})
	})
	mg.Row(c).Grow(1).MinHeight(0).AlignItems(mg.Stretch).Children(func() {
		mg.Scroll(c).Basis(400).Grow(1).MinWidth(320).MaxWidth(520).BorderWidth(0, 1, 0, 0).BorderColor(p.line).Padding(sp3, sp3).Gap(2).
			Label(locales.ActivityTasksLabel()).Children(func() {
			if !s.loaded {
				mg.Box(c).Padding(sp5).Center().Children(func() { spinner(c) })
				return
			}
			if len(s.tasks) == 0 {
				msg := locales.ActivityEmpty()
				if s.query != "" {
					msg = locales.ActivityNoMatch(s.query)
				}
				ic := icHistory
				if s.query != "" {
					ic = icSearch
				}
				emptyWell(c, ic, msg, nil)
				return
			}
			now := c.Now()
			group := ""
			for _, t := range s.tasks {
				if g := dayLabel(t.Started, now); g != group {
					group = g
					pad := float32(sp4)
					if group == dayLabel(s.tasks[0].Started, now) {
						pad = sp1
					}
					mg.Text(c, group).FontSize(fsCaption).FontWeight(fwSemibold).TextColor(p.ink3).Padding(pad, rowInset, 6, rowInset)
				}
				a.activityRow(c.Key(t.ID), t)
			}
		})
		mg.Scroll(c).Basis(460).Grow(999).MinWidth(0).Padding(sp6, sp8).Gap(sp5).Label(locales.ActivityDetailLabel()).Children(func() {
			var t *gateway.TaskView
			for i := range s.tasks {
				if s.tasks[i].ID == s.sel {
					t = &s.tasks[i]
				}
			}
			if t == nil {
				if s.loaded {
					mg.Text(c, locales.ActivityChooseTask()).FontSize(fsBody).TextColor(p.ink3).AlignSelf(mg.Center).Margin(sp8, 0, 0, 0)
				}
				return
			}
			a.activityDetail(c, *t)
		})
	})
}

// finishedSince reports whether a task ended after t.
func (a *app) finishedSince(t time.Time) bool {
	for _, x := range a.tasks {
		if !x.Ended.IsZero() && x.Ended.After(t) {
			return true
		}
	}
	return false
}

func outcome(c *mg.Context, t gateway.TaskView) (string, mg.Element) {
	p := pal(c)
	switch {
	case t.State == gateway.TaskStopped:
		return locales.ActivityOutcomeStopped(), dot(c, p.ink3, false)
	case t.State == gateway.TaskFailed:
		return locales.ActivityOutcomeFailed(), dot(c, p.red, false)
	case denied(t) && !strings.HasPrefix(lastText(t), "You allowed"):
		if allowedLater(t) {
			return locales.ActivityOutcomeDone(), dot(c, p.green, false)
		}
		return locales.ActivityOutcomeDenied(), hollowDot(c)
	}
	return locales.ActivityOutcomeDone(), dot(c, p.green, false)
}

// answered reports whether a step is the person's answer to an approval,
// as the gateway and the fake word it.
func answered(s gateway.StepView) bool {
	return strings.HasPrefix(s.Text, "You allowed") || strings.HasPrefix(s.Text, "You denied") ||
		s.Status == "approval_denied" && strings.HasPrefix(s.Text, "Waiting for you: ")
}

func lastText(t gateway.TaskView) string {
	if len(t.Steps) == 0 {
		return ""
	}
	return t.Steps[len(t.Steps)-1].Text
}

// allowedLater reports whether the person allowed something after a
// denial, as when the agent fixed what they objected to.
func allowedLater(t gateway.TaskView) bool {
	seenDeny := false
	for _, s := range t.Steps {
		if strings.HasPrefix(s.Text, "You denied") || s.Status == "approval_denied" {
			seenDeny = true
		}
		if seenDeny && strings.HasPrefix(s.Text, "You allowed") {
			return true
		}
	}
	return false
}

func (a *app) activityRow(c *mg.Context, t gateway.TaskView) {
	p := pal(c)
	on := a.act.sel == t.ID
	row := mg.ButtonBase(c).Padding(10, sp3, 10, rowInset).Radius(rControl).Gap(sp3).AlignItems(mg.Center).Justify(mg.Start).FillWidth().Label(t.Title)
	switch {
	case on:
		row.Background(p.bg3)
		selectionMark(c, row)
	case row.Hovered():
		row.Background(p.bg2)
	}
	if row.Clicked() {
		a.act.sel, a.act.replay = t.ID, false
		a.closeRecording()
	}
	app := appOf(t)
	row.Children(func() {
		appIcon(c, nonEmpty(app, t.Title), 32, rControl, 14, "")
		mg.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			mg.Text(c, t.Title).FontSize(fsBody).FontWeight(fwSemibold).SingleLine()
			sub := strings.Join(nonBlank(t.Agent, t.DeviceName, t.Started.Format("15:04")), " · ")
			mg.Text(c, sub).FontSize(fsCaption).TextColor(p.ink2).SingleLine()
		})
		mg.Column(c).AlignItems(mg.End).Gap(2).Shrink(0).Children(func() {
			mg.Row(c).Gap(6).AlignItems(mg.Center).Children(func() {
				word, _ := outcome(c, t)
				mg.Text(c, word).FontSize(fsSmall)
			})
			if n := approvals(t); n > 0 {
				mg.Text(c, locales.ActivityApprovals(n)).FontSize(fsCaption).TextColor(p.ink3)
			}
		})
	})
}

func nonBlank(s ...string) []string {
	var out []string
	for _, v := range s {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// summaryOf is the summary the agent gave phone_release, if any.
func summaryOf(t gateway.TaskView) string {
	sum := ""
	for _, s := range t.Steps {
		if s.Args == "" {
			continue
		}
		var a struct {
			Summary string `json:"summary"`
		}
		if json.Unmarshal([]byte(s.Args), &a) == nil && a.Summary != "" {
			sum = a.Summary
		}
	}
	return sum
}

func (a *app) activityDetail(c *mg.Context, t gateway.TaskView) {
	p := pal(c)
	app := appOf(t)
	col := appColor(nonEmpty(app, t.Title))
	mg.Row(c).Gap(14).AlignItems(mg.Start).Wrap().Children(func() {
		appIcon(c, nonEmpty(app, t.Title), 44, 10, 19, col)
		mg.Column(c).Basis(220).Grow(1).MinWidth(0).Gap(sp1).Children(func() {
			mg.Text(c, t.Title).FontSize(fsTitle2).FontWeight(fwSemibold).LineHeight(1.3).Role(mg.RoleHeading)
			when := dayLabel(t.Started, c.Now()) + " " + t.Started.Format("15:04")
			meta := nonBlank(t.Agent, t.DeviceName, when, elapsed(taskDuration(t, c.Now())))
			mg.Text(c, strings.Join(meta, " · ")).FontSize(fsSmall).TextColor(p.ink2)
		})
		mg.Row(c).Gap(sp2).Children(func() {
			rec := a.recordingOf(t.ID)
			switch {
			case rec != nil:
				if button(c, locales.RecordingWatch(), btnOpt{icon: icPlay}).Clicked() {
					a.act.replay, a.act.playing = true, false
					a.openRecording(t.ID, *rec)
				}
			default:
				if button(c, locales.ReplayWatch(), btnOpt{icon: icPlay}).Tooltip(locales.ReplayWatchTooltip()).Clicked() {
					a.act.replay, a.act.at, a.act.playing = true, 0, false
					a.closeRecording()
				}
			}
			more := button(c, "", btnOpt{icon: icMore, label: locales.ActivityMoreActions()}).Width(34).Padding(0)
			more.PopoverMenuButton(func(m *mg.Menu) {
				if m.Item(locales.ExportTask()).Chosen() {
					a.exportHistory([]string{t.ID}, t.Title)
				}
				if rec != nil && m.Item(locales.ReplayMenuItem()).Chosen() {
					a.act.replay, a.act.at, a.act.playing = true, 0, false
					a.closeRecording()
				}
				m.Separator()
				m.Item(locales.ActivityDelete()).Disabled(true)
			})
		})
	})
	if sum := summaryOf(t); sum != "" {
		mg.Text(c, sum).FontSize(fsHeadline).LineHeight(1.55).MaxWidth(560)
	}
	if a.act.replay {
		if pl := a.rec.pl; pl != nil && pl.task == t.ID {
			a.recordingPanel(c, t, pl)
		} else {
			a.replayView(c, t)
		}
		return
	}
	mg.Column(c).Children(func() {
		sectionLabel(c, locales.ActivitySteps())
		n := len(t.Steps)
		mg.Column(c).Role(mg.RoleList).Children(func() {
			for i, s := range t.Steps {
				text, sub, kind := stepDisplay(t, i)
				you := kind == stepYou
				if sub == "" && you && s.Elapsed >= time.Second && answered(s) {
					sub = locales.ActivityAnsweredIn(elapsed(s.Elapsed))
				}
				key := fmt.Sprintf("act/%s/%d", t.ID, s.Index)
				shot := a.hasShot(s)
				open := shot && a.previews[key]
				mg.Row(c.Key(i)).Gap(sp4).AlignItems(mg.Stretch).Role(mg.RoleListItem).Children(func() {
					// The rail: the step's picture, or the person's mark, on
					// a line that joins the steps.
					rail := mg.Column(c).Width(stepRail).Shrink(0).AlignItems(mg.Center).Padding(sp1, 0)
					rail.Draw(func(pt *mg.Painter, r mg.Rect) {
						top, bot, mid := r.Y, r.Y+r.H, r.Y+sp1+stepThumbH/2
						if i == 0 {
							top = mid
						}
						if i == n-1 {
							bot = mid
						}
						if bot > top {
							pt.Fill(mg.Rect{X: r.X + r.W/2 - 1, Y: top, W: 2, H: bot - top}, p.line, 1)
						}
					})
					rail.Children(func() {
						switch {
						case you:
							mg.Box(c).Size(stepRail, stepThumbH).Center().Children(func() {
								mg.Box(c).Size(26, 26).Radius(13).Background(p.bg3).Border(2, p.bg).Center().Children(func() {
									mg.Icon(c, icPerson).FontSize(14).TextColor(p.ink2)
								})
							})
						case shot:
							b := mg.ButtonBase(c).Radius(rSmall).Shrink(0).Label(locales.ActivityStepShotLabel(text)).Expanded(open).
								Tooltip(locales.ActivityShowShot())
							if b.Clicked() {
								a.previews[key] = !open
							}
							b.Children(func() { a.shotView(c, t, s, stepThumbH) })
						default:
							stepThumb(c, col, stepThumbH)
						}
					})
					mg.Column(c).Grow(1).MinWidth(0).Padding(sp1, 0).Gap(sp2).Children(func() {
						mg.Column(c).MinHeight(stepThumbH).Justify(mg.Center).Gap(2).Children(func() {
							tx := mg.Text(c, text).FontSize(fsBody).LineHeight(1.4)
							if you {
								tx.FontWeight(fwSemibold)
							}
							if sub != "" {
								mg.Text(c, sub).FontSize(fsSmall).TextColor(p.ink2)
							}
						})
						if open {
							a.shotView(c, t, s, 420).Margin(0, 0, sp2, 0)
						}
					})
				})
			}
		})
	})
}

// A step's picture on Activity's rail is stepThumbH high, and the rail
// stepRail wide.
const (
	stepThumbH = 48
	stepRail   = 28
)

// stepThumb is the small phone sketch beside an agent's step, h high,
// where there is no screenshot to show: the app's color at the top, a few
// lines under it.
func stepThumb(c *mg.Context, col string, h float32) mg.Element {
	p := pal(c)
	w := float32(int(h*9/19.5 + 0.5))
	return mg.Box(c).Size(w, h).Shrink(0).Radius(5).Border(1, p.line2).Background(p.card).Clip().
		Draw(func(pt *mg.Painter, r mg.Rect) {
			pt.Fill(mg.Rect{X: r.X, Y: r.Y, W: r.W, H: 8}, hex(col), 0)
			pt.Fill(mg.Rect{X: r.X + 4, Y: r.Y + 13, W: r.W - 8, H: 3}, p.line2, 1.5)
			pt.Fill(mg.Rect{X: r.X + 4, Y: r.Y + 19, W: r.W - 11, H: 3}, p.line2, 1.5)
			pt.Fill(mg.Rect{X: r.X + 4, Y: r.Y + r.H - 11, W: r.W - 8, H: 6}, p.bg3, 2)
		})
}

// recordingPanel is Watch recording: the task's screen recording with its
// step marks, in the replay's panel.
func (a *app) recordingPanel(c *mg.Context, t gateway.TaskView, pl *recPlayer) {
	p := pal(c)
	mg.Column(c).Gap(sp4).Padding(sp5).Radius(rCard).Border(1, p.line).Background(p.bg2).Role(mg.RoleDialog).Label(locales.RecordingPanelLabel()).Children(func() {
		mg.Row(c).Gap(sp3).AlignItems(mg.Center).Children(func() {
			mg.Text(c, locales.RecordingPanelTitle(t.Title)).FontSize(fsHeadline).FontWeight(fwSemibold).Grow(1)
			if button(c, locales.ActivityPanelClose(), btnOpt{}).Clicked() {
				a.act.replay = false
				a.closeRecording()
			}
		})
		a.recordingView(c, t, pl)
	})
}

// replayView is Watch replay: the task's steps one at a time, with the
// screenshot of each, on a timeline. Tasks with a screen recording play it
// instead (recordingPanel); this is the replay of those without.
func (a *app) replayView(c *mg.Context, t gateway.TaskView) {
	p := pal(c)
	st := &a.act
	n := len(t.Steps)
	if st.at >= n {
		st.at = max(0, n-1)
	}
	if st.playing {
		now := c.Now()
		if !now.Before(st.nextAt) {
			if st.at < n-1 {
				st.at++
				st.nextAt = now.Add(1500 * time.Millisecond)
			} else {
				st.playing = false
			}
		}
		c.After(100 * time.Millisecond)
	}
	mg.Column(c).Gap(sp4).Padding(sp5).Radius(rCard).Border(1, p.line).Background(p.bg2).Role(mg.RoleDialog).Label(locales.ReplayPanelLabel()).Children(func() {
		mg.Row(c).Gap(sp3).AlignItems(mg.Center).Children(func() {
			mg.Text(c, locales.ReplayPanelTitle(t.Title)).FontSize(fsHeadline).FontWeight(fwSemibold).Grow(1)
			if button(c, locales.ActivityPanelClose(), btnOpt{}).Clicked() {
				a.act.replay, a.act.playing = false, false
			}
		})
		if n == 0 {
			mg.Text(c, locales.ReplayNoSteps()).FontSize(fsBody).TextColor(p.ink2)
			return
		}
		s := t.Steps[st.at]
		text, sub, _ := stepDisplay(t, st.at)
		mg.Row(c).Gap(sp5).AlignItems(mg.Start).Wrap().Children(func() {
			a.shotView(c, t, s, 420)
			mg.Column(c).Basis(200).Grow(1).MinWidth(0).Gap(6).Children(func() {
				mg.Text(c, locales.ReplayStepOf(st.at+1, n)).FontSize(fsCaption).FontWeight(fwSemibold).TextColor(p.ink3)
				mg.Text(c, text).FontSize(fsTitle3).FontWeight(fwSemibold).LineHeight(1.35)
				if sub != "" {
					mg.Text(c, sub).FontSize(fsBody).TextColor(p.ink2)
				}
				if !s.At.IsZero() {
					mg.Text(c, s.At.Format("15:04:05")).FontSize(fsSmall).TextColor(p.ink3)
				}
				mg.Row(c).Gap(sp2).Margin(sp3, 0, 0, 0).Children(func() {
					if button(c, "", btnOpt{icon: icChevL, label: locales.ReplayPrevious()}).Width(34).Padding(0).Disabled(st.at == 0).Clicked() {
						st.at--
						st.playing = false
					}
					play, ic := locales.ReplayPlay(), icPlay
					if st.playing {
						play, ic = locales.ReplayPause(), icPause
					}
					if button(c, play, btnOpt{kind: btnPrimary, icon: ic}).Clicked() {
						st.playing = !st.playing
						if st.playing {
							if st.at >= n-1 {
								st.at = 0
							}
							st.nextAt = c.Now().Add(1500 * time.Millisecond)
						}
					}
					if button(c, "", btnOpt{icon: icChevR, label: locales.ReplayNext()}).Width(34).Padding(0).Disabled(st.at >= n-1).Clicked() {
						st.at++
						st.playing = false
					}
				})
			})
		})
		at := st.at
		mg.Box(c).Height(30).FillWidth().Draw(func(pt *mg.Painter, r mg.Rect) {
			pt.Fill(mg.Rect{X: r.X, Y: r.Y + 13, W: r.W, H: 4}, p.bg3, 2)
			for i := range t.Steps {
				you := kindOf(t, i) == stepYou
				size := float32(10)
				col := p.ink3
				if you {
					size, col = 14, p.ink
				}
				if i == at {
					col = p.accent
				}
				x := r.X + r.W*(float32(i)+0.5)/float32(max(n, 1))
				var dotPath mg.Path
				dotPath.Circle(x, r.Y+15, size/2+2)
				pt.FillPath(&dotPath, p.bg2)
				var d2 mg.Path
				d2.Circle(x, r.Y+15, size/2)
				pt.FillPath(&d2, col)
			}
		})
		mg.Row(c).Children(func() {
			mg.Text(c, locales.ReplayTimelineHint()).FontSize(fsCaption).TextColor(p.ink2).Grow(1)
			mg.Text(c, clock(taskDuration(t, c.Now()))).FontSize(fsCaption).TextColor(p.ink2)
		})
	})
}

// reportHTML is a small, self-contained report of tasks, for a gateway
// that cannot export its own.
func reportHTML(tasks []gateway.TaskView) string {
	var b strings.Builder
	esc := html.EscapeString
	title := "Pinky activity"
	if len(tasks) == 1 {
		title = tasks[0].Title
	}
	fmt.Fprintf(&b, "<!doctype html><meta charset=utf-8><title>%s</title>", esc(title))
	b.WriteString(`<style>body{font:15px/1.5 -apple-system,system-ui,sans-serif;max-width:680px;margin:40px auto;padding:0 20px;color:#1d1d22}small{color:#5b5b64}li{margin:6px 0}section{margin:0 0 40px}</style>`)
	if len(tasks) != 1 {
		fmt.Fprintf(&b, "<h1>%s</h1><p><small>%d tasks</small></p>", esc(title), len(tasks))
	}
	for _, t := range tasks {
		b.WriteString("<section>")
		fmt.Fprintf(&b, "<h2>%s</h2><p><small>%s · %s · %s · %s</small></p>", esc(t.Title), esc(t.Agent), esc(t.DeviceName),
			t.Started.Format("2006-01-02 15:04"), esc(elapsedIn(locales.In("en"), t.Ended.Sub(t.Started))))
		if s := summaryOf(t); s != "" {
			fmt.Fprintf(&b, "<p>%s</p>", esc(s))
		}
		b.WriteString("<ol>")
		for _, s := range t.Steps {
			text, sub := splitStep(s.Text)
			fmt.Fprintf(&b, "<li>%s", esc(text))
			if sub != "" {
				fmt.Fprintf(&b, "<br><small>%s</small>", esc(sub))
			}
			b.WriteString("</li>")
		}
		b.WriteString("</ol></section>")
	}
	b.WriteString("<p><small>Exported from Pinky.</small></p>")
	return b.String()
}

package ui

import (
	"strconv"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
)

// A list of steps the person goes through, such as Add phone › iPhone's:
// each step a number (a check once done) and a title, and under the step
// the person is at, what to do, what Pinky waits for, or why it is stuck.

// guideState is where a step stands.
type guideState int

const (
	guideLater guideState = iota
	guideNow
	guideDone
)

// guideRow is one step: its number (a check once done), its title, and for
// the step the person is at, what to do.
func guideRow(c *mg.Context, n int, title string, state guideState, body func()) {
	guideRowHint(c, n, title, "", state, body)
}

// guideRowHint is guideRow with a hint under the title that every step
// shows, whatever its state, such as whether it needs the cable.
func guideRowHint(c *mg.Context, n int, title, hint string, state guideState, body func()) {
	p := pal(c)
	mg.Row(c.Key(n)).Gap(12).AlignItems(mg.Start).Role(mg.RoleListItem).Children(func() {
		mark := mg.Box(c).Size(24, 24).Radius(12).Center().Shrink(0)
		switch state {
		case guideDone:
			mark.Background(p.green).Label(locales.AddPhoneDone()).Children(func() {
				mg.Icon(c, icCheck).FontSize(14).TextColor(p.white)
			})
		case guideNow:
			mark.Border(2, p.accent).Children(func() {
				mg.Text(c, strconv.Itoa(n)).FontSize(fsSmall).FontWeight(fwBold).TextColor(p.accent)
			})
		default:
			mark.Border(1, p.line2).Children(func() {
				mg.Text(c, strconv.Itoa(n)).FontSize(fsCaption).TextColor(p.ink3)
			})
		}
		mg.Column(c).Grow(1).MinWidth(0).Gap(8).Padding(2, 0, 0, 0).Children(func() {
			t := mg.Text(c, title).FontSize(fsBody).FontWeight(fwSemibold)
			if state == guideLater {
				t.TextColor(p.ink3)
			}
			if hint != "" {
				mg.Text(c, hint).FontSize(fsCaption).TextColor(p.ink3).Margin(-6, 0, 0, 0)
			}
			if state == guideNow && body != nil {
				body()
			}
		})
	})
}

func guideText(c *mg.Context, s string) {
	mg.Text(c, s).FontSize(fsSmall).TextColor(pal(c).ink2)
}

func guideNote(c *mg.Context, s string) {
	mg.Text(c, s).FontSize(fsCaption).TextColor(pal(c).ink3)
}

// guideWaiting is a spinner and what Pinky waits for.
func guideWaiting(c *mg.Context, s string) {
	mg.Row(c).Gap(8).AlignItems(mg.Center).Role(mg.RoleStatus).Children(func() {
		spinner(c)
		mg.Text(c, s).FontSize(fsSmall).TextColor(pal(c).ink2).Grow(1).MinWidth(0)
	})
}

// guideProblem is why a step is stuck: a sentence in the person's language and,
// when the driver said more, its own words.
func guideProblem(c *mg.Context, title, detail string) {
	p := pal(c)
	mg.Column(c).Padding(10, 12).Radius(10).Background(p.amberTint).Border(1, p.amberLine).Gap(4).Role(mg.RoleStatus).Children(func() {
		mg.Row(c).Gap(8).AlignItems(mg.Start).Children(func() {
			mg.Icon(c, icAlert).FontSize(15).TextColor(p.amberDot).Shrink(0)
			mg.Text(c, title).FontSize(fsBody).FontWeight(fwSemibold).Grow(1).MinWidth(0)
		})
		if detail != "" {
			mg.Text(c, detail).FontSize(fsSmall).TextColor(p.ink2).Margin(0, 0, 0, 23)
		}
	})
}

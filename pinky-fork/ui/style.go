package ui

import (
	"time"

	mg "github.com/egoist/mygo/ui"
)

// palette holds the prototype's colors (its CSS variables), with the
// changes docs/notes/client.md explains: amber and red moved away from the
// brand's lemon and watermelon, and the brand's watermelon added for the
// selection mark and the focus ring.
type palette struct {
	dark bool

	bg, bg2, bg3, sel, panel mg.Color
	line, line2              mg.Color
	ink, ink2, ink3          mg.Color

	accent, accentInk, accentBtn, accentTint mg.Color

	amber, amberDot, amberTint, amberLine, amberEdge mg.Color
	red, redTint, redLine                            mg.Color
	green                                            mg.Color

	ovl   mg.Color // the agent's overlay on the mirror
	phone mg.Color // the phone's bezel

	brand    mg.Color // watermelon: the selection mark and the focus ring
	lemon    mg.Color // the logo's field, for the brand only
	shadowLo mg.Color // the two layers of the prototype's --shadow
	shadowHi mg.Color
	white    mg.Color

	// The surfaces of the layout: the sidebar and the panel on the right
	// are the wings around the page, the cards sit on them, the groups of
	// rows sit on a page, and the rim is the phone's metal edge.
	wing, card, group, rim mg.Color
}

var lightPalette = palette{
	bg: hex("#ffffff"), bg2: hex("#f7f7f8"), bg3: hex("#efeff2"), sel: hex("#e7e7ed"), panel: hex("#ffffff"),
	line: hex("#ebebef"), line2: hex("#d9d9e0"),
	ink: hex("#1d1d22"), ink2: hex("#5b5b64"), ink3: hex("#6c6c75"),
	accent: hex("#5450d6"), accentInk: hex("#4542c4"), accentBtn: hex("#5450d6"), accentTint: hex("#f1f0fd"),
	amber: hex("#975800"), amberDot: hex("#df8601"), amberTint: hex("#fff7ed"), amberLine: hex("#f4dcbc"), amberEdge: hex("#e79733"),
	red: hex("#c32c18"), redTint: hex("#fef0ed"), redLine: hex("#f0c6bd"),
	green: hex("#2f9e5a"), ovl: hex("#5048e5"), phone: hex("#131317"),
	brand: hex("#ee4d6c"), lemon: hex("#ffbe1a"),
	shadowLo: mg.RGBA(18, 18, 28, 0.05), shadowHi: mg.RGBA(18, 18, 28, 0.10),
	white: hex("#ffffff"),
	wing:  hex("#f5f5f7"), card: hex("#ffffff"), group: hex("#f7f7f9"), rim: hex("#3a3a41"),
}

var darkPalette = palette{
	dark: true,
	bg:   hex("#1c1c1f"), bg2: hex("#212125"), bg3: hex("#2b2b30"), sel: hex("#323238"), panel: hex("#202024"),
	line: hex("#2e2e34"), line2: hex("#404047"),
	ink: hex("#ededf2"), ink2: hex("#b3b3bc"), ink3: hex("#a3a3ad"),
	accent: hex("#9895ff"), accentInk: hex("#b0aeff"), accentBtn: hex("#5b57e0"), accentTint: hex("#262549"),
	amber: hex("#f7a74e"), amberDot: hex("#f7a74e"), amberTint: hex("#322a23"), amberLine: hex("#5a4530"), amberEdge: hex("#b6762a"),
	red: hex("#f87a60"), redTint: hex("#3a1e19"), redLine: hex("#723b31"),
	green: hex("#4cc38a"), ovl: hex("#7a73ff"), phone: hex("#050506"),
	brand: hex("#ff8a9b"), lemon: hex("#ffbe1a"),
	shadowLo: mg.RGBA(0, 0, 0, 0.4), shadowHi: mg.RGBA(0, 0, 0, 0.45),
	white: hex("#ffffff"),
	wing:  hex("#212125"), card: hex("#29292e"), group: hex("#232327"), rim: hex("#3c3c44"),
}

func hex(s string) mg.Color { return mg.Hex(s) }

// The type ramp. Every size the client draws text at is one of these,
// but for the few drawn things that are pictures more than text (a PIN's
// digits, a pairing code).
const (
	fsCaption  = 12 // eyebrows, counts, meta under a name, shortcuts
	fsSmall    = 13 // descriptions and secondary lines
	fsBody     = 14 // body text, rows and buttons
	fsHeadline = 15 // the titles of cards and groups
	fsTitle3   = 17 // the title of a page's header
	fsTitle2   = 20 // the title of a page's content and of dialogs
	fsTitle1   = 26 // the welcome and empty pages
)

// The weights: text, labels and controls, titles, and the largest titles.
const (
	fwRegular  = 400
	fwMedium   = 500
	fwSemibold = 600
	fwBold     = 700
)

// The spacing scale, on a grid of 4 points.
const (
	sp1 = 4
	sp2 = 8
	sp3 = 12
	sp4 = 16
	sp5 = 20
	sp6 = 24
	sp8 = 32
)

// The radii, by the size of the thing rounded.
const (
	rSmall   = 6  // small buttons, chips, thumbnails
	rControl = 8  // buttons, rows, menu items
	rCard    = 12 // cards, tiles, popovers, the phone's keys
	rSheet   = 16 // dialogs
)

// headerHeight is the height of the sidebar's brand row and of a page's
// header beside it, which line up.
const headerHeight = 52

// pal returns the palette of the theme the view builds with.
func pal(c *mg.Context) *palette {
	if c.Theme().Dark {
		return &darkPalette
	}
	return &lightPalette
}

// applyTheme sets the window's theme from the palette, following the
// system's appearance (or the one forced in Options).
func applyTheme(c *mg.Context) *palette {
	p := pal(c)
	t := *c.Theme()
	t.Background, t.Surface = p.bg, p.panel
	t.SurfaceHover, t.SurfacePressed = p.bg3, p.sel
	t.Border, t.Text, t.TextMuted = p.line2, p.ink, p.ink2
	t.Accent, t.AccentHover, t.AccentPressed, t.AccentText = p.accentBtn, p.accentBtn, p.accentBtn, p.white
	// MyGo's own controls keep the accent in a window that is not active,
	// as the client's buttons, drawn in the palette's colors, do.
	t.InactiveAccent = t.Accent
	t.Danger, t.Warning, t.Success = p.red, p.amberDot, p.green
	t.Focus = p.brand
	t.Selection = p.accent.Alpha(0.28)
	t.FontSize = 14
	t.Radius = 8
	c.SetTheme(&t)
	return p
}

// --- small building blocks, after the prototype's CSS classes ---

type btnKind int

const (
	btnDefault btnKind = iota
	btnPrimary
	btnRed
)

type btnOpt struct {
	kind  btnKind
	small bool
	icon  *mg.SVG
	label string // accessible name for an icon-only button
	dark  bool   // the dark, translucent buttons over a tile
}

// button draws the prototype's .btn: 34 high, a border, a quiet hover.
func button(c *mg.Context, text string, o btnOpt) mg.Element {
	p := pal(c)
	b := mg.ButtonBase(c).Gap(7).Justify(mg.Center).AlignItems(mg.Center)
	h, px, r, fs := float32(34), float32(14), float32(8), float32(14)
	if o.small {
		h, px, r, fs = 28, 10, 7, 13
	}
	b.Height(h).Padding(0, px).Radius(r)
	bg, border, fg := p.panel, p.line2, p.ink
	hovered := b.Hovered()
	switch o.kind {
	case btnPrimary:
		bg, border, fg = p.accentBtn, p.accentBtn, p.white
		switch {
		case b.Pressed():
			bg = bg.Mix(mg.RGB(0, 0, 0), 0.12)
		case hovered:
			bg = bg.Mix(mg.RGB(255, 255, 255), 0.08)
		}
		border = bg
	case btnRed:
		fg, border = p.red, p.redLine
		if hovered {
			bg = p.redTint
		}
	default:
		if hovered {
			bg = p.bg3
		}
	}
	if o.dark {
		bg, border, fg = mg.RGBA(20, 20, 24, 0.9), mg.Transparent, p.white
		if o.kind == btnRed {
			fg = hex("#ffb4a8")
		}
	}
	b.Background(bg).Border(1, border).TextColor(fg)
	if !o.dark && !p.dark {
		// A push button stands a hair off the page, as the system's do.
		b.Shadow(0, 1, 1, 0, mg.RGBA(18, 18, 28, 0.04))
	}
	if o.label != "" {
		b.Label(o.label)
	} else if text != "" {
		b.Label(text)
	}
	b.Children(func() {
		if o.icon != nil {
			mg.Icon(c, o.icon).FontSize(18)
		}
		if text != "" {
			mg.Text(c, text).FontSize(fs).FontWeight(fwMedium).NoWrap()
		}
	})
	return b
}

// linkButton is the prototype's .link: accent text that underlines on hover.
func linkButton(c *mg.Context, text string) mg.Element {
	p := pal(c)
	b := mg.ButtonBase(c).Label(text)
	hovered := b.Hovered()
	b.Children(func() {
		t := mg.Text(c, text).FontSize(fsSmall).FontWeight(fwMedium).TextColor(p.accentInk)
		if hovered {
			t.Underline()
		}
	})
	return b
}

// quietButton is a text button in the secondary ink.
func quietButton(c *mg.Context, text string) mg.Element {
	p := pal(c)
	b := mg.ButtonBase(c).Label(text)
	hovered := b.Hovered()
	b.Children(func() {
		t := mg.Text(c, text).FontSize(fsSmall).FontWeight(fwMedium).TextColor(p.ink2)
		if hovered {
			t.TextColor(p.ink).Underline()
		}
	})
	return b
}

// dot is the prototype's 8-pixel status dot; live ones pulse.
func dot(c *mg.Context, col mg.Color, live bool) mg.Element {
	d := mg.Box(c).Size(8, 8).Radius(4).Background(col).Shrink(0)
	if live && !c.Preferences().ReduceMotion {
		d.Opacity(0.35 + 0.65*d.Loop("pulse", 1600*time.Millisecond, mg.Bounce(mg.EaseInOut)))
	}
	return d
}

// hollowDot is the offline dot: a ring.
func hollowDot(c *mg.Context) mg.Element {
	p := pal(c)
	return mg.Box(c).Size(8, 8).Radius(4).Border(1.5, p.ink3).Shrink(0)
}

// sectionLabel is the heading of a section of a panel or a list.
func sectionLabel(c *mg.Context, text string) mg.Element {
	p := pal(c)
	return mg.Text(c, text).FontSize(fsCaption).FontWeight(fwSemibold).TextColor(p.ink3).Margin(0, 0, sp2, 0)
}

// toggle is the prototype's switch: 36 × 20, the knob sliding across.
func toggle(c *mg.Context, on *bool, label string) mg.Element {
	p := pal(c)
	s := mg.SwitchBase(c, on).Label(label).Size(36, 20).Radius(999).Shrink(0)
	bg := p.line2
	if *on {
		bg = p.accentBtn
	}
	x := float32(2)
	if *on {
		x = 18
	}
	x = s.Animate("knob", x, 150*time.Millisecond)
	s.Background(bg).Draw(func(pt *mg.Painter, r mg.Rect) {
		k := mg.Rect{X: r.X + x, Y: r.Y + 2, W: 16, H: 16}
		pt.Shadow(k, 8, 0, 1, 2, 0, mg.RGBA(0, 0, 0, 0.25))
		pt.Fill(k, p.white, 8)
	})
	return s
}

// selectionMark draws the brand's watermelon bar inside the start of a
// selected row, clear of its rounded corners. Rows that carry it start
// their content rowInset in.
func selectionMark(c *mg.Context, row mg.Element) {
	p := pal(c)
	row.DrawOver(func(pt *mg.Painter, r mg.Rect) {
		h := min(r.H-16, float32(16))
		pt.Fill(mg.Rect{X: r.X + 4, Y: r.Y + (r.H-h)/2, W: 3, H: h}, p.brand, 1.5)
	})
}

// rowInset is the padding at the start of a row that can be selected,
// which leaves the selection mark its room.
const rowInset = 12

// appIcon is the letter icon of an app in Activity and Settings.
func appIcon(c *mg.Context, name string, size, radius, font float32, col string) mg.Element {
	if col == "" {
		col = appColor(name)
	}
	letter := "?"
	if name != "" {
		letter = string([]rune(name)[:1])
	}
	b := mg.Box(c).Size(size, size).Radius(radius).Background(hex(col)).Center().Shrink(0)
	b.Children(func() {
		mg.Text(c, letter).FontSize(font).FontWeight(fwBold).TextColor(hex("#ffffff"))
	})
	return b
}

// shadow lifts an element that floats over the page: a banner, a
// popover, the phone's keys.
func shadow(c *mg.Context, e mg.Element) mg.Element {
	p := pal(c)
	return e.Shadow(0, 1, 2, 0, p.shadowLo).Shadow(0, 10, 30, 0, p.shadowHi)
}

// raised lifts a card a little off the panel it sits on.
func raised(c *mg.Context, e mg.Element) mg.Element {
	if pal(c).dark {
		return e
	}
	return e.Shadow(0, 1, 2, 0, mg.RGBA(18, 18, 28, 0.04)).Shadow(0, 2, 8, 0, mg.RGBA(18, 18, 28, 0.03))
}

// card is a card on a wing: the right panel's tasks, a tile.
func card(c *mg.Context, e mg.Element) mg.Element {
	p := pal(c)
	return raised(c, e.Background(p.card).Border(1, p.line).Radius(rCard))
}

// group is a rounded group of rows on a page, as the system's settings
// draw theirs. Its rows separate with groupRow.
func group(c *mg.Context) mg.Element {
	p := pal(c)
	return mg.Column(c).Background(p.group).Border(1, p.line).Radius(rCard).Clip()
}

// groupRow pads a row of a group and draws the line under it, inset from
// both sides, unless it is the last.
func groupRow(c *mg.Context, row mg.Element, last bool) mg.Element {
	p := pal(c)
	row.Padding(sp3, sp4).MinHeight(48)
	if !last {
		row.DrawOver(func(pt *mg.Painter, r mg.Rect) {
			pt.Fill(mg.Rect{X: r.X + sp4, Y: r.Y + r.H - 1, W: r.W - 2*sp4, H: 1}, p.line, 0)
		})
	}
	return row
}

// sheet is a dialog over the window, as mg.Modal shows one, with the
// app's corners, padding and shadow.
func sheet(c *mg.Context, open *bool, fn func()) mg.Element {
	p := pal(c)
	w, h := c.Size()
	return mg.DialogBase(c, open, func(back, panel mg.Element) {
		back.Background(mg.RGBA(0, 0, 0, 0.32))
		panel.Padding(sp6).Gap(sp3).Radius(rSheet).Background(p.panel).Border(1, p.line).
			MaxWidth(w-2*sp6).MaxHeight(h-2*sp6).
			Shadow(0, 1, 3, 0, p.shadowLo).Shadow(0, 24, 60, 0, mg.RGBA(0, 0, 0, 0.22))
		fn()
	})
}

// menuPanel styles a drawn popover like the prototype's .pop.
func menuPanel(c *mg.Context, panel mg.Element) mg.Element {
	p := pal(c)
	return shadow(c, panel.Background(p.panel).Border(1, p.line2).Radius(rCard).Padding(6))
}

// menuItem is the prototype's .mi row.
func menuItem(c *mg.Context, text string, col mg.Color) mg.Element {
	p := pal(c)
	b := mg.ButtonBase(c).Label(text).MinHeight(32).Padding(6, 10).Radius(rSmall).Gap(10).AlignItems(mg.Center).Justify(mg.Start)
	if b.Hovered() || b.Focused() {
		b.Background(p.bg3)
	}
	if col == (mg.Color{}) {
		col = p.ink
	}
	b.Children(func() { mg.Text(c, text).FontSize(fsBody).TextColor(col) })
	return b
}

// menuText is the words of a menu row with a line under its name, as
// Work in background and Pause connection have: the name at the size of
// menuItem's, the line smaller and secondary.
func menuText(c *mg.Context, name, desc string, ink mg.Color) {
	p := pal(c)
	mg.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
		mg.Text(c, name).FontSize(fsBody).TextColor(ink)
		mg.Text(c, desc).FontSize(fsCaption).LineHeight(1.4).TextColor(p.ink2)
	})
}

// spinner is the prototype's .spin: a ring with an accent arc, turning.
func spinner(c *mg.Context) mg.Element {
	p := pal(c)
	b := mg.Box(c).Size(14, 14).Shrink(0)
	b.Draw(func(pt *mg.Painter, r mg.Rect) {
		pt.AnimationFrame()
		cx, cy, rad := r.X+7, r.Y+7, float32(6)
		var ring mg.Path
		ring.Circle(cx, cy, rad)
		pt.StrokePath(&ring, 2, p.accentTint)
		turn := float32(pt.Now().UnixMilli()%900) / 900
		var arc mg.Path
		arc.Circle(cx, cy, rad)
		from := turn
		pt.StrokePath(arc.Trim(from, from+0.25), 2, p.accent)
		if from+0.25 > 1 {
			var arc2 mg.Path
			arc2.Circle(cx, cy, rad)
			pt.StrokePath(arc2.Trim(0, from+0.25-1), 2, p.accent)
		}
	})
	return b
}

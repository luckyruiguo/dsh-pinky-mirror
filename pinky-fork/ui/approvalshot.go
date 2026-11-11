package ui

import (
	"math"
	"strings"

	"github.com/egoist/mygo"
	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/screen"
)

// An approval's screenshot answers "on which screen does the agent want to
// do what": the whole screen, small, with the target marked, so the person
// knows the app and the page, beside the part around the target, large
// enough to read its words. A swipe shows as an arrow from where the
// finger goes down. A click shows the screenshot larger: in a window
// beside the floating panel, or over the main window.

const (
	figureGap = 10 // between the whole screen and the zoom
	// lensMin is the narrowest zoom beside a portrait screen.
	lensMin = 150
)

// The screenshot on a Needs you card, whose words are 260 DIPs wide: as in
// the floating panel, smaller.
const (
	cardFigureW = 260
	cardFigure  = 150 // a portrait screen's
	cardWide    = 130 // a landscape screen's at most
	cardLens    = 80  // the zoom under a landscape screen
)

// shotMarks are what an approval marks on its screenshot, in its pixels:
// the element the action targets, where the finger goes down and, for a
// swipe, where it lifts; for a pinch, the second finger's too; for a drag
// that stops on the way, every point it goes through.
type shotMarks struct {
	target      screen.Rect
	point, to   screen.Point
	point2, to2 screen.Point
	path        []screen.Point
}

func marksOf(n gateway.NeedView) shotMarks {
	return shotMarks{target: n.Target, point: n.Point, to: n.To, point2: n.Point2, to2: n.To2, path: n.Path}
}

// swipe reports a finger that moves: a swipe's, a drag's or a pinch's.
func (m shotMarks) swipe() bool {
	return m.to != (screen.Point{}) && m.to != m.point || len(m.path) > 2
}

// route is the points the first finger goes through, from where it goes
// down to where it lifts.
func (m shotMarks) route() []screen.Point {
	if len(m.path) > 1 {
		return m.path
	}
	return []screen.Point{m.point, m.to}
}

func (m shotMarks) pinch() bool { return m.point2 != (screen.Point{}) }

func (m shotMarks) touch() bool { return m.point != (screen.Point{}) }

// outlined returns the target to outline on an iw × ih screenshot: none
// where it covers so much of the screen that an outline says nothing, as
// the list under a swipe or a picture the agent taps on, which show the
// touch instead.
func (m shotMarks) outlined(iw, ih int) (screen.Rect, bool) {
	if m.target.Empty() {
		return screen.Rect{}, false
	}
	area := float64(m.target.W) * float64(m.target.H)
	limit := 0.4
	if m.swipe() {
		limit = 0.15
	}
	if (m.touch() || m.swipe()) && area > limit*float64(iw)*float64(ih) {
		return screen.Rect{}, false
	}
	return m.target, true
}

// focus is the part of the screen the zoom shows: the target, where the
// finger goes down, or the whole swipe. ok is false when the action has
// no position, as a key or opening an app.
func (m shotMarks) focus(iw, ih int) (r screen.Rect, ok bool) {
	switch {
	case m.swipe():
		x0, x1 := min(m.point.X, m.to.X), max(m.point.X, m.to.X)
		y0, y1 := min(m.point.Y, m.to.Y), max(m.point.Y, m.to.Y)
		for _, q := range m.path {
			x0, x1 = min(x0, q.X), max(x1, q.X)
			y0, y1 = min(y0, q.Y), max(y1, q.Y)
		}
		if m.pinch() {
			x0, x1 = min(x0, m.point2.X, m.to2.X), max(x1, m.point2.X, m.to2.X)
			y0, y1 = min(y0, m.point2.Y, m.to2.Y), max(y1, m.point2.Y, m.to2.Y)
		}
		return screen.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}, true
	case !m.target.Empty():
		if t, ok := m.outlined(iw, ih); ok {
			return t, true
		}
	}
	if m.touch() {
		return screen.Rect{X: m.point.X - 24, Y: m.point.Y - 24, W: 48, H: 48}, true
	}
	return screen.Rect{}, false
}

// pxPerPt is about how many of an iw × ih screenshot's pixels make a
// point of the phone's own interface: phones are about 400 points across
// and tablets, wider for their length, about 800.
func pxPerPt(iw, ih int) float32 {
	short, long := float32(min(iw, ih)), float32(max(iw, ih))
	if short <= 0 {
		return 1
	}
	pts := float32(400)
	if short/long >= 0.6 {
		pts = 800
	}
	return max(1, short/pts)
}

// zoomOn returns the part of an iw × ih screenshot that a box of bw × bh
// DIPs shows of m, and its scale in DIPs a pixel: a little smaller than
// the phone shows it, zoomed out to show the target whole while its words
// still read, as far as the screenshot's whole width. A target too large
// for that shows around where the finger goes down. The part is within
// the screenshot, which fills the box.
func zoomOn(iw, ih int, m shotMarks, bw, bh float32) (mg.Rect, float32) {
	f, _ := m.focus(iw, ih)
	px := pxPerPt(iw, ih)
	want, floor := 0.85/px, 0.55/px
	fw, fh := float32(f.W), float32(f.H)
	mx, my := max(10*px, 0.08*fw), max(16*px, 0.5*fh)
	fitW, fitH := bw/(fw+2*mx), bh/(fh+2*my)
	s := min(want, fitW)
	if s < 0.75*floor {
		s = floor // too wide to read whole: around the finger
	}
	if fitH >= floor {
		s = min(s, fitH)
	}
	s = max(s, bw/float32(iw), bh/float32(ih))
	cx, cy := float32(f.X)+fw/2, float32(f.Y)+fh/2
	cut := fw*s > bw || fh*s > bh
	switch {
	case cut && m.swipe():
		// Where the finger goes down, and some of the way it goes.
		dx, dy := float32(m.to.X-m.point.X), float32(m.to.Y-m.point.Y)
		l := float32(math.Hypot(float64(dx), float64(dy)))
		shift := min(l, 0.3*min(bw, bh)/s)
		cx, cy = float32(m.point.X)+dx/l*shift, float32(m.point.Y)+dy/l*shift
	case cut && m.touch():
		if fw*s > bw {
			cx = float32(m.point.X)
		}
		if fh*s > bh {
			cy = float32(m.point.Y)
		}
	}
	rw, rh := bw/s, bh/s
	x := clampF(cx-rw/2, 0, float32(iw)-rw)
	y := clampF(cy-rh/2, 0, float32(ih)-rh)
	return mg.Rect{X: x, Y: y, W: rw, H: rh}, s
}

func clampF(v, lo, hi float32) float32 {
	if hi < lo {
		return lo
	}
	return max(lo, min(v, hi))
}

// figureHeight is the natural height of the figure for an iw × ih
// screenshot at width w: tall DIPs for a portrait screen, beside its
// zoom; a landscape screen across the width, at most wideMax high, with a
// zoom low DIPs high under it.
func figureHeight(w, tall, wideMax, low float32, iw, ih int, zoom bool) float32 {
	if iw <= 0 || ih <= 0 {
		return tall
	}
	a := float32(iw) / float32(ih)
	if a < 0.8 {
		return tall
	}
	h := min(w/a, wideMax)
	if zoom {
		h += figureGap + low
	}
	return h
}

// shotLayout splits the figure's box r between the whole screen and the
// zoom: side by side for a portrait screen, the zoom under a landscape
// one. Without a zoom the screen shows alone, in the middle.
func shotLayout(r mg.Rect, iw, ih int, zoom bool) (whole, lens mg.Rect) {
	if iw <= 0 || ih <= 0 || r.W <= 0 || r.H <= 0 {
		return mg.Rect{}, mg.Rect{}
	}
	a := float32(iw) / float32(ih)
	fit := func(box mg.Rect) mg.Rect {
		w, h := box.W, box.W/a
		if h > box.H {
			w, h = box.H*a, box.H
		}
		return mg.Rect{X: box.X + (box.W-w)/2, Y: box.Y + (box.H-h)/2, W: w, H: h}
	}
	if !zoom {
		return fit(r), mg.Rect{}
	}
	if a < 0.8 {
		w := min(r.H*a, r.W-figureGap-lensMin)
		whole = fit(mg.Rect{X: r.X, Y: r.Y, W: w, H: r.H})
		whole.X = r.X
		lens = mg.Rect{X: whole.X + whole.W + figureGap, Y: r.Y, H: r.H}
		lens.W = r.X + r.W - lens.X
		return whole, lens
	}
	low := max(r.H*0.32, min(96, r.H*0.42))
	whole = fit(mg.Rect{X: r.X, Y: r.Y, W: r.W, H: r.H - figureGap - low})
	whole.Y = r.Y
	lens = mg.Rect{X: r.X, Y: whole.Y + whole.H + figureGap, W: r.W}
	lens.H = r.Y + r.H - lens.Y
	return whole, lens
}

// shotBitmap returns the approval's screenshot as a bitmap, made once.
func (a *app) shotBitmap(n gateway.NeedView) *mg.Bitmap {
	if n.Screenshot == nil {
		return nil
	}
	bm := a.shots[n.ID]
	if bm == nil {
		bm = mg.NewBitmap(n.Screenshot)
		a.shots[n.ID] = bm
	}
	return bm
}

// approvalFigure draws the approval's screenshot, which n must have,
// across its parent's width: the whole screen with the target marked, and
// the part around it zoomed. The caller sets its height. With open it is a
// button that shows the screenshot larger.
func (a *app) approvalFigure(c *mg.Context, n gateway.NeedView, open func()) mg.Element {
	p := pal(c)
	bm := a.shotBitmap(n)
	iw, ih := bm.Size()
	m := marksOf(n)
	_, zoom := m.focus(iw, ih)
	var fig mg.Element
	hovered := false
	if open != nil {
		fig = mg.ButtonBase(c).Cursor(mg.CursorPointer).Tooltip(locales.ShotEnlarge())
		hovered = fig.Hovered() || fig.FocusVisible()
		if fig.Clicked() {
			open()
		}
	} else {
		fig = mg.Box(c)
	}
	fig.FillWidth().Label(locales.PanelShotLabel(n.DeviceName))
	fig.Draw(func(pt *mg.Painter, r mg.Rect) {
		if iw == 0 || ih == 0 {
			return
		}
		all := mg.Rect{W: float32(iw), H: float32(ih)}
		if !zoom {
			// The screen alone, on a tray across the figure, the mark in
			// the tray's corner.
			pt.Fill(r, trayColor(p), 12)
			whole, _ := shotLayout(mg.Rect{X: r.X + 8, Y: r.Y + 8, W: r.W - 16, H: r.H - 16}, iw, ih, false)
			drawScreen(pt, p, bm, whole, all, m, iw, ih, shotThumb)
			if open != nil {
				drawEnlarge(pt, r, mg.Rect{X: r.X, Y: r.Y}, hovered)
			}
			return
		}
		whole, lens := shotLayout(r, iw, ih, true)
		drawScreen(pt, p, bm, whole, all, m, iw, ih, shotThumb)
		src, _ := zoomOn(iw, ih, m, lens.W, lens.H)
		drawScreen(pt, p, bm, lens, src, m, iw, ih, shotLens)
		if open != nil {
			// In the corner away from the target, so as not to hide it.
			f, _ := m.focus(iw, ih)
			s := lens.W / src.W
			at := mg.Rect{X: lens.X + (float32(f.X)-src.X)*s, Y: lens.Y + (float32(f.Y)-src.Y)*s, W: float32(f.W) * s, H: float32(f.H) * s}
			drawEnlarge(pt, lens, at, hovered)
		}
	})
	return fig
}

// shotMode is how large a screenshot draws: the whole screen beside the
// zoom, the zoom, or the large view.
type shotMode int

const (
	shotThumb shotMode = iota
	shotLens
	shotLarge
)

func trayColor(p *palette) mg.Color {
	if p.dark {
		return mg.RGBA(255, 255, 255, 0.05)
	}
	return mg.RGBA(60, 40, 10, 0.06)
}

// drawScreen draws the part src of the screenshot into dst, rounded, with
// its marks: marks that show at a thumbnail's size, and in the zoom what
// is around the target dimmed.
func drawScreen(pt *mg.Painter, p *palette, bm *mg.Bitmap, dst, src mg.Rect, m shotMarks, iw, ih int, mode shotMode) {
	if dst.W <= 0 || dst.H <= 0 {
		return
	}
	rad := float32(10)
	if mode == shotThumb {
		rad = min(10, dst.W*0.09)
	}
	pt.Shadow(dst, rad, 0, 1, 3, 0, mg.RGBA(0, 0, 0, 0.16))
	pt.Clip(dst, rad, func() {
		pt.Fill(dst, hex("#000000"), rad)
		pt.ImageRegion(bm, src, dst, mg.ImageOptions{})
		drawMarks(pt, p, dst, src, m, iw, ih, mode)
	})
	edge := mg.RGBA(0, 0, 0, 0.14)
	if p.dark {
		edge = mg.RGBA(255, 255, 255, 0.16)
	}
	pt.Stroke(dst, edge, rad, 1)
}

// drawMarks marks m on a picture of the screenshot's part src drawn into
// dst: the target outlined, a touch without one as a ring, a swipe as an
// arrow from where the finger goes down, a drag as an arrow through its
// stops, and a pinch as two arrows.
func drawMarks(pt *mg.Painter, p *palette, dst, src mg.Rect, m shotMarks, iw, ih int, mode shotMode) {
	thumb := mode == shotThumb
	s := dst.W / src.W
	at := func(x, y int) (float32, float32) {
		return dst.X + (float32(x)-src.X)*s, dst.Y + (float32(y)-src.Y)*s
	}
	halo := mg.RGBA(255, 255, 255, 0.92)
	accent := p.ovl
	width, pad, least := float32(2.5), float32(5), float32(0)
	if thumb {
		width, pad, least = 2, 2.5, 12
	}
	t, outline := m.outlined(iw, ih)
	if outline {
		x, y := at(t.X, t.Y)
		box := mg.Rect{X: x - pad, Y: y - pad, W: float32(t.W)*s + 2*pad, H: float32(t.H)*s + 2*pad}
		if box.W < least {
			box.X, box.W = box.X-(least-box.W)/2, least
		}
		if box.H < least {
			box.Y, box.H = box.Y-(least-box.H)/2, least
		}
		rad := min(14, box.H/2, box.W/2)
		if mode == shotLens && !m.swipe() {
			dimAround(pt, dst, box, rad)
		}
		// A target the zoom cuts through, as a wide button, shows by the
		// dimming alone: an outline would be two lines across.
		through := (box.X < dst.X && box.X+box.W > dst.X+dst.W) || (box.Y < dst.Y && box.Y+box.H > dst.Y+dst.H)
		if !through {
			pt.Stroke(grow(box, 1.5), halo, rad+1.5, width+3)
			pt.Stroke(box, accent, rad, width)
		}
	}
	switch {
	case m.swipe():
		var route []mg.Point
		for _, q := range m.route() {
			x, y := at(q.X, q.Y)
			route = append(route, mg.Point{X: x, Y: y})
		}
		drawArrow(pt, route, thumb, accent, halo)
		if m.pinch() {
			x0, y0 := at(m.point2.X, m.point2.Y)
			x1, y1 := at(m.to2.X, m.to2.Y)
			drawArrow(pt, []mg.Point{{X: x0, Y: y0}, {X: x1, Y: y1}}, thumb, accent, halo)
		}
	case m.touch() && !outline:
		x, y := at(m.point.X, m.point.Y)
		ring, dotR := float32(16), float32(3.5)
		if thumb {
			ring, dotR = 6, 2
		}
		var o, in mg.Path
		o.Circle(x, y, ring)
		pt.StrokePath(&o, width+3, halo)
		pt.StrokePath(&o, width, accent)
		in.Circle(x, y, dotR+1.5)
		pt.FillPath(&in, halo)
		var d mg.Path
		d.Circle(x, y, dotR)
		pt.FillPath(&d, accent)
	}
}

func grow(r mg.Rect, d float32) mg.Rect {
	return mg.Rect{X: r.X - d, Y: r.Y - d, W: r.W + 2*d, H: r.H + 2*d}
}

// dimAround darkens r outside the rounded box hole, so the target stands
// out.
func dimAround(pt *mg.Painter, r, hole mg.Rect, rad float32) {
	var path mg.Path
	path.MoveTo(r.X, r.Y).LineTo(r.X+r.W, r.Y).LineTo(r.X+r.W, r.Y+r.H).LineTo(r.X, r.Y+r.H).Close()
	// The hole, the other way round: non-zero winding leaves it out.
	k := rad * 0.5523
	x0, y0, x1, y1 := hole.X, hole.Y, hole.X+hole.W, hole.Y+hole.H
	path.MoveTo(x0+rad, y0)
	path.CubeTo(x0+rad-k, y0, x0, y0+rad-k, x0, y0+rad)
	path.LineTo(x0, y1-rad)
	path.CubeTo(x0, y1-rad+k, x0+rad-k, y1, x0+rad, y1)
	path.LineTo(x1-rad, y1)
	path.CubeTo(x1-rad+k, y1, x1, y1-rad+k, x1, y1-rad)
	path.LineTo(x1, y0+rad)
	path.CubeTo(x1, y0+rad-k, x1-rad+k, y0, x1-rad, y0)
	path.Close()
	pt.FillPath(&path, mg.RGBA(0, 0, 0, 0.14))
}

// drawArrow draws a finger's way: a dot where it goes down, a line
// through the points between, and a head where it lifts.
func drawArrow(pt *mg.Painter, route []mg.Point, thumb bool, accent, halo mg.Color) {
	var pts []mg.Point
	for _, q := range route {
		if n := len(pts); n == 0 || math.Hypot(float64(q.X-pts[n-1].X), float64(q.Y-pts[n-1].Y)) >= 1 {
			pts = append(pts, q)
		}
	}
	if len(pts) < 2 {
		return
	}
	x0, y0 := pts[0].X, pts[0].Y
	x1, y1 := pts[len(pts)-1].X, pts[len(pts)-1].Y
	px, py := pts[len(pts)-2].X, pts[len(pts)-2].Y // where the last leg starts
	dx, dy := x1-px, y1-py
	l := float32(math.Hypot(float64(dx), float64(dy)))
	ux, uy := dx/l, dy/l
	width, head, dot := float32(3.5), float32(15), float32(7)
	if thumb {
		width, head, dot = 2, 7, 3.5
	}
	head = min(head, l*0.6)
	bx, by := x1-ux*head, y1-uy*head // the head's base
	var line mg.Path
	line.MoveTo(x0, y0)
	for _, q := range pts[1 : len(pts)-1] {
		line.LineTo(q.X, q.Y)
	}
	line.LineTo(bx+ux*1, by+uy*1)
	pt.StrokePath(&line, width+3, halo)
	var tip mg.Path
	wing := head * 0.62
	tip.MoveTo(x1, y1).LineTo(bx-uy*wing, by+ux*wing).LineTo(bx+uy*wing, by-ux*wing).Close()
	pt.StrokePath(&tip, 3, halo)
	pt.StrokePath(&line, width, accent)
	pt.FillPath(&tip, accent)
	var start, ring mg.Path
	ring.Circle(x0, y0, dot+1.5)
	pt.FillPath(&ring, halo)
	start.Circle(x0, y0, dot)
	pt.FillPath(&start, accent)
}

// drawEnlarge draws the expand mark in the corner of r farthest from
// away: the figure shows the screenshot larger.
func drawEnlarge(pt *mg.Painter, r, away mg.Rect, strong bool) {
	if r.W < 40 || r.H < 40 {
		return
	}
	b := mg.Rect{X: r.X + r.W - 30, Y: r.Y + r.H - 30, W: 24, H: 24}
	if away.X+away.W/2 > r.X+r.W/2 {
		b.X = r.X + 6
	}
	if away.Y+away.H/2 > r.Y+r.H/2 {
		b.Y = r.Y + 6
	}
	alpha := float32(0.5)
	if strong {
		alpha = 0.78
	}
	pt.Fill(b, mg.RGBA(20, 20, 24, alpha), 7)
	pt.Icon(icExpand, mg.Rect{X: b.X + 5, Y: b.Y + 5, W: 14, H: 14}, hex("#ffffff"))
}

// missingShot says why an approval about the screen has no screenshot:
// the phone is locked, Pinky takes none of a blocked app, or the phone
// sent none. It is empty for approvals that are about no screen: copying
// files, giving the agent a recording, reading an app's logs, and the agent's own questions and
// first connection.
func (a *app) missingShot(n gateway.NeedView) (string, *mg.SVG) {
	if n.Screenshot != nil || n.Kind != gateway.NeedApproval || newAgentNeed(n) || n.Rule == "Agent request" || n.Rule == "Files" ||
		n.Rule == "Recordings" || n.Rule == "App logs" {
		return "", nil
	}
	if d := a.device(n.DeviceID); d != nil && a.isLocked(*d) {
		return locales.ShotMissingLocked(d.Info.Name), icLock
	}
	if n.App != "" && a.blockedApp(n.App) {
		return locales.ShotMissingBlocked(n.App), icEyeOff
	}
	if !n.Target.Empty() || n.Point != (screen.Point{}) {
		return locales.ShotMissing(), icEyeOff
	}
	return "", nil
}

// blockedApp reports whether Settings › Safety lists the app, by its name.
func (a *app) blockedApp(name string) bool {
	for _, b := range a.settings.BlockedApps {
		if strings.EqualFold(b.Name, name) {
			return true
		}
	}
	return false
}

// missingShotNote is the line that stands for a missing screenshot.
func (a *app) missingShotNote(c *mg.Context, n gateway.NeedView) {
	text, ic := a.missingShot(n)
	if text == "" {
		return
	}
	p := pal(c)
	mg.Row(c).Margin(12, 0, 0, 0).Padding(9, 12).Radius(10).Background(trayColor(p)).Gap(8).AlignItems(mg.Center).Children(func() {
		mg.Icon(c, ic).FontSize(16).TextColor(p.ink3).Shrink(0)
		mg.Text(c, text).FontSize(fsSmall).TextColor(p.ink2).MaxLines(2)
	})
}

// --- the screenshot, larger ---

// shotViewer shows n's screenshot as large as w × h DIPs allow, or as
// its parent allows for fill, under the approval's phone, app and title,
// with a close button.
func (a *app) shotViewer(c *mg.Context, n gateway.NeedView, w, h float32, fill bool, close func()) {
	p := pal(c)
	bm := a.shotBitmap(n)
	if bm == nil {
		return
	}
	iw, ih := bm.Size()
	m := marksOf(n)
	col := mg.Column(c).Gap(10).Label(locales.ShotLabel(n.DeviceName))
	if fill {
		col.Fill()
	}
	col.Children(func() {
		mg.Row(c).Gap(12).AlignItems(mg.Start).Children(func() {
			mg.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				needEyebrow(c, n)
				mg.Text(c, needTitle(n)).FontSize(fsHeadline).FontWeight(fwSemibold).LineHeight(1.3).MaxLines(2).Margin(4, 0, 0, 0)
			})
			b := mg.ButtonBase(c).Size(28, 28).Radius(8).Center().Label(locales.ShotClose()).Shrink(0)
			ink := p.ink2
			if b.Hovered() {
				b.Background(p.bg3)
				ink = p.ink
			}
			b.TextColor(ink).Children(func() { mg.Icon(c, icX).FontSize(17) })
			if b.Clicked() {
				close()
			}
		})
		box := mg.Box(c).Label(locales.PanelShotLabel(n.DeviceName))
		if fill {
			box.Grow(1).MinHeight(0).FillWidth()
		} else {
			img := viewerImage(w, h, iw, ih)
			box.Size(img.W, img.H).AlignSelf(mg.Center)
		}
		box.Draw(func(pt *mg.Painter, r mg.Rect) {
			img := viewerImage(r.W, r.H, iw, ih)
			img.X, img.Y = r.X+(r.W-img.W)/2, r.Y+(r.H-img.H)/2
			drawScreen(pt, p, bm, img, mg.Rect{W: float32(iw), H: float32(ih)}, m, iw, ih, shotLarge)
		})
	})
}

// viewerImage sizes the large screenshot to fit w × h.
func viewerImage(w, h float32, iw, ih int) mg.Rect {
	if iw <= 0 || ih <= 0 {
		return mg.Rect{}
	}
	a := float32(iw) / float32(ih)
	iwF := min(w, h*a)
	return mg.Rect{W: iwF, H: iwF / a}
}

// viewerChrome is the room the large view takes around its picture: the
// padding, the eyebrow and a title of a line, the gap.
const viewerChrome = 16 + 42 + 10 + 16

// viewingNeed returns the need whose screenshot shows larger beside the
// floating panel.
func (a *app) viewingNeed() (gateway.NeedView, bool) {
	if a.viewing == "" {
		return gateway.NeedView{}, false
	}
	for _, n := range a.needs {
		if n.ID == a.viewing && n.Screenshot != nil {
			return n, true
		}
	}
	return gateway.NeedView{}, false
}

// openShot shows n's screenshot larger, in a window beside the floating
// panel, which takes the keyboard without making Pinky the active app:
// Esc closes it.
func (a *app) openShot(n gateway.NeedView) {
	if n.Screenshot == nil {
		return
	}
	a.viewing = n.ID
	if a.panel == nil {
		return // tests have no windows
	}
	w, h := a.viewerSize(n)
	if a.viewer == nil {
		a.viewer = mygo.NewWindow(mygo.WindowOptions{
			Title: locales.ShotLabel(n.DeviceName), Width: w, Height: h,
			Panel: true, Frameless: true, Transparent: true, CornerRadius: 14,
			DisableResize: true, DisableMinimize: true, DisableMaximize: true, DisableFullScreen: true,
			SkipTaskbar: true, AlwaysOnTop: true,
			Content: mg.View(a.viewerView),
		})
		v := a.viewer
		v.OnClosed(func() {
			if a.viewer == v {
				a.viewer, a.viewing = nil, ""
			}
		})
	} else {
		a.viewer.SetTitle(locales.ShotLabel(n.DeviceName))
		a.viewer.SetSize(w, h)
	}
	a.placeViewer(w)
	a.viewer.Show()
	a.viewer.Invalidate()
}

// closeShot closes the large view.
func (a *app) closeShot() {
	a.viewing = ""
	if v := a.viewer; v != nil {
		a.viewer = nil
		v.Close()
	}
}

// viewerSize is the large view's window size for n: its screenshot as
// large as most of the display allows.
func (a *app) viewerSize(n gateway.NeedView) (int, int) {
	b := n.Screenshot.Bounds()
	maxW, maxH := float32(900), float32(800)
	if d := a.panelDisplay(); d.Width > 0 {
		maxW, maxH = float32(d.Width)*0.55, float32(d.Height)*0.86
	}
	img := viewerImage(maxW-32, maxH-viewerChrome, b.Dx(), b.Dy())
	return int(max(img.W+32, 320) + 0.5), int(img.H + viewerChrome + 0.5)
}

// panelDisplay is the work area of the display the floating panel is on.
func (a *app) panelDisplay() mygo.Rectangle {
	if a.panel == nil {
		return mygo.Rectangle{}
	}
	x, y := a.panel.Position()
	return mygo.Screen.DisplayNearestPoint(mygo.Point{X: x + panelW/2, Y: y + 20}).WorkArea
}

// placeViewer puts the large view left of the floating panel, at its top,
// or at the display's left edge where there is no room.
func (a *app) placeViewer(w int) {
	x, y := a.panel.Position()
	wa := a.panelDisplay()
	vx := x - w - 12
	if vx < wa.X+8 {
		vx = wa.X + 8
	}
	a.viewer.SetPosition(vx, y)
}

// viewerView is the large view's window.
func (a *app) viewerView(c *mg.Context) {
	p := applyTheme(c)
	c.Root().Background(mg.Transparent)
	n, ok := a.viewingNeed()
	box := mg.Column(c).Fill().Radius(14).Background(p.panel).Border(1, p.line2).Padding(16).Clip()
	if !ok {
		a.post(a.closeShot)
		return
	}
	if c.Shortcut(0, mg.KeyEscape) {
		a.post(a.closeShot)
	}
	box.Children(func() {
		a.shotViewer(c, n, 0, 0, true, func() { a.post(a.closeShot) })
	})
}

// shotDialog shows a screenshot larger over the main window, from a Needs
// you card.
func (a *app) shotDialog(c *mg.Context) {
	if a.shotOpen == "" {
		return
	}
	var n gateway.NeedView
	found := false
	for _, x := range a.needs {
		if x.ID == a.shotOpen && x.Screenshot != nil {
			n, found = x, true
		}
	}
	open := found
	w, h := c.Size()
	sheet(c.Key("shot-"+a.shotOpen), &open, func() {
		mg.Column(c).Children(func() {
			a.shotViewer(c, n, w*0.7, h*0.86-viewerChrome, false, func() { a.shotOpen = "" })
		})
	})
	if !open {
		a.shotOpen = ""
	}
}

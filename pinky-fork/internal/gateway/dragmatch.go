package gateway

import (
	"image"
	"image/color"
	"math"
	"sort"

	"github.com/DIYgod/pinky/internal/screen"
)

// Finding where a home screen's drop point went (dragfollow.go). Launchers
// such as Pixel Launcher shrink the page toward a point near the middle
// once an app is picked up, while the dock and the wallpaper stay. The
// gateway compares a picture from before the drag with one taken while the
// finger holds the app: the elements around the drop point (app icons,
// folders, widgets: the tree's, from before the drag) are cut out of the
// first and looked for in the second at scales from matchMinScale to 1, by
// normalized cross-correlation over their pixels only, so the wallpaper
// between them does not count. The search runs on grayscale cells of a few
// screen pixels, coarse first, then finer around the best places.
//
// It assumes the page shrinks evenly toward a point in the middle region
// of the screen (shrinkCentre), as Launcher3 does; a page that moved some
// other way finds no match and the drop stays where the agent put it.

const (
	matchMinScale = 0.6
	matchMaxScale = 1.0
	// matchFineCells is how many cells across the fine grid has.
	matchFineCells = 360
	// matchCoarse is how many fine cells across a coarse cell holds.
	matchCoarse = 4
	// matchAccept is the least correlation a match needs.
	matchAccept = 0.6
	// matchClear is how much better the match must be than the best one
	// that puts the drop point elsewhere.
	matchClear = 0.05
	// matchStill is the correlation above which an element counts as not
	// moved: the dock, or a page that did not shrink.
	matchStill = 0.9
	// matchShrunk is the largest scale that counts as a shrink.
	matchShrunk = 0.97
	// matchFlat is the least standard deviation, in gray levels, of the
	// pixels compared: below it there is nothing to recognise.
	matchFlat = 6
)

// shrinkCentre is where the page may shrink toward, as fractions of the
// screen: Pixel Launcher's was 0.5, 0.41.
var shrinkCentre = struct{ x0, y0, x1, y1 float64 }{0.2, 0.15, 0.8, 0.75}

// dropFind is what findDrop learnt.
type dropFind struct {
	To     screen.Point // where the drop point is now
	Scale  float64      // how much the page shrank
	Score  float64      // the match's correlation
	Shrunk bool         // To is where the drop point moved; else it did not move
	Why    string       // when it cannot tell: why, for the agent
}

// findDrop finds where the point to of a w×h screen went between before
// and during, from the elements marks shown around it (screen pixels).
func findDrop(before, during image.Image, w, h int, to screen.Point, marks []screen.Rect) dropFind {
	unit := max(1, w/matchFineCells)
	b, d := newGrid(before, w, h, unit), newGrid(during, w, h, unit)
	if b == nil || d == nil {
		return dropFind{Why: "the pictures are not of this screen"}
	}
	var moved []screen.Rect
	nearest, nearestDist, nearestStill := -1, math.MaxFloat64, false
	for i, m := range marks {
		still, ok := stayed(b, d, m)
		if !ok {
			continue
		}
		if !still {
			moved = append(moved, m)
		}
		if dist := rectDist(m, to); dist < nearestDist {
			nearest, nearestDist, nearestStill = i, dist, still
		}
	}
	switch {
	case nearest < 0:
		return dropFind{Why: "nothing near the drop point to follow"}
	case nearestStill || len(moved) == 0:
		// The dock, or a page that did not shrink.
		return dropFind{To: to, Scale: 1}
	}
	cut, ok := cutAround(b, moved, to, w, unit)
	if !ok {
		return dropFind{Why: "nothing near the drop point to follow"}
	}
	f := cut.search(b, d, to, w, h)
	if f.Why == "" && !f.Shrunk {
		f.To = to
	}
	return f
}

// grid is a grayscale picture of the screen in square cells of unit screen
// pixels.
type grid struct {
	w, h, unit int
	pix        []float32
}

// newGrid averages img, a picture of a w×h screen at any size, into cells
// of unit screen pixels; nil when the picture has another shape.
func newGrid(img image.Image, w, h, unit int) *grid {
	r := img.Bounds()
	if r.Empty() || w <= 0 || h <= 0 || unit <= 0 {
		return nil
	}
	if a := float64(r.Dx()) / float64(r.Dy()) / (float64(w) / float64(h)); a < 0.98 || a > 1.02 {
		return nil
	}
	g := &grid{w: w / unit, h: h / unit, unit: unit}
	if g.w < matchCoarse*4 || g.h < matchCoarse*4 {
		return nil
	}
	cols := make([]int, r.Dx())
	for x := range cols {
		cols[x] = int((float64(x) + 0.5) * float64(w) / float64(r.Dx()) / float64(unit))
	}
	sum := make([]float32, g.w*g.h)
	n := make([]float32, g.w*g.h)
	// Big pictures skip every other pixel: a cell still averages several.
	step := 1
	if float64(r.Dx())/float64(g.w) >= 4 {
		step = 2
	}
	lum := lumaRow(img)
	row := make([]uint8, r.Dx())
	for y := 0; y < r.Dy(); y += step {
		cy := int((float64(y) + 0.5) * float64(h) / float64(r.Dy()) / float64(unit))
		if cy >= g.h {
			break
		}
		lum(y, row)
		base := cy * g.w
		for x := 0; x < len(row); x += step {
			cx := cols[x]
			if cx >= g.w {
				break
			}
			sum[base+cx] += float32(row[x])
			n[base+cx]++
		}
	}
	g.pix = sum
	for i := range sum {
		if n[i] > 0 {
			sum[i] /= n[i]
		}
	}
	return g
}

// lumaRow returns a function that writes the luma of row y of img (from
// its top) into out, with fast paths for the pictures drivers return.
func lumaRow(img image.Image) func(y int, out []uint8) {
	r := img.Bounds()
	switch m := img.(type) {
	case *image.RGBA:
		return func(y int, out []uint8) {
			p := m.Pix[y*m.Stride:]
			for x := range out {
				out[x] = uint8((77*uint32(p[4*x]) + 150*uint32(p[4*x+1]) + 29*uint32(p[4*x+2])) >> 8)
			}
		}
	case *image.NRGBA:
		return func(y int, out []uint8) {
			p := m.Pix[y*m.Stride:]
			for x := range out {
				out[x] = uint8((77*uint32(p[4*x]) + 150*uint32(p[4*x+1]) + 29*uint32(p[4*x+2])) >> 8)
			}
		}
	case *image.YCbCr:
		return func(y int, out []uint8) { copy(out, m.Y[y*m.YStride:]) }
	case *image.Gray:
		return func(y int, out []uint8) { copy(out, m.Pix[y*m.Stride:]) }
	}
	return func(y int, out []uint8) {
		for x := range out {
			out[x] = color.GrayModel.Convert(img.At(r.Min.X+x, r.Min.Y+y)).(color.Gray).Y
		}
	}
}

// shrink averages k×k cells into one.
func (g *grid) shrink(k int) *grid {
	s := &grid{w: g.w / k, h: g.h / k, unit: g.unit * k}
	s.pix = make([]float32, s.w*s.h)
	for y := 0; y < s.h*k; y++ {
		for x := 0; x < s.w*k; x++ {
			s.pix[(y/k)*s.w+x/k] += g.pix[y*g.w+x]
		}
	}
	for i := range s.pix {
		s.pix[i] /= float32(k * k)
	}
	return s
}

// cells lists the cells whose middles lie in r.
func (g *grid) cells(r screen.Rect) (x0, y0, x1, y1 int) {
	u := g.unit
	x0, y0 = max(0, (r.X+u/2)/u), max(0, (r.Y+u/2)/u)
	x1, y1 = min(g.w, (r.X+r.W+u/2)/u), min(g.h, (r.Y+r.H+u/2)/u)
	return
}

// stayed reports whether the element at m shows the same in both pictures
// where it was; ok is false when it is too plain to tell.
func stayed(b, d *grid, m screen.Rect) (still, ok bool) {
	x0, y0, x1, y1 := b.cells(m)
	var tv, dv []float32
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			tv = append(tv, b.pix[y*b.w+x])
			dv = append(dv, d.pix[y*d.w+x])
		}
	}
	if len(tv) < 16 || stddev(tv) < matchFlat {
		return false, false
	}
	return ncc(tv, dv) >= matchStill, true
}

// cut is the part of the before picture findDrop looks for: a region of
// the screen aligned to coarse cells (screen pixels), and which of its
// fine cells count.
type cut struct {
	r    screen.Rect
	mask []bool // fine cells of r, row by row
}

// cutAround cuts the elements in marks around to, from a window that grows
// until it holds enough of them.
func cutAround(b *grid, marks []screen.Rect, to screen.Point, w, unit int) (cut, bool) {
	cu := unit * matchCoarse
	for _, side := range []float64{0.3, 0.45, 0.6, 0.9} {
		half := int(side * float64(w) / 2)
		win := screen.Rect{X: to.X - half, Y: to.Y - half, W: 2 * half, H: 2 * half}
		// The fine cells in the window that an element covers.
		wx0, wy0, wx1, wy1 := b.cells(win)
		in := make(map[int]bool)
		bx0, by0, bx1, by1 := b.w, b.h, 0, 0
		for _, m := range marks {
			mx0, my0, mx1, my1 := b.cells(m)
			for y := max(my0, wy0); y < min(my1, wy1); y++ {
				for x := max(mx0, wx0); x < min(mx1, wx1); x++ {
					in[y*b.w+x] = true
					bx0, by0, bx1, by1 = min(bx0, x), min(by0, y), max(bx1, x+1), max(by1, y+1)
				}
			}
		}
		if len(in) < 300 {
			continue
		}
		// Align to coarse cells, outwards.
		k := matchCoarse
		bx0, by0 = bx0/k*k, by0/k*k
		bx1, by1 = min((bx1+k-1)/k*k, b.w/k*k), min((by1+k-1)/k*k, b.h/k*k)
		c := cut{r: screen.Rect{X: bx0 * unit, Y: by0 * unit, W: (bx1 - bx0) * unit, H: (by1 - by0) * unit}}
		c.mask = make([]bool, (bx1-bx0)*(by1-by0))
		var vals []float32
		for y := by0; y < by1; y++ {
			for x := bx0; x < bx1; x++ {
				if in[y*b.w+x] {
					c.mask[(y-by0)*(bx1-bx0)+x-bx0] = true
					vals = append(vals, b.pix[y*b.w+x])
				}
			}
		}
		if stddev(vals) < matchFlat || c.r.W < 2*cu || c.r.H < 2*cu {
			continue
		}
		return c, true
	}
	return cut{}, false
}

// level is the cut at one grid's cells.
type level struct {
	src        *grid
	x0, y0     int // the cut's first cell
	cw, ch     int // its size in cells
	mask       []bool
	unit       int
	minW, minH int
}

func (c cut) at(g *grid, fine []bool, k int) level {
	l := level{src: g, x0: c.r.X / g.unit, y0: c.r.Y / g.unit, cw: c.r.W / g.unit, ch: c.r.H / g.unit, unit: g.unit}
	if k == 1 {
		l.mask = fine
		return l
	}
	fw := c.r.W / (g.unit / k)
	l.mask = make([]bool, l.cw*l.ch)
	for y := 0; y < l.ch; y++ {
		for x := 0; x < l.cw; x++ {
			n := 0
			for dy := 0; dy < k; dy++ {
				for dx := 0; dx < k; dx++ {
					if fine[(y*k+dy)*fw+x*k+dx] {
						n++
					}
				}
			}
			l.mask[y*l.cw+x] = 2*n >= k*k
		}
	}
	return l
}

// tmpl is the cut scaled by s: the offsets of its counted cells in the
// picture searched (row width stride), and their values less their mean.
type tmpl struct {
	s      float64
	w, h   int
	off    []int
	v      []float32
	energy float64
}

// scaled samples the cut at scale s for a picture of row width stride: the
// result's cell (i, j) shows the cut's point ((i+0.5)/s, (j+0.5)/s).
func (l level) scaled(s float64, stride int) (tmpl, bool) {
	t := tmpl{s: s, w: int(math.Round(float64(l.cw) * s)), h: int(math.Round(float64(l.ch) * s))}
	var sum float64
	for j := 0; j < t.h; j++ {
		sy := (float64(j)+0.5)/s - 0.5
		for i := 0; i < t.w; i++ {
			sx := (float64(i)+0.5)/s - 0.5
			mx, my := int(math.Round(sx)), int(math.Round(sy))
			if mx < 0 || my < 0 || mx >= l.cw || my >= l.ch || !l.mask[my*l.cw+mx] {
				continue
			}
			v := l.src.bilinear(float64(l.x0)+sx, float64(l.y0)+sy)
			t.off = append(t.off, j*stride+i)
			t.v = append(t.v, v)
			sum += float64(v)
		}
	}
	if len(t.v) < 12 {
		return t, false
	}
	mean := float32(sum / float64(len(t.v)))
	for i := range t.v {
		t.v[i] -= mean
		t.energy += float64(t.v[i]) * float64(t.v[i])
	}
	return t, t.energy > 0
}

func (g *grid) bilinear(x, y float64) float32 {
	x = math.Max(0, math.Min(x, float64(g.w-1)))
	y = math.Max(0, math.Min(y, float64(g.h-1)))
	x0, y0 := int(x), int(y)
	x1, y1 := min(x0+1, g.w-1), min(y0+1, g.h-1)
	fx, fy := float32(x-float64(x0)), float32(y-float64(y0))
	a := g.pix[y0*g.w+x0]*(1-fx) + g.pix[y0*g.w+x1]*fx
	b := g.pix[y1*g.w+x0]*(1-fx) + g.pix[y1*g.w+x1]*fx
	return a*(1-fy) + b*fy
}

// score is the correlation of t with d at cell x, y.
func (t tmpl) score(d *grid, x, y int) float64 {
	base := y*d.w + x
	var sd, sdd, std float64
	for k, o := range t.off {
		v := float64(d.pix[base+o])
		sd += v
		sdd += v * v
		std += float64(t.v[k]) * v
	}
	n := float64(len(t.off))
	vd := sdd - sd*sd/n
	if vd <= n { // flat: under one gray level of deviation
		return 0
	}
	return std / math.Sqrt(t.energy*vd)
}

// place is one place the cut may have gone: its top-left cell at scale s,
// and where that puts the drop point.
type place struct {
	s     float64
	x, y  int
	score float64
	to    screen.Point
}

// search looks for the cut in d, as a shrink toward shrinkCentre.
func (c cut) search(b, d *grid, to screen.Point, w, h int) dropFind {
	cb, cd := b.shrink(matchCoarse), d.shrink(matchCoarse)
	coarse := c.at(cb, c.mask, matchCoarse)
	fine := c.at(b, c.mask, 1)
	var found []place
	for s := matchMinScale; s <= matchMaxScale+1e-9; s += 0.02 {
		found = append(found, c.scan(coarse, cd, s, to, w, h, 1, nil)...)
	}
	if len(found) == 0 {
		return dropFind{Why: "no match"}
	}
	// The best places that put the drop point apart, refined on the fine
	// grid.
	sort.Slice(found, func(i, j int) bool { return found[i].score > found[j].score })
	apart := float64(w) / 12
	var best []place
	for _, p := range found {
		if len(best) == 3 {
			break
		}
		if p.score < matchAccept-0.2 {
			break
		}
		near := false
		for _, q := range best {
			if dist(p.to, q.to) < apart {
				near = true
				break
			}
		}
		if !near {
			best = append(best, p)
		}
	}
	if len(best) == 0 {
		return dropFind{Score: found[0].score, Why: "no clear match"}
	}
	var refined []place
	for _, p := range best {
		var top place
		for s := p.s - 0.02; s <= p.s+0.02+1e-9; s += 0.01 {
			if s < matchMinScale-0.02 || s > matchMaxScale+0.02 {
				continue
			}
			for _, q := range c.scan(fine, d, s, to, w, h, 0, &p.to) {
				if q.score > top.score {
					top = q
				}
			}
		}
		if top.score > 0 {
			refined = append(refined, top)
		}
	}
	sort.Slice(refined, func(i, j int) bool { return refined[i].score > refined[j].score })
	if len(refined) == 0 || refined[0].score < matchAccept {
		f := dropFind{Why: "no clear match"}
		if len(refined) > 0 {
			f.Score = refined[0].score
		}
		return f
	}
	top := refined[0]
	for _, q := range refined[1:] {
		if dist(q.to, top.to) >= apart && q.score >= top.score-matchClear {
			return dropFind{Score: top.score, Why: "more than one place matches"}
		}
	}
	f := dropFind{To: top.to, Scale: top.s, Score: top.score}
	f.Shrunk = top.s <= matchShrunk && dist(top.to, to) >= 3
	return f
}

// scan scores the cut at scale s at every top-left cell of d that a shrink
// toward shrinkCentre allows, with margin cells around them; or, with
// around, at the cells within a coarse cell of where the drop point would
// be at around.
func (c cut) scan(l level, d *grid, s float64, to screen.Point, w, h, margin int, around *screen.Point) []place {
	t, ok := l.scaled(s, d.w)
	if !ok {
		return nil
	}
	u := float64(d.unit)
	ox, oy := float64(c.r.X), float64(c.r.Y)
	var x0, y0, x1, y1 int
	if around != nil {
		// The top-left that puts the drop point at around.
		cx := (float64(around.X) - s*(float64(to.X)-ox)) / u
		cy := (float64(around.Y) - s*(float64(to.Y)-oy)) / u
		r := matchCoarse + 1
		x0, y0, x1, y1 = int(cx)-r, int(cy)-r, int(cx)+r, int(cy)+r
	} else {
		// The top-left is (1-s)·centre + s·origin, the centre anywhere in
		// shrinkCentre.
		k := shrinkCentre
		x0 = int(math.Floor(((1-s)*k.x0*float64(w)+s*ox)/u)) - margin
		x1 = int(math.Ceil(((1-s)*k.x1*float64(w)+s*ox)/u)) + margin
		y0 = int(math.Floor(((1-s)*k.y0*float64(h)+s*oy)/u)) - margin
		y1 = int(math.Ceil(((1-s)*k.y1*float64(h)+s*oy)/u)) + margin
	}
	x0, y0 = max(x0, 0), max(y0, 0)
	x1, y1 = min(x1, d.w-t.w), min(y1, d.h-t.h)
	var out []place
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			sc := t.score(d, x, y)
			if sc <= 0 {
				continue
			}
			p := place{s: s, x: x, y: y, score: sc}
			p.to = screen.Point{
				X: int(math.Round(float64(x)*u + s*(float64(to.X)-ox))),
				Y: int(math.Round(float64(y)*u + s*(float64(to.Y)-oy))),
			}
			out = append(out, p)
		}
	}
	return out
}

func ncc(a, b []float32) float64 {
	var sa, sb, saa, sbb, sab float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		sa += x
		sb += y
		saa += x * x
		sbb += y * y
		sab += x * y
	}
	n := float64(len(a))
	va, vb := saa-sa*sa/n, sbb-sb*sb/n
	if va <= 0 || vb <= 0 {
		return 0
	}
	return (sab - sa*sb/n) / math.Sqrt(va*vb)
}

func stddev(v []float32) float64 {
	if len(v) == 0 {
		return 0
	}
	var s, ss float64
	for _, x := range v {
		s += float64(x)
		ss += float64(x) * float64(x)
	}
	n := float64(len(v))
	return math.Sqrt(math.Max(0, ss/n-(s/n)*(s/n)))
}

func dist(a, b screen.Point) float64 {
	return math.Hypot(float64(a.X-b.X), float64(a.Y-b.Y))
}

// rectDist is how far p is from r; 0 inside it.
func rectDist(r screen.Rect, p screen.Point) float64 {
	dx := max(r.X-p.X, 0, p.X-(r.X+r.W))
	dy := max(r.Y-p.Y, 0, p.Y-(r.Y+r.H))
	return math.Hypot(float64(dx), float64(dy))
}

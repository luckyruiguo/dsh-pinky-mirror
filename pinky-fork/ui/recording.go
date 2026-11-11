package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/recording"
	"github.com/DIYgod/pinky/internal/video"
)

// Recordings: the gateway keeps a task's screen as an MP4 file with a mark
// per step. Activity plays one in the mirror's view, with the same decoder,
// from recording.Open (or a fake's OpenRecording), and jumps to a step by
// opening the recording again at its mark.

// recState is the recordings' part of the client.
type recState struct {
	views map[string]*recEntry // by task id
	pl    *recPlayer           // the player Activity shows
}

type recEntry struct {
	rec     gateway.RecordingView
	err     error
	loading bool
	at      time.Time // when the gateway answered
}

// recordingOf returns a task's recording, looking it up in the background
// the first time; nil while it loads or when the task has none. A task
// without one is asked again after a while: the gateway may still be
// writing the file of a task that just ended.
func (a *app) recordingOf(taskID string) *gateway.RecordingView {
	rf, ok := a.api.(recordingFinder)
	if !ok || taskID == "" {
		return nil
	}
	if e, ok := a.rec.views[taskID]; ok && (e.loading || e.err == nil || time.Since(e.at) < 20*time.Second) {
		if e.loading || e.err != nil {
			return nil
		}
		return &e.rec
	}
	e := &recEntry{loading: true}
	a.rec.views[taskID] = e
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
		defer cancel()
		rec, err := rf.Recording(ctx, taskID)
		a.post(func() {
			e.loading, e.rec, e.err, e.at = false, rec, err, time.Now()
		})
	}()
	return nil
}

// recordingWide is the widest the recording player gets, for a phone
// recorded in landscape.
const recordingWide = 640

// recPlayer plays a recording. Its fields are used on the main thread.
type recPlayer struct {
	a      *app
	task   string
	rec    gateway.RecordingView
	gen    int
	cancel context.CancelFunc
	player *video.Player
	bitmap *mg.Bitmap
	picW   int // the picture's size the right way up
	picH   int
	turn   int    // quarter turns clockwise that put the picture upright
	state  string // "loading", "playing", "paused", "ended", "failed"
	err    error
	pos    time.Duration // the clock: where the player is, from the recording's start
	pic    time.Duration // the shown picture's PTS, which may sit before pos while paused
	from   time.Duration // where the stream was opened
	rebase bool          // the stream is a fake's, whose PTS may count from where it opened
	base   time.Duration // added to the stream's PTS, when they count from where it opened
	first  bool          // the next picture is the stream's first
	// A stream opens at the key frame at or before from and sends the
	// pictures up to from at once. They wait in lead, unshown, so a jump
	// rewinds neither the picture nor the time; the last of them is the
	// picture at from, shown once the stream has caught up: when a picture
	// past from comes, none comes for leadSettle, or the stream ends.
	lead    *video.Frame
	leadAt  time.Time
	settled bool // the picture at from shows
	still   bool // paused: the stream only fetches the picture at from
	// catching is set while Open feeds catch-up pictures through OnFrame:
	// Take only keeps the newest, so each decoded picture is taken there
	// before the next overwrites it, then advanced on the main thread.
	catching atomic.Bool
	// A paused seek may show its lead after a newer Take, when the player
	// may write into it again, so it keeps the lead in one of copies: the
	// one the bitmap does not show (shownCopy, or -1).
	copies    [2]video.YUV
	shownCopy int
	scrubbing bool          // the timeline is held: show scrubTo, seek once it lets go
	scrubTo   time.Duration // the time under the pointer while scrubbing
}

// leadSettle is how long a stream sends no picture before the one waiting
// in lead counts as the picture at from.
const leadSettle = 150 * time.Millisecond

// openRecording starts playing a task's recording from its start.
func (a *app) openRecording(taskID string, rec gateway.RecordingView) {
	a.closeRecording()
	pl := &recPlayer{a: a, task: taskID, rec: rec, shownCopy: -1}
	a.rec.pl = pl
	pl.open(0, true)
}

func (a *app) closeRecording() {
	if pl := a.rec.pl; pl != nil {
		pl.stop()
		a.rec.pl = nil
	}
}

// stop ends the stream, keeping the picture.
func (pl *recPlayer) stop() {
	pl.gen++
	if pl.cancel != nil {
		pl.cancel()
		pl.cancel = nil
	}
	if p := pl.player; p != nil {
		pl.player = nil
		go p.Close()
	}
}

// open opens the recording at from, to play it on (run) or, paused, to
// show the picture there and stop. The picture shown stays until the one at
// from, so a jump does not flash.
func (pl *recPlayer) open(from time.Duration, run bool) {
	pl.stop()
	gen := pl.gen
	ctx, cancel := context.WithCancel(pl.a.ctx)
	pl.cancel = cancel
	pl.from, pl.pos, pl.err, pl.first = from, from, nil, true
	pl.lead, pl.settled, pl.still = nil, false, !run
	rp, rebase := pl.a.api.(recordingPlayer)
	// Real files catch up in OnFrame; a fake's stream is taken on paint.
	pl.catching.Store(!rebase)
	pl.state = "paused"
	if run {
		pl.state = "loading"
	}
	// pic stays until advance shows a new one, so a jump does not flash.
	a, rec := pl.a, pl.rec
	go func() {
		var s driver.Stream
		var err error
		if rebase {
			s, err = rp.OpenRecording(ctx, rec, from)
		} else {
			s, err = recording.Open(ctx, rec.Path, from)
		}
		var p *video.Player
		if err == nil {
			opt := video.PlayerOptions{YUV: true}
			if !rebase {
				// Catch-up frames decode faster than paints: Take only keeps
				// the newest, so take each picture here before the next
				// overwrites it. ready is closed after p is assigned:
				// NewPlayer starts decoding before it returns.
				ready := make(chan struct{})
				opt.OnFrame = func() {
					<-ready
					if !pl.catching.Load() {
						return
					}
					f := p.Take()
					if f == nil {
						return
					}
					held := cloneRecFrame(f)
					a.post(func() { pl.feed(gen, p, rebase, held) })
				}
				p, err = video.NewPlayer(s, opt)
				close(ready)
			} else {
				p, err = video.NewPlayer(s, opt)
			}
			if err != nil {
				s.Close()
			}
		}
		a.post(func() {
			if pl.gen != gen || a.rec.pl != pl {
				if p != nil {
					go p.Close()
				}
				return
			}
			if err != nil {
				pl.catching.Store(false)
				pl.state, pl.err = "failed", err
				return
			}
			pl.player, pl.rebase = p, rebase
			if pl.state == "loading" {
				pl.state = "playing"
			}
			go func() {
				<-p.Done()
				a.post(func() {
					if pl.player == p {
						pl.ended(p)
					}
				})
			}()
		})
	}()
}

// feed is the main-thread side of a catch-up picture from OnFrame.
func (pl *recPlayer) feed(gen int, p *video.Player, rebase bool, f *video.Frame) {
	if pl.gen != gen || pl.a.rec.pl != pl {
		return
	}
	if pl.player == nil {
		pl.player, pl.rebase = p, rebase
		if pl.state == "loading" {
			pl.state = "playing"
		}
	}
	if pl.player != p || pl.settled {
		return
	}
	pl.advance(f)
	if pl.settled {
		pl.catching.Store(false)
	}
}

// ended follows the end of the stream p, the one playing.
func (pl *recPlayer) ended(p *video.Player) {
	still := pl.still
	pl.catching.Store(false)
	if !pl.settled {
		// Take may still hold the last catch-up picture; a paint often has
		// not run yet when the stream ends right after those frames.
		if f := p.Take(); f != nil {
			pl.advance(f)
		}
		if !pl.settled {
			pl.settle()
		}
	}
	pl.still = false
	pl.stop()
	if e := p.Err(); e != nil && !errors.Is(e, io.EOF) && !errors.Is(e, context.Canceled) {
		pl.state, pl.err = "failed", e
		return
	}
	if !still {
		pl.state, pl.pos = "ended", pl.rec.Length
	}
}

// pause stops at the picture shown; before the picture at from shows, the
// stream goes on to fetch it.
func (pl *recPlayer) pause() {
	if !pl.running() {
		return
	}
	pl.state = "paused"
	if pl.settled {
		pl.stop()
		return
	}
	pl.still = true
	if pl.lead != nil {
		pl.keep(pl.lead)
	}
}

// resume plays on from the picture shown, or from the start once it ended.
func (pl *recPlayer) resume() {
	from := pl.pos
	if pl.state == "ended" || from >= pl.rec.Length {
		from = 0
	}
	pl.open(from, true)
}

// seek jumps to at, playing on when the recording plays.
func (pl *recPlayer) seek(at time.Duration) {
	pl.scrubbing = false
	pl.open(max(0, min(at, pl.rec.Length)), pl.running())
}

func (pl *recPlayer) running() bool {
	return pl.state == "playing" || pl.state == "loading"
}

func (pl *recPlayer) shownPos() time.Duration {
	if pl.scrubbing {
		return pl.scrubTo
	}
	return pl.pos
}

// take shows the newest picture, at paint time.
func (pl *recPlayer) take(pt *mg.Painter) {
	p := pl.player
	if p == nil {
		return
	}
	pt.AnimationFrameRate(60)
	if pl.settled {
		if f := p.Take(); f != nil {
			pl.advance(f)
		}
		return
	}
	if pl.catching.Load() {
		// OnFrame is taking catch-up pictures.
		if pl.lead != nil && time.Since(pl.leadAt) >= leadSettle {
			pl.settle()
		}
		return
	}
	for !pl.settled {
		f := p.Take()
		if f == nil {
			if pl.lead != nil && time.Since(pl.leadAt) >= leadSettle {
				pl.settle()
			}
			return
		}
		pl.advance(f)
	}
}

// advance places f: catch-up frames wait in lead; once past from, a paused
// seek shows the lead when Take dropped nothing between them (f follows
// lead's Seq), else f itself.
func (pl *recPlayer) advance(f *video.Frame) {
	at := pl.frameAt(f)
	switch {
	case pl.settled:
		pl.show(f, max(pl.pos, at))
	case at < pl.from:
		pl.keep(f)
	case pl.still && at > pl.from && pl.lead != nil && f.Seq == pl.lead.Seq+1:
		pl.settle()
	default:
		pl.lead, pl.settled = nil, true
		pl.show(f, at)
		if pl.still {
			pl.settle()
		}
	}
}

// settle shows the picture at from, and stops there when paused.
func (pl *recPlayer) settle() {
	if pl.lead != nil {
		pl.show(pl.lead, pl.from)
		pl.lead = nil
	}
	pl.settled = true
	pl.catching.Store(false)
	if pl.still {
		pl.still = false
		pl.stop()
	}
}

// cloneRecFrame copies f so it still holds after the player reuses the original.
func cloneRecFrame(f *video.Frame) *video.Frame {
	if f == nil {
		return nil
	}
	out := *f
	if f.YUV == nil {
		return &out
	}
	y := *f.YUV
	for i, p := range f.YUV.Planes {
		y.Planes[i] = append([]byte(nil), p...)
	}
	out.YUV = &y
	return &out
}

// keep holds f as the lead, the newest picture before from.
func (pl *recPlayer) keep(f *video.Frame) {
	pl.leadAt = time.Now()
	if !pl.still {
		pl.lead = f
		return
	}
	c := &pl.copies[0]
	if pl.shownCopy == 0 {
		c = &pl.copies[1]
	}
	if c == f.YUV {
		return
	}
	planes := c.Planes
	*c = *f.YUV
	for i, p := range f.YUV.Planes {
		c.Planes[i] = append(planes[i][:0], p...)
	}
	kept := *f
	kept.YUV = c
	pl.lead = &kept
}

// frameAt returns where in the recording f is.
func (pl *recPlayer) frameAt(f *video.Frame) time.Duration {
	if pl.first {
		// recording.Open counts PTS from the recording's start; a fake's
		// stream counting from where it opened starts near zero instead,
		// well before from.
		pl.first, pl.base = false, 0
		if pl.rebase && pl.from > 10*time.Second && f.PTS < pl.from-10*time.Second {
			pl.base = pl.from - f.PTS
		}
	}
	return max(0, min(pl.base+f.PTS, pl.rec.Length))
}

// show puts f on screen; at is the clock, which may sit past the picture's
// PTS while a paused seek holds the last frame before from.
func (pl *recPlayer) show(f *video.Frame, at time.Duration) {
	pl.bitmap = showPlanes(pl.bitmap, f.YUV)
	pl.shownCopy = -1
	for i := range pl.copies {
		if f.YUV == &pl.copies[i] {
			pl.shownCopy = i
		}
	}
	w, h := f.Width, f.Height
	if f.Turn%2 == 1 {
		w, h = h, w
	}
	if pl.picW > 0 && (w != pl.picW || h != pl.picH) {
		pl.a.post(func() {}) // the phone turned: lay the player out again
	}
	pl.picW, pl.picH, pl.turn = w, h, f.Turn
	pl.pos, pl.pic = at, max(0, min(pl.base+f.PTS, pl.rec.Length))
}

// markAt returns the index of the last mark at or before at, or -1.
func (pl *recPlayer) markAt(at time.Duration) int {
	idx := -1
	for i, m := range pl.rec.Marks {
		if m.At <= at {
			idx = i
		}
	}
	return idx
}

// recordingView is the recording player: the phone's screen as the mirror
// shows it, Play and Pause, the time, and a timeline with a mark per step.
func (a *app) recordingView(c *mg.Context, t gateway.TaskView, pl *recPlayer) {
	p := pal(c)
	if pl.running() {
		c.After(250 * time.Millisecond) // the time and the current step move on
	}
	mg.Row(c).Gap(sp5).AlignItems(mg.Start).Wrap().Children(func() {
		kind := bezelAndroid
		for _, d := range a.devices {
			if d.Info.ID == t.DeviceID {
				kind = bezelOf(d)
			}
		}
		aspect := float32(9.0 / 19.5)
		if pl.picW > 0 && pl.picH > 0 {
			aspect = float32(pl.picW) / float32(pl.picH)
		}
		// 440 high for a portrait phone; a turned one takes at most
		// recordingWide's width instead (bezel.go).
		const bezel = 7
		w, h, sw, sh := fitBezel(aspect, recordingWide, 440, bezel)
		radius, inner := bezelRadius(kind, sw, sh, bezel)
		mg.Box(c).Size(w, h).Shrink(0).Radius(radius).Background(p.phone).Padding(bezel).Label(locales.RecordingPlayerLabel(t.Title)).
			Children(func() {
				scr := mg.Box(c).Fill().Radius(inner).Clip().Background(hex("#000000"))
				scr.Draw(func(pt *mg.Painter, r mg.Rect) {
					pl.take(pt)
					if pl.bitmap != nil {
						pt.Clip(r, inner, func() { drawTurned(pt, pl.bitmap, r, pl.turn) })
					}
					// What the step at this moment acted on, for a moment after
					// its mark.
					if i := pl.markAt(pl.pos); i >= 0 && pl.picW > 0 {
						mk := pl.rec.Marks[i]
						if pl.pos-mk.At < 2*time.Second && mk.Step >= 0 && mk.Step < len(t.Steps) {
							if s := t.Steps[mk.Step]; !s.Target.Empty() {
								pt.Clip(r, inner, func() {
									drawOverlay(pt, r, p, s, stepDone, shotMarks{}, float32(pl.picW), float32(pl.picH), false)
								})
							}
						}
					}
				})
				scr.Children(func() {
					switch {
					case pl.state == "failed":
						mg.Column(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center().Gap(8).Padding(20).Children(func() {
							mg.Icon(c, icAlert).FontSize(22).TextColor(hex("#f7a74e"))
							mg.Text(c, recordingError(pl.err)).FontSize(fsCaption).LineHeight(1.4).TextColor(hex("#d0d0d8")).TextAlign(mg.Center).MaxWidth(220)
						})
					case pl.bitmap == nil:
						mg.Column(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center().Children(func() { spinner(c) })
					}
				})
			})
		mg.Column(c).Basis(220).Grow(1).MinWidth(0).Gap(4).Label(locales.RecordingStepsLabel()).Children(func() {
			cur := pl.markAt(pl.pos)
			sectionLabel(c, locales.RecordingJumpHeading()).Margin(0, 0, sp1, 0)
			for i, mk := range pl.rec.Marks {
				i, mk := i, mk
				text := stepText(mk.Text)
				if mk.Step >= 0 && mk.Step < len(t.Steps) {
					text, _, _ = stepDisplay(t, mk.Step)
				}
				row := mg.ButtonBase(c.Key(i)).Padding(6, 10, 6, rowInset).Radius(rControl).Gap(10).AlignItems(mg.Center).Justify(mg.Start).FillWidth().
					Label(locales.RecordingJumpTo(clock(mk.At), text)).TextColor(p.ink)
				switch {
				case i == cur:
					row.Background(p.bg3)
					selectionMark(c, row)
				case row.Hovered():
					row.Background(p.bg3.Mix(p.bg2, 0.5))
				}
				if row.Clicked() {
					pl.seek(mk.At)
				}
				row.Children(func() {
					mg.Text(c, clock(mk.At)).FontSize(fsCaption).TextColor(p.ink3).Width(40).Shrink(0)
					tx := mg.Text(c, text).FontSize(fsBody).SingleLine().Grow(1).MinWidth(0)
					if i == cur {
						tx.FontWeight(fwSemibold)
					}
				})
			}
			if len(pl.rec.Marks) == 0 {
				mg.Text(c, locales.RecordingNoMarks()).FontSize(fsSmall).TextColor(p.ink2)
			}
		})
	})
	// Play, the timeline and the time.
	mg.Row(c).Gap(sp3).AlignItems(mg.Center).Children(func() {
		label, icon := locales.RecordingPause(), icPause
		if !pl.running() {
			label, icon = locales.RecordingPlay(), icPlay
			if pl.state == "ended" {
				label, icon = locales.RecordingPlayAgain(), icPlay
			}
		}
		b := button(c, "", btnOpt{kind: btnPrimary, icon: icon, label: label}).Width(34).Padding(0).Tooltip(label)
		if b.Clicked() {
			if label == locales.RecordingPause() {
				pl.pause()
			} else {
				pl.resume()
			}
		}
		a.recordingTimeline(c, t, pl).Grow(1).MinWidth(0)
		mg.Text(c, clock(pl.shownPos())+" / "+clock(pl.rec.Length)).FontSize(fsSmall).TextColor(p.ink2).Shrink(0)
	})
}

// recordingTimeline is the recording's length with a mark per step, larger
// where the person was asked; a click jumps there.
func (a *app) recordingTimeline(c *mg.Context, t gateway.TaskView, pl *recPlayer) mg.Element {
	p := pal(c)
	length := max(pl.rec.Length, time.Millisecond)
	bar := mg.Box(c).Height(30).Focusable().Role(mg.RoleSlider).Label(locales.RecordingPosition()).Cursor(mg.CursorPointer)
	bar.Range(0, length.Seconds(), pl.shownPos().Seconds())
	// A press drags the head wherever the pointer goes, past the ends too,
	// and seeks once it lets go; a press the system takes seeks nowhere.
	button, pressing := bar.PressedButton()
	switch w := bar.LayoutBounds().W; {
	case pressing && button == 0 && w > 0:
		x, _, _ := bar.PointerPosition()
		pl.scrubbing, pl.scrubTo = true, recordingAt(x, w, length)
	case bar.PressCanceled():
		pl.scrubbing = false
	case pl.scrubbing && !pressing:
		pl.seek(pl.scrubTo)
	}
	if bar.Shortcut(0, mg.KeyLeft) {
		pl.seek(pl.pos - 5*time.Second)
	} else if bar.Shortcut(0, mg.KeyRight) {
		pl.seek(pl.pos + 5*time.Second)
	}
	bar.Draw(func(pt *mg.Painter, r mg.Rect) {
		y := r.Y + r.H/2
		pt.Fill(mg.Rect{X: r.X, Y: y - 2, W: r.W, H: 4}, p.bg3, 2)
		done := r.W * float32(float64(pl.shownPos())/float64(length))
		pt.Fill(mg.Rect{X: r.X, Y: y - 2, W: done, H: 4}, p.accent, 2)
		for _, mk := range pl.rec.Marks {
			x := r.X + r.W*float32(float64(mk.At)/float64(length))
			size, col := float32(8), p.ink3
			if mk.Step >= 0 && mk.Step < len(t.Steps) && kindOf(t, mk.Step) == stepYou {
				size, col = 12, p.ink
			}
			var ring, d mg.Path
			ring.Circle(x, y, size/2+2)
			pt.FillPath(&ring, p.bg2)
			d.Circle(x, y, size/2)
			pt.FillPath(&d, col)
		}
		var head mg.Path
		head.Circle(r.X+done, y, 7)
		pt.FillPath(&head, p.accentBtn)
	})
	return bar
}

func recordingAt(x, width float32, length time.Duration) time.Duration {
	if width <= 0 {
		return 0
	}
	frac := max(0, min(x/width, 1))
	return time.Duration(float64(length) * float64(frac))
}

func recordingError(err error) string {
	switch {
	case err == nil:
		return locales.RecordingEnded()
	case errors.Is(err, driver.ErrUnsupported):
		return locales.RecordingUnsupported()
	case errors.Is(err, driver.ErrNotFound), errors.Is(err, os.ErrNotExist):
		return locales.RecordingGone()
	case errors.Is(err, video.ErrNotFound):
		return err.Error()
	}
	return locales.RecordingPlayFailed(err.Error())
}

// --- export ---

// exportFolder is where exports go: the Desktop, else the home folder.
func exportFolder() (string, error) {
	dir, err := mygo.App.Path(mygo.PathDesktop)
	if err != nil || dir == "" {
		return os.UserHomeDir()
	}
	return dir, nil
}

func revealFile(path string) { mygo.Shell.ShowItemInFolder(path) }

// exportHistory writes tasks (all finished ones when ids is empty) into a
// new folder on the Desktop as an HTML report and JSON, through the
// gateway when it exports, and shows the report in the Finder.
func (a *app) exportHistory(ids []string, name string) {
	exp, gw := a.api.(historyExporter)
	var known []gateway.TaskView
	for _, t := range a.act.tasks {
		for _, id := range ids {
			if t.ID == id {
				known = append(known, t)
			}
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
		defer cancel()
		base, err := a.exportTo()
		var dir, path string
		if err == nil {
			now := time.Now()
			dir = filepath.Join(base, freeName(base, locales.ExportFolderName(fileName(name), now.Format("2006-01-02"), now.Format("15.04.05"))))
			err = os.MkdirAll(dir, 0o755)
		}
		if err == nil && gw {
			path, err = exp.ExportHistory(ctx, ids, dir)
		}
		if (err == nil && !gw) || errors.Is(err, driver.ErrUnsupported) {
			path, err = a.exportHere(ctx, ids, known, dir)
		}
		a.post(func() {
			if err != nil {
				a.toasts = append(a.toasts, locales.ExportFailed(err.Error()))
				return
			}
			a.toasts = append(a.toasts, locales.ExportDone(filepath.Base(dir), filepath.Base(base)))
			if a.reveal != nil {
				a.reveal(path)
			}
		})
	}()
}

// exportHere writes the report itself, for a gateway that does not.
func (a *app) exportHere(ctx context.Context, ids []string, known []gateway.TaskView, dir string) (string, error) {
	tasks := known
	if len(ids) == 0 || len(known) < len(ids) {
		all, err := a.api.History(ctx, gateway.HistoryQuery{})
		if err != nil {
			return "", err
		}
		tasks = all
		if len(ids) > 0 {
			want := map[string]bool{}
			for _, id := range ids {
				want[id] = true
			}
			tasks = tasks[:0:0]
			for _, t := range all {
				if want[t.ID] {
					tasks = append(tasks, t)
				}
			}
		}
	}
	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "tasks.json"), append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "report.html")
	return path, os.WriteFile(path, []byte(reportHTML(tasks)), 0o644)
}

// fileName keeps what a file name may hold.
func fileName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < 0x20 {
			return '-'
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60])
	}
	return strings.TrimSpace(s)
}

// freeName returns name, or name with a number, so it names nothing in dir
// yet. A folder it cannot look into gets name, for MkdirAll to report.
func freeName(dir, name string) string {
	try := name
	for i := 2; i < 100; i++ {
		if _, err := os.Stat(filepath.Join(dir, try)); err != nil {
			return try
		}
		try = fmt.Sprintf("%s %d", name, i)
	}
	return try
}

package gateway

import (
	"context"
	"time"
)

// History retention (round three, decision 9): finished tasks, their
// steps, screenshots and recordings are deleted once they are older than
// the limit, when the limit is set (the client sets it at start) and once a
// day after.

// pruneEvery is how often the retention limit is applied.
const pruneEvery = 24 * time.Hour

// SetRetention keeps finished tasks, their screenshots and recordings for
// days days and deletes older ones now and once a day; 0 keeps everything.
func (g *Gateway) SetRetention(days int) {
	if days < 0 {
		days = 0
	}
	g.mu.Lock()
	g.keepDays = days
	g.mu.Unlock()
	select {
	case g.prune <- struct{}{}:
	default:
	}
}

// Retention returns the retention limit in days; 0 keeps everything.
func (g *Gateway) Retention() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.keepDays
}

func (g *Gateway) retentionLoop() {
	defer g.wg.Done()
	t := time.NewTicker(pruneEvery)
	defer t.Stop()
	for {
		select {
		case <-g.ctx.Done():
			return
		case <-g.prune:
		case <-t.C:
		}
		g.applyRetention(g.ctx)
	}
}

// applyRetention deletes what is older than the limit.
func (g *Gateway) applyRetention(ctx context.Context) {
	g.mu.Lock()
	days := g.keepDays
	var keep []string
	for _, d := range g.devices {
		if d.lease != nil {
			keep = append(keep, d.lease.task.id)
		}
	}
	for _, d := range g.devices {
		if v := d.video; v != nil {
			keep = append(keep, v.t.id)
		}
	}
	g.mu.Unlock()
	if days <= 0 || g.store == nil {
		return
	}
	g.Flush()
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	n, err := g.store.Prune(ctx, cutoff, keep)
	if err != nil {
		g.log.Warn("history retention", "err", err)
		return
	}
	if n.Tasks > 0 || n.Media > 0 || n.Recordings > 0 {
		g.log.Info("history retention", "days", days, "tasks", n.Tasks, "steps", n.Steps, "screenshots", n.Media,
			"recordings", n.Recordings, "sessions", n.Sessions)
		g.mu.Lock()
		kept := g.ended[:0]
		for _, t := range g.ended {
			if t.ended.IsZero() || !t.ended.Before(cutoff) {
				kept = append(kept, t)
			}
		}
		g.ended = kept
		g.mu.Unlock()
		g.emit(Event{Kind: EventTasks})
	}
}

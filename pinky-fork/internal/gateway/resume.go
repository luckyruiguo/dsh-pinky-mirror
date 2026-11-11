package gateway

import (
	"fmt"
	"slices"
	"time"

	"github.com/DIYgod/pinky/internal/approval"
)

// Resuming sessions. A lease belongs to one session, and a session to one
// MCP session. Some MCP clients over HTTP open a new MCP session for every
// call, closing the last one or leaving it open, so the next call finds no
// phone, or finds it leased to a session nobody uses until it idles out.
// For agents whose sessions resume (AgentInfo.Resume), a newer session of
// the same agent continues an older one's lease when it needs the phone
// and that session is not using it at the moment: the task goes on, with
// its approvals and grants. It never goes the other way, so of two
// sessions of the same agent that both want a phone, the newer one wins
// it and the older one is told it is in use. A closed session's lease is
// kept for Options.ResumeGrace for a newer session to continue. Sessions
// of other agents, and agents on this computer, never take a lease this
// way.
//
// The person's Stop holds for the agent's newer sessions too, for
// stopHold: a client that opens a session for every call cannot simply
// connect again.
//
// Agents on this computer (AgentInfo.Reconnect) resume more narrowly. Their
// MCP session lasts as long as the agent's: the stdio shim keeps it across
// the app's restarts (cmd/pinky/shimconn.go). But a client may still drop
// it and start a new one, as Claude Desktop does after a call times out
// (field test, 2026-11-10), and the task would end with its grants. So a
// closed session's lease is kept for Options.LocalResumeGrace, and a newer
// session of the same agent continues it; one of a session still open
// only from phone_connect with the same task, as the new session may come
// up before the client closes the old one. Two sessions of one agent that
// run side by side, as two windows of an app do, never take each other's
// phones otherwise.
//
// Either way, a phone_connect for another task never continues a lease:
// one client may run several conversations, so the newer session may be
// another conversation's, and the task it would end may be running
// (shared.go).

// stopHold is how long a Stop keeps a resuming agent's newer sessions off
// the phone.
const stopHold = 10 * time.Minute

// resumeStop is a Stop of a resuming agent's task, which its newer
// sessions inherit.
type resumeStop struct {
	agent  string
	device string // "" for Stop all
	seq    uint64 // the stopped session's
	at     time.Time
}

// continues reports whether s may continue the work of o on lease l: a
// newer session of the same agent, both resuming; or, for agents on this
// computer, o closed, or task names l's task. task is phone_connect's, or
// "" for other calls, which name none; a phone_connect for another task
// never continues l, as it may come from another conversation sharing the
// client's session (shared.go). The caller holds g.mu.
func (s *Session) continues(o *Session, l *lease, task string) bool {
	if o == s || o.agent.Name != s.agent.Name || o.seq >= s.seq {
		return false
	}
	if task != "" && l.task.title != task {
		return false
	}
	switch {
	case s.agent.Resume && o.agent.Resume:
		return true
	case s.agent.Reconnect && o.agent.Reconnect:
		return o.closed || task != ""
	}
	return false
}

// adoptLocked moves to s the lease on d that an older session of the same
// agent holds and is not using, with its open approvals, and returns it;
// nil when there is none. task is phone_connect's task, or "". The caller
// holds g.mu.
func (g *Gateway) adoptLocked(s *Session, d *device, task string) *lease {
	l := d.lease
	if l == nil || l.ended || l.inflight > 0 || !s.continues(l.session, l, task) || g.barredLocked(s, d) {
		return nil
	}
	old := l.session
	delete(old.leases, d.id)
	old.successor = s
	l.session = s
	l.lastUsed = time.Now()
	s.leases[d.id] = l
	if !slices.Contains(s.used, d.id) {
		s.used = append(s.used, d.id)
	}
	leaseID := l.id
	g.approvals.Rebind(func(i approval.Info) bool { return i.Binding.Lease == leaseID }, s.id)
	g.log.Info("a newer session continues the lease", "device", d.id, "agent", s.agent.Name, "task", l.task.title)
	// The conversations that shared the older session share this one.
	if old.shared {
		g.markSharedLocked(s)
	}
	g.noteSharedLocked(s)
	return l
}

// adoptableLocked returns the phones whose leases s may continue, for
// task as adoptLocked takes it.
func (g *Gateway) adoptableLocked(s *Session, task string) []*device {
	var out []*device
	for _, d := range g.devices {
		if l := d.lease; l != nil && !l.ended && l.inflight == 0 && s.continues(l.session, l, task) && !g.barredLocked(s, d) {
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(a, b *device) int {
		if a.id < b.id {
			return -1
		}
		return 1
	})
	return out
}

// busyElsewhereLocked returns a result when an older session of the same
// agent holds d and is in the middle of a call on it. task is as for
// adoptLocked.
func (g *Gateway) busyElsewhereLocked(s *Session, d *device, task string) *Result {
	l := d.lease
	if l == nil || l.ended || l.inflight == 0 || !s.continues(l.session, l, task) {
		return nil
	}
	holder := fmt.Sprintf("%s · %s", l.session.agent.Name, l.task.title)
	res := errResult(ErrLeaseHeld, "%s is still busy with a call %s made from an earlier connection. Try again when it has ended.", d.name(), s.agent.Name)
	res.Holder = holder
	return res
}

// barredLocked reports whether the person stopped s on d, or with Stop
// all; or stopped an older session of the same resuming agent so within
// stopHold.
func (g *Gateway) barredLocked(s *Session, d *device) bool {
	if s.barred[d.id] || s.stopped {
		return true
	}
	return g.inheritedStopLocked(s, d.id)
}

// stoppedLocked reports whether the person stopped any task of s, or of an
// older session of the same resuming agent within stopHold.
func (g *Gateway) stoppedLocked(s *Session) bool {
	return len(s.barred) > 0 || s.stopped || g.inheritedStopLocked(s, "*")
}

// inheritedStopLocked reports whether a Stop of an older session of s's
// agent holds for device, or for any device with "*".
func (g *Gateway) inheritedStopLocked(s *Session, device string) bool {
	if !s.agent.Resume {
		return false
	}
	now := time.Now()
	for _, st := range g.resumeStops {
		if st.agent == s.agent.Name && st.seq < s.seq && now.Sub(st.at) < stopHold &&
			(device == "*" || st.device == "" || st.device == device) {
			return true
		}
	}
	return false
}

// noteStopLocked keeps a Stop of a resuming agent's session on device ("":
// Stop all) for its newer sessions.
func (g *Gateway) noteStopLocked(s *Session, device string) {
	if !s.agent.Resume {
		return
	}
	now := time.Now()
	kept := g.resumeStops[:0]
	for _, st := range g.resumeStops {
		if now.Sub(st.at) < stopHold {
			kept = append(kept, st)
		}
	}
	g.resumeStops = append(kept, resumeStop{agent: s.agent.Name, device: device, seq: s.seq, at: now})
}

// keepLocked keeps the lease of a resuming agent's session that closed for
// a newer session to continue, and ends it after Options.ResumeGrace
// (LocalResumeGrace for an agent on this computer) unless one did. A
// session that continued it and closed in turn keeps it with a grace of
// its own.
func (g *Gateway) keepLocked(l *lease) {
	s := l.session
	grace := g.opt.ResumeGrace
	if !s.agent.Resume {
		grace = g.opt.LocalResumeGrace
	}
	g.log.Info("lease kept for the agent's next session", "device", l.dev.id, "agent", s.agent.Name, "grace", grace)
	time.AfterFunc(grace, func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if !g.closed && !l.ended && l.session == s {
			g.endLeaseLocked(l, OutcomeDisconnected, "")
		}
	})
}

// EndKeptLeases ends the leases kept for remote agents' next sessions, as
// when the person turns remote agents off.
func (g *Gateway) EndKeptLeases() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.endKeptLocked(OutcomeDisconnected, true)
}

// endKeptLocked ends the leases kept for agents' next sessions: only remote
// agents' with remote.
func (g *Gateway) endKeptLocked(outcome string, remote bool) {
	for _, d := range g.devices {
		if l := d.lease; l != nil && !l.ended && l.session.closed && (!remote || l.session.agent.Resume) {
			g.endLeaseLocked(l, outcome, "")
		}
	}
}

// Current returns the session that carries on s's work now: s, or the
// newest session of its agent that continued its lease.
func (s *Session) Current() *Session {
	g := s.g
	g.mu.Lock()
	defer g.mu.Unlock()
	for s.successor != nil {
		s = s.successor
	}
	return s
}

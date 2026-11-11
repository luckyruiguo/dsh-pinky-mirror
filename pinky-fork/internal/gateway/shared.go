package gateway

import (
	"fmt"
	"sort"
	"strings"
)

// One session, several tasks. A Session is one MCP session, and a lease
// belongs to it. But a client may run several conversations over one MCP
// session: Claude Cowork does, and on 2026-11-11 moved two conversations,
// one working on an iPad and one about to use an iPhone, onto one new
// session. Pinky cannot tell their calls apart, except by the task that
// phone_connect names. Two rules keep one conversation's steps off the
// other's phone:
//
//   - phone_connect for a task never ends another running task of the
//     session, nor one a closed session of its agent keeps for it
//     (resume.go): it is refused with lease_held, naming that task.
//     Without device, it goes to the phone that runs the same task, or
//     chooses as for a new task: the one phone online, or "Pass device".
//   - A session that shows two tasks at once is shared: the phones it
//     holds, and those kept for it, run tasks of different names, or a
//     phone_connect of it was refused for another task. Every call of a
//     shared session must name its phone: one without device is refused
//     instead of going to the session's one phone, which may run another
//     conversation's task. It stays shared, as the conversations may go on
//     after their tasks end.
//
// A single agent that ends each task with phone_release before it starts
// the next, as the skill tells it to, never shows two tasks at once and
// notices none of this.

// keptForLocked returns the leases that closed sessions of s's agent keep
// for it to continue (resume.go), by device id. The caller holds g.mu.
func (g *Gateway) keptForLocked(s *Session) []*lease {
	var out []*lease
	for _, d := range g.sortedDevicesLocked() {
		l := d.lease
		if l != nil && !l.ended && l.session != s && l.session.closed && s.continues(l.session, l, "") && !g.barredLocked(s, d) {
			out = append(out, l)
		}
	}
	return out
}

// noteSharedLocked marks s shared when the phones it holds and those kept
// for it run tasks of different names, or one of those was kept by a
// shared session, and reports whether s is shared. The caller holds g.mu.
func (g *Gateway) noteSharedLocked(s *Session) bool {
	if s.shared {
		return true
	}
	titles := map[string]bool{}
	for _, l := range s.leases {
		titles[l.task.title] = true
	}
	inherited := false
	for _, l := range g.keptForLocked(s) {
		titles[l.task.title] = true
		inherited = inherited || l.session.shared
	}
	if len(titles) > 1 || inherited {
		g.markSharedLocked(s)
	}
	return s.shared
}

// markSharedLocked marks s shared. The caller holds g.mu.
func (g *Gateway) markSharedLocked(s *Session) {
	if !s.shared {
		s.shared = true
		g.log.Info("several tasks share a session: its calls must name their phone", "session", s.id, "agent", s.agent.Name)
	}
}

// noteNewerSharedLocked notes, once s closed and kept its leases, the
// newer sessions of its agent that may continue them and now show two
// tasks: a client that moves its conversations to a new session, as
// Claude Cowork does, may have started a task on it before closing s.
func (g *Gateway) noteNewerSharedLocked(s *Session) {
	for _, o := range g.sessions {
		if o.seq > s.seq && o.agent.Name == s.agent.Name {
			g.noteSharedLocked(o)
		}
	}
}

// phonesLocked lists the phones s holds and those kept for it, with their
// tasks; "" when there are none. The caller holds g.mu.
func (g *Gateway) phonesLocked(s *Session) string {
	ls := g.keptForLocked(s)
	for _, l := range s.leases {
		ls = append(ls, l)
	}
	var out []string
	for _, l := range ls {
		out = append(out, fmt.Sprintf("%s (device %q, task “%s”)", l.dev.name(), l.dev.id, l.task.title))
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// sharedResultLocked answers a call without device in a shared session.
func (g *Gateway) sharedResultLocked(s *Session) *Result {
	msg := "Several tasks share this MCP session (another conversation may use it too), so Pinky cannot tell which phone a call without device is for. " +
		"Nothing was done: pass device on every call."
	if list := g.phonesLocked(s); list != "" {
		msg += " Phones of this session: " + list + "."
	} else {
		msg += " Connected phones: " + g.deviceListLocked() + "."
	}
	return errResult(ErrInvalidArgument, "%s", msg)
}

// sharedNote tells an agent whose session is shared, in a result that
// worked, to name the phone from now on.
func sharedNote(res *Result, shared bool) *Result {
	if !shared || res == nil || res.IsError() {
		return res
	}
	return withNote(res, "Several tasks share this MCP session, so pass device on every call from now on.")
}

// runsOtherTask reports whether l runs another task than task for s: s's
// own lease, or one s would continue on a call that names no task (an
// idle older session's of a resuming agent, a closed one's of an agent on
// this computer). The caller holds g.mu.
func (s *Session) runsOtherTask(l *lease, task string) bool {
	return l != nil && !l.ended && l.task.title != task && (l.session == s || s.continues(l.session, l, ""))
}

// otherTaskResultLocked refuses phone_connect on d, whose lease l runs
// another task for s (runsOtherTask), and marks s shared: the task asked
// for may be another conversation's. The caller holds g.mu.
func (g *Gateway) otherTaskResultLocked(s *Session, d *device, l *lease) *Result {
	g.markSharedLocked(s)
	whose := "this session"
	if l.session != s {
		whose = "an earlier session of " + s.agent.Name
	}
	g.log.Info("phone_connect for another task refused", "device", d.id, "agent", s.agent.Name, "running", l.task.title)
	holder := fmt.Sprintf("%s · %s", l.session.agent.Name, l.task.title)
	res := errResult(ErrLeaseHeld, "%s is running another task of %s: “%s”. Nothing was changed. "+
		"If that task is yours and finished, end it first with phone_release (device %q). If it is not yours, another conversation may share this session: "+
		"leave it alone and pass device to use another phone. Connected phones: %s.",
		d.name(), whose, l.task.title, d.id, g.deviceListLocked())
	res.Holder = holder
	return res
}

// otherTasksNoteLocked says which phones run other tasks of s than task,
// for an agent choosing a phone; "" when none do.
func (g *Gateway) otherTasksNoteLocked(s *Session, task string) string {
	var out []string
	for _, d := range g.sortedDevicesLocked() {
		if l := d.lease; s.runsOtherTask(l, task) {
			out = append(out, fmt.Sprintf("%s (“%s”)", d.name(), l.task.title))
		}
	}
	if len(out) == 0 {
		return ""
	}
	return fmt.Sprintf("Already running other tasks of %s: %s. Leave those phones to their tasks.", s.agent.Name, strings.Join(out, ", "))
}

// taskPhoneLocked returns the phone that runs task for s: one s holds, or
// one an older session of its agent left that s continues for that task
// (resume.go). nil when none or several do. The caller holds g.mu.
func (g *Gateway) taskPhoneLocked(s *Session, task string) *device {
	var out []*device
	for _, l := range s.leases {
		if l.task.title == task {
			out = append(out, l.dev)
		}
	}
	out = append(out, g.adoptableLocked(s, task)...)
	if len(out) == 1 {
		return out[0]
	}
	return nil
}

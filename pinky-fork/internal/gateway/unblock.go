package gateway

import (
	"fmt"
	"slices"

	"github.com/DIYgod/pinky/internal/approval"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// A blocked app can be allowed for one task (field test #18). When an
// agent opens a finance app, or one the person blocked, the launch asks the
// person instead of being refused: "Allow for this task" lets the agent
// open that app and read its screen until the task ends, and Settings stay
// as they are. Everything the rules ask about still asks there, read-only
// mode and scopes still hold, and Pinky keeps no pictures, recording or
// screen text of it: history has the steps only. Its text that only the
// pixels show is read by OCR from a picture the driver keeps in memory
// and drops (mayCapture); the agent gets the text, never a screenshot. A
// refusal holds for the rest of the task. Password managers are never
// offered: their screens are passwords.

// liftable reports whether the person may allow a blocked app for a task,
// and names the rule that blocks it.
func (g *Gateway) liftable(app screen.App) (string, bool) {
	rule, blocked := g.policy.Blocked(app)
	if !blocked {
		return "", false
	}
	cats := g.policy.Categories(app)
	if slices.Contains(cats, policy.CatPasswords) {
		return rule, false
	}
	return rule, slices.Contains(cats, policy.CatFinance) || slices.Contains(cats, policy.CatBlocked)
}

// allowsLocked reports whether the person allowed app for the lease's task
// although a rule blocks it.
func allowsLocked(l *lease, app screen.App) bool {
	if l == nil {
		return false
	}
	return slices.ContainsFunc(l.unblocked, func(a screen.App) bool { return sameApp(a, app) })
}

// allows is allowsLocked for callers without Gateway.mu.
func (g *Gateway) allows(l *lease, app screen.App) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return allowsLocked(l, app)
}

// unblockAsk turns the refusal of a launch of a blocked app into a
// question for the person, when they may allow the app for the task and
// have not refused it in this task. It marks p so the approval knows.
func (g *Gateway) unblockAsk(l *lease, p *prep, v policy.Verdict) (policy.Verdict, bool) {
	if v.Decision != policy.Deny || v.Scope || p.input.Op != policy.OpLaunch || p.url {
		return v, false
	}
	app := p.input.Launch
	rule, ok := g.liftable(app)
	if !ok || rule != v.Rule {
		return v, false
	}
	g.mu.Lock()
	kept := slices.ContainsFunc(l.kept, func(a screen.App) bool { return sameApp(a, app) })
	g.mu.Unlock()
	if kept {
		v.Reason = fmt.Sprintf("%s is blocked by the rule %q, and the person kept it blocked for this task.", appLabel(app), rule)
		return v, false
	}
	p.unblock = true
	return policy.Verdict{Decision: policy.Ask, Rule: rule, Kinds: v.Kinds, Asks: []string{rule},
		Reason: fmt.Sprintf("%s is blocked by the rule %q; the person can allow it for this task.", appLabel(app), rule)}, true
}

// unblockedMessage tells the agent what allowing a blocked app gave it.
func unblockedMessage(app screen.App) string {
	return fmt.Sprintf("The person allowed %s for this task: you may open it and read its screen until the task ends. "+
		"Whatever their rules ask about still asks, and you get no screenshots of it: text that only its pixels show comes as ocr lines.", appLabel(app))
}

// blockedShotMessage answers a screenshot asked for on a blocked app the
// person allowed for the task; readsText says whether the phone reads
// text from the screen's pixels on this computer.
func blockedShotMessage(readsText bool) string {
	msg := "No screenshot: Pinky gives no pictures of a blocked app, also when the person allows it for the task."
	if readsText {
		msg += " Text that only its pixels show comes as ocr lines, read from a picture Pinky does not keep."
	}
	return msg
}

// isUnblock reports whether an approval asks to allow a blocked app for the
// task (NeedView.Unblock).
func isUnblock(info approval.Info) bool {
	snap, ok := info.Data.(*snapshot)
	return ok && info.Kind == approval.KindAction && snap.unblock
}

// agentRequest is what an agent's first connection asked for.
type agentRequest struct {
	readOnly bool // phone_connect read_only
}

// askedReadOnly reports whether an agent's first connection asked to only
// browse and read (NeedView.ReadOnly).
func askedReadOnly(info approval.Info) bool {
	r, ok := info.Data.(agentRequest)
	return ok && info.Kind == approval.KindAgent && r.readOnly
}

package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// A call's budget is the time one tool call may take in all, from when the
// gateway receives it (Options.CallBudget, 45 s). MCP clients give up on a
// call after about a minute, and some (Claude Desktop) then drop the whole
// session, so a call must answer before that however slow the phone is:
// an iPhone through WebDriverAgent over Wi-Fi took up to a minute for one
// action and its reads in a field test (2026-11-10).
//
// The budget is a value on the call's context, not a deadline: it bounds
// what the gateway waits for (reads, settling, approvals, phone_batch
// steps), never an input the phone is already performing.

type budgetKey struct{}

// errOutOfTime is a read cut short because the call's budget ran out.
var errOutOfTime = errors.New("the call's time ran out")

// withBudget gives ctx the gateway's call budget, unless it has one: a
// call that calls another (Connect's first read) keeps the first's.
func (g *Gateway) withBudget(ctx context.Context) context.Context {
	if _, ok := budgetDeadline(ctx); ok {
		return ctx
	}
	return context.WithValue(ctx, budgetKey{}, time.Now().Add(g.opt.CallBudget))
}

// callCtx is the context a call on lease l runs with: cancelled when the
// lease ends, and bounded by the call's budget.
func (g *Gateway) callCtx(ctx context.Context, l *lease) (context.Context, context.CancelFunc) {
	return mergeCtx(g.withBudget(ctx), l.ctx)
}

// budgetDeadline is when the call's budget runs out.
func budgetDeadline(ctx context.Context) (time.Time, bool) {
	t, ok := ctx.Value(budgetKey{}).(time.Time)
	return t, ok
}

// budgetLeft is how much of the call's budget is left; ok is false for a
// context without one, which is not bounded.
func budgetLeft(ctx context.Context) (left time.Duration, ok bool) {
	t, ok := budgetDeadline(ctx)
	if !ok {
		return 0, false
	}
	return time.Until(t), true
}

// budgetBefore is the earlier of t and the budget's end, less reserve: a
// wait that must leave the call time for what comes after it.
func budgetBefore(ctx context.Context, t time.Time, reserve time.Duration) time.Time {
	if dl, ok := budgetDeadline(ctx); ok {
		if end := dl.Add(-reserve); end.Before(t) {
			return end
		}
	}
	return t
}

// budgetSpent reports whether the call's budget has run out.
func budgetSpent(ctx context.Context) bool {
	left, ok := budgetLeft(ctx)
	return ok && left <= 0
}

// approvalReserve is what an approval wait leaves of the call's budget for
// what follows a decision: the action and the read of its result, or the
// read after a handoff.
const approvalReserve = 10 * time.Second

// outOfTimeAfterAction is the result of an action whose effect could not be
// read before the call's budget ran out.
func outOfTimeAfterAction(budget time.Duration) *Result {
	return statusResult(StatusUncertain, "The action was sent, but the phone answered too slowly to read the screen within the call's %s. Observe before anything else; do not repeat it blindly.", human(budget))
}

// outOfTimeBeforeAction is the result of a call whose budget ran out
// before it could act or read.
func outOfTimeBeforeAction(budget time.Duration) *Result {
	return errResult(ErrInternal, "The phone answered too slowly: the screen could not be read within the call's %s. Nothing was done. Try again; if this keeps happening, observe first or use fewer steps per phone_batch.", human(budget))
}

func human(d time.Duration) string {
	return fmt.Sprintf("%d s", int(d.Round(time.Second)/time.Second))
}

// lockAct takes the device's action lock, waiting no longer than the
// call's budget, and reports whether it did.
func lockAct(ctx context.Context, d *device) bool {
	left, ok := budgetLeft(ctx)
	if !ok {
		d.act.Lock()
		return true
	}
	return lockWithin(ctx, &d.act, left)
}

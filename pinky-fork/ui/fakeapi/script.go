package fakeapi

import (
	"fmt"
	"time"

	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/screen"
)

const agentName = "Claude Code"

// Where things are on the fake screens, in device pixels. The rectangles
// follow the prototype's mock apps, scaled from its 282-point screens.
var (
	pixelDinelyIcon = screen.Rect{X: 96, Y: 1520, W: 190, H: 190}
	pixelOrdersTab  = screen.Rect{X: 330, Y: 2196, W: 210, H: 130}
	pixelReorder    = screen.Rect{X: 54, Y: 650, W: 972, H: 207}
	pixelNote       = screen.Rect{X: 54, Y: 1010, W: 972, H: 153}
	pixelPlaceOrder = screen.Rect{X: 54, Y: 2091, W: 972, H: 184}
	pixelChatRow    = screen.Rect{X: 0, Y: 420, W: 1080, H: 200}
	pixelMessage    = screen.Rect{X: 46, Y: 2210, W: 860, H: 168}
	pixelSend       = screen.Rect{X: 916, Y: 2210, W: 130, H: 168}
	iphoneTallyIcon = screen.Rect{X: 590, Y: 1690, W: 220, H: 220}
	iphoneNext      = screen.Rect{X: 103, Y: 2232, W: 1000, H: 222}
	iphoneGroupName = screen.Rect{X: 103, Y: 1570, W: 1000, H: 197}
	iphoneContinue  = screen.Rect{X: 103, Y: 2232, W: 1000, H: 222}
	iphoneSettings  = screen.Rect{X: 1000, Y: 190, W: 160, H: 160}
	iphoneDelete    = screen.Rect{X: 103, Y: 1980, W: 1000, H: 200}
)

// run drives one task's script.
type run struct {
	a   *API
	gen int
	t   *task
}

// newTask creates a task on the device; a.mu must be held.
func (a *API) newTask(d *device, title, short string) *task {
	return a.newTaskBy(d, agentName, title, short)
}

func (a *API) newTaskBy(d *device, agent, title, short string) *task {
	a.nextID++
	t := &task{
		view: gateway.TaskView{
			ID: fmt.Sprintf("task-%d", a.nextID), DeviceID: d.info.ID, DeviceName: d.info.Name,
			Agent: agent, Title: title, State: gateway.TaskWorking, Started: a.opt.Now(),
		},
		short: short, dev: d, wake: make(chan struct{}, 1),
	}
	d.task = t
	d.statusLine = agent + " · " + short
	a.tasks = append(a.tasks, t)
	return t
}

// alive reports whether the script should go on; a.mu must be held.
func (r *run) alive() bool {
	return r.a.gen == r.gen && !isFinished(r.t.view.State) && r.t.dev.task == r.t
}

// sleep waits d of the agent's working time, scaled by the speed: the
// clock stops while the person is in control or the phone is locked. It
// returns false when the task ended or another scenario started.
func (r *run) sleep(d time.Duration) bool {
	left := time.Duration(float64(d) / r.a.opt.Speed)
	const tick = 40 * time.Millisecond
	last := time.Now()
	for {
		r.a.mu.Lock()
		ok := r.alive()
		paused := r.t.view.State == gateway.TaskPaused || r.t.dev.inControl
		r.a.mu.Unlock()
		if !ok {
			return false
		}
		now := time.Now()
		if !paused {
			left -= now.Sub(last)
		}
		last = now
		if left <= 0 && !paused {
			return true
		}
		wait := tick
		if !paused && left < wait {
			wait = left
		}
		select {
		case <-r.t.wake:
		case <-time.After(wait):
		}
	}
}

func (a *API) sleepGen(gen int, d time.Duration) bool {
	end := time.Now().Add(time.Duration(float64(d) / a.opt.Speed))
	for time.Now().Before(end) {
		time.Sleep(40 * time.Millisecond)
		a.mu.Lock()
		ok := a.gen == gen
		a.mu.Unlock()
		if !ok {
			return false
		}
	}
	return true
}

// do adds a step in progress, waits, and settles it with its done text.
func (r *run) do(doing, done, tool, args string, target screen.Rect, d time.Duration) bool {
	r.a.mu.Lock()
	if !r.alive() {
		r.a.mu.Unlock()
		return false
	}
	r.t.finishCurrent()
	start := r.a.opt.Now()
	point := screen.Point{}
	if !target.Empty() {
		point = target.Center()
	}
	r.t.addStep(start, gateway.StepView{Text: doing, Tool: tool, Args: args, Target: target, Point: point})
	r.a.mu.Unlock()
	r.a.emit(gateway.Event{Kind: gateway.EventTasks, TaskID: r.t.view.ID, DeviceID: r.t.dev.info.ID})
	if !r.sleep(d) {
		return false
	}
	r.a.mu.Lock()
	if !r.alive() {
		r.a.mu.Unlock()
		return false
	}
	if n := len(r.t.view.Steps); n > 0 && r.t.view.Steps[n-1].Text == doing {
		s := &r.t.view.Steps[n-1]
		s.Text, s.Status, s.Elapsed = done, "settled", r.a.opt.Now().Sub(start)
		if tool != "" {
			s.After = mediaID(r.t.dev.info.ID)
		}
	}
	r.a.mu.Unlock()
	r.a.emit(gateway.Event{Kind: gateway.EventTasks, TaskID: r.t.view.ID, DeviceID: r.t.dev.info.ID})
	return true
}

// finish ends the task.
func (r *run) finish(state gateway.TaskState) {
	r.a.mu.Lock()
	if !r.alive() {
		r.a.mu.Unlock()
		return
	}
	r.t.finishCurrent()
	r.a.finishLocked(r.t, state)
	r.a.mu.Unlock()
	r.a.emitAll(r.t.dev.info.ID)
}

// askSpec describes an approval.
type askSpec struct {
	title, detail, app, rule string
	step, tool, args         string // the step that waits
	target                   screen.Rect
	allowed, denied          string // the person's answer as steps
}

// ask asks the person for approval and waits for the answer. ok is false
// when the task ended or the scenario changed meanwhile.
func (r *run) ask(s askSpec) (dec decision, ok bool) {
	a := r.a
	shot := a.screenshot(r.t.dev)
	a.mu.Lock()
	if !r.alive() {
		a.mu.Unlock()
		return decision{}, false
	}
	r.t.finishCurrent()
	now := a.opt.Now()
	r.t.addStep(now, gateway.StepView{Text: s.step, Tool: s.tool, Args: s.args, Status: "approval_pending",
		Target: s.target, Point: s.target.Center(), Before: mediaID(r.t.dev.info.ID)})
	stepIndex := len(r.t.view.Steps) - 1
	a.nextID++
	n := &need{
		view: gateway.NeedView{
			ID: fmt.Sprintf("ap-%d", a.nextID), Kind: gateway.NeedApproval,
			DeviceID: r.t.dev.info.ID, DeviceName: r.t.dev.info.Name, App: s.app, Agent: agentName,
			TaskID: r.t.view.ID, Title: s.title, Detail: s.detail, Rule: s.rule,
			Created: now, Deadline: now.Add(a.opt.ApprovalTimeout), Screenshot: shot, Target: s.target,
			AlwaysAllow: s.rule != "" && s.app != "", AllowTask: s.rule != "",
		},
		decision: make(chan decision, 1),
	}
	a.needs = append(a.needs, n)
	r.t.view.State = gateway.TaskWaiting
	a.mu.Unlock()
	a.emitAll(r.t.dev.info.ID)

	timer := time.NewTimer(a.opt.ApprovalTimeout)
	defer timer.Stop()
	timedOut := false
	select {
	case d, open := <-n.decision:
		if !open {
			return decision{}, false
		}
		dec = d
	case <-timer.C:
		a.mu.Lock()
		for i, x := range a.needs {
			if x == n {
				a.needs = append(a.needs[:i], a.needs[i+1:]...)
				break
			}
		}
		a.mu.Unlock()
		timedOut = true
		dec = decision{d: gateway.Deny}
	}

	a.mu.Lock()
	defer func() {
		a.mu.Unlock()
		a.emitAll(r.t.dev.info.ID)
	}()
	if !r.alive() {
		return decision{}, false
	}
	if dec.d == gateway.TakeOver {
		return dec, true // takeOverLocked rewrote the steps
	}
	if r.t.view.State == gateway.TaskWaiting {
		r.t.view.State = gateway.TaskWorking
	}
	if stepIndex < len(r.t.view.Steps) {
		st := &r.t.view.Steps[stepIndex]
		st.Elapsed = a.opt.Now().Sub(now)
		st.Tool, st.Args = "", ""
		switch {
		case timedOut:
			st.Text = fmt.Sprintf("No answer in %d\u00a0s, so Pinky said no: %s", int(a.opt.ApprovalTimeout.Seconds()), s.denied)
			st.Status = "approval_denied"
		case dec.d == gateway.Deny:
			st.Text, st.Status = "You denied: "+s.denied, "user"
			if dec.note != "" {
				st.Text += "\nYour note: “" + dec.note + "”"
			}
		default:
			st.Text, st.Status = "You allowed: "+s.allowed, "user"
		}
	}
	return dec, true
}

// waitUnlock asks the person to unlock the phone and waits.
func (r *run) waitUnlock(detail string) bool {
	a := r.a
	a.mu.Lock()
	if !r.alive() {
		a.mu.Unlock()
		return false
	}
	r.t.finishCurrent()
	now := a.opt.Now()
	r.t.addStep(now, gateway.StepView{Text: "Waiting for you: unlock the phone", Status: "approval_pending"})
	idx := len(r.t.view.Steps) - 1
	a.nextID++
	n := &need{
		view: gateway.NeedView{
			ID: fmt.Sprintf("unlock-%d", a.nextID), Kind: gateway.NeedUnlock,
			DeviceID: r.t.dev.info.ID, DeviceName: r.t.dev.info.Name, Agent: agentName, TaskID: r.t.view.ID,
			Title: r.t.dev.info.Name + " is locked", Detail: detail, Created: now,
		},
		decision: make(chan decision, 1),
	}
	a.needs = append(a.needs, n)
	r.t.view.State = gateway.TaskWaiting
	a.mu.Unlock()
	a.emitAll(r.t.dev.info.ID)

	if _, open := <-n.decision; !open {
		return false
	}
	a.mu.Lock()
	if !r.alive() {
		a.mu.Unlock()
		return false
	}
	r.t.view.State = gateway.TaskWorking
	st := &r.t.view.Steps[idx]
	st.Text, st.Status, st.Elapsed = "You unlocked it with Touch ID", "user", a.opt.Now().Sub(now)
	a.mu.Unlock()
	a.emitAll(r.t.dev.info.ID)
	return true
}

// lunch is the prototype's main scenario: Pixel 9 reorders last week's
// lunch in Dinely and asks before paying.
func (a *API) lunch(gen int, d *device, takenOver bool) func() {
	t := a.newTask(d, "Reorder last week's lunch", "Reorder lunch")
	if takenOver {
		now := a.opt.Now()
		t.view.Started = now.Add(-70 * time.Second)
		for _, s := range []gateway.StepView{
			{Text: "Opened Dinely", Tool: "phone_launch", Args: `{"app":"Dinely"}`, Status: "settled", Elapsed: 1200 * time.Millisecond},
			{Text: "Found last week's order", Tool: "phone_tap", Args: `{"ref":7}`, Status: "settled", Elapsed: 800 * time.Millisecond},
			{Text: "Added 1 chicken rice bowl", Tool: "phone_tap", Args: `{"ref":21}`, Status: "settled", Elapsed: 800 * time.Millisecond},
			{Text: "Added the note “No cilantro”", Tool: "phone_type", Args: `{"ref":14,"text":"No cilantro"}`, Status: "settled", Elapsed: 500 * time.Millisecond},
			{Text: "You took over", Status: "user"},
			{Text: "You are changing the note", Status: "user"},
		} {
			t.addStep(now, s)
		}
		d.inControl = true
		t.view.State = gateway.TaskPaused
	}
	return func() {
		r := &run{a: a, gen: gen, t: t}
		if takenOver {
			if !r.sleep(0) || !r.do("Checking the cart again", "Checked the cart again", "phone_observe", `{}`, screen.Rect{}, time.Second) {
				return
			}
		} else {
			steps := []struct {
				doing, done, tool, args string
				target                  screen.Rect
				d                       time.Duration
			}{
				{"Opening Dinely", "Opened Dinely", "phone_launch", `{"app":"Dinely"}`, pixelDinelyIcon, 1500 * time.Millisecond},
				{"Finding last week's order", "Found last week's order", "phone_tap", `{"ref":7,"label":"Orders"}`, pixelOrdersTab, 1800 * time.Millisecond},
				{"Adding 1 chicken rice bowl", "Added 1 chicken rice bowl", "phone_tap", `{"ref":21,"label":"Reorder · Chicken rice bowl"}`, pixelReorder, 1500 * time.Millisecond},
				{"Adding the note “No cilantro”", "Added the note “No cilantro”", "phone_type", `{"ref":14,"text":"No cilantro"}`, pixelNote, 1500 * time.Millisecond},
			}
			for _, s := range steps {
				if !r.do(s.doing, s.done, s.tool, s.args, s.target, s.d) {
					return
				}
			}
		}
		for {
			dec, ok := r.ask(askSpec{
				title: "Pay $12.80?", detail: agentName + " · 1 chicken rice bowl, no cilantro", app: "Dinely", rule: "Payments",
				step: "Waiting for you: pay $12.80", tool: "phone_tap",
				args:   `{"ref":15,"label":"Place order · $12.80","intent":"Pay $12.80 for 1 chicken rice bowl"}`,
				target: pixelPlaceOrder, allowed: "pay $12.80", denied: "pay $12.80",
			})
			if !ok {
				return
			}
			switch dec.d {
			case gateway.TakeOver:
				if !r.sleep(0) || !r.do("Checking the cart again", "Checked the cart again", "phone_observe", `{}`, screen.Rect{}, time.Second) {
					return
				}
				continue
			case gateway.Deny:
				if r.do("Ending the task", agentName+" ended the task", "phone_release", `{"outcome":"denied"}`, screen.Rect{}, 800*time.Millisecond) {
					r.finish(gateway.TaskDone)
				}
				return
			default:
				if r.do("Placing the order", "Placed the order", "phone_tap", `{"ref":15,"approval_id":"ap"}`, pixelPlaceOrder, 1500*time.Millisecond) &&
					r.do("Checking the delivery time", "Delivery in 25–35 min", "phone_wait_for", `{"text":"Order placed"}`, screen.Rect{}, 2100*time.Millisecond) {
					r.finish(gateway.TaskDone)
				}
				return
			}
		}
	}
}

// onboarding is the iPhone's task in the three-phone scenario: a test of
// Tally's onboarding, which later asks to delete a test group.
func (a *API) onboarding(gen int, d *device) func() {
	t := a.newTask(d, "Test onboarding in Tally", "Test onboarding in Tally")
	return func() {
		r := &run{a: a, gen: gen, t: t}
		steps := []struct {
			doing, done, tool, args string
			target                  screen.Rect
			d                       time.Duration
		}{
			{"Installing the Tally test build", "Installed the Tally test build", "", "", screen.Rect{}, 1500 * time.Millisecond},
			{"Opening Tally", "Opened Tally", "phone_launch", `{"app":"Tally"}`, iphoneTallyIcon, 1200 * time.Millisecond},
			{"Going past the welcome screens", "Went past the welcome screens", "phone_batch", `{"steps":3}`, iphoneNext, 2200 * time.Millisecond},
			{"Typing the group name", "Typed the group name “Weekend trip”", "phone_type", `{"ref":12,"text":"Weekend trip"}`, iphoneGroupName, 1500 * time.Millisecond},
			{"Tapping Continue", "Tapped Continue", "phone_tap", `{"ref":31,"label":"Continue"}`, iphoneContinue, 30 * time.Second},
			{"Checking the group screen", "Checked the group screen", "phone_observe", `{}`, screen.Rect{}, 4 * time.Second},
			{"Opening group settings", "Opened group settings", "phone_tap", `{"ref":40,"label":"Settings"}`, iphoneSettings, 3 * time.Second},
		}
		for _, s := range steps {
			if !r.do(s.doing, s.done, s.tool, s.args, s.target, s.d) {
				return
			}
		}
		for {
			dec, ok := r.ask(askSpec{
				title: "Delete a group?", detail: agentName + " · “Test group 2”, 3 members", app: "Tally", rule: "Deletions",
				step: "Waiting for you: delete a group", tool: "phone_tap",
				args:   `{"ref":44,"label":"Delete group","intent":"Delete the test group made by the last run"}`,
				target: iphoneDelete, allowed: "delete the group", denied: "delete the group",
			})
			if !ok {
				return
			}
			switch dec.d {
			case gateway.TakeOver:
				if !r.sleep(0) {
					return
				}
				continue
			case gateway.Deny:
				if r.do("Leaving the group", "Left “Test group 2” in place", "phone_key", `{"key":"back"}`, screen.Rect{}, time.Second) {
					r.finish(gateway.TaskDone)
				}
				return
			default:
				if r.do("Deleting “Test group 2”", "Deleted “Test group 2”", "phone_tap", `{"ref":44}`, iphoneDelete, 1500*time.Millisecond) &&
					r.do("Finishing the test", "Finished the onboarding test", "phone_release", `{"outcome":"done"}`, screen.Rect{}, time.Second) {
					r.finish(gateway.TaskDone)
				}
				return
			}
		}
	}
}

// replyToSam is the locked-phone scenario: the agent wakes Pixel 9 to
// answer Sam and finds it locked.
func (a *API) replyToSam(gen int, d *device) func() {
	t := a.newTask(d, "Reply to Sam about Saturday", "Reply to Sam")
	return func() {
		r := &run{a: a, gen: gen, t: t}
		if !r.do("Starting the task", "Started the task", "phone_connect", `{"task":"Reply to Sam about Saturday"}`, screen.Rect{}, 400*time.Millisecond) ||
			!r.do("Waking Pixel 9", "Woke Pixel 9", "phone_key", `{"key":"wake"}`, screen.Rect{}, 600*time.Millisecond) ||
			!r.waitUnlock(agentName+" is waiting to reply to Sam.") {
			return
		}
		if !r.do("Opening Chatter", "Opened Chatter", "phone_launch", `{"app":"Chatter"}`, pixelDinelyIcon, 1200*time.Millisecond) ||
			!r.do("Opening the chat with Sam", "Opened the chat with Sam", "phone_tap", `{"ref":9,"label":"Sam"}`, pixelChatRow, 4*time.Second) ||
			!r.do("Writing the reply", "Wrote “Saturday at 3 works”", "phone_type", `{"ref":22,"text":"Saturday at 3 works"}`, pixelMessage, 1800*time.Millisecond) {
			return
		}
		for {
			dec, ok := r.ask(askSpec{
				title: "Send a message to Sam?", detail: agentName + " · “Saturday at 3 works”", app: "Chatter", rule: "Send in Chatter",
				step: "Waiting for you: send the message", tool: "phone_tap",
				args:   `{"ref":23,"label":"Send","intent":"Tell Sam that Saturday at 3 works"}`,
				target: pixelSend, allowed: "send the message", denied: "send the message",
			})
			if !ok {
				return
			}
			switch dec.d {
			case gateway.TakeOver:
				if !r.sleep(0) {
					return
				}
				continue
			case gateway.Deny:
				if r.do("Ending the task", agentName+" ended the task", "phone_release", `{"outcome":"denied"}`, screen.Rect{}, 800*time.Millisecond) {
					r.finish(gateway.TaskDone)
				}
				return
			default:
				if r.do("Sending the message", "Message delivered", "phone_tap", `{"ref":23}`, pixelSend, 1500*time.Millisecond) {
					r.finish(gateway.TaskDone)
				}
				return
			}
		}
	}
}

// handoff asks the person to do something on the phone, as phone_handoff
// does, and waits: Done hands the phone back, Deny declines, and Take over
// leaves the step open until the person hands back.
func (r *run) handoff(reason, app string) (dec decision, ok bool) {
	a := r.a
	a.mu.Lock()
	if !r.alive() {
		a.mu.Unlock()
		return decision{}, false
	}
	r.t.finishCurrent()
	now := a.opt.Now()
	r.t.addStep(now, gateway.StepView{Text: "Asked you to take over: " + reason, Tool: "phone_handoff",
		Args: `{"reason":"` + reason + `"}`, Status: "approval_pending", Before: mediaID(r.t.dev.info.ID)})
	idx := len(r.t.view.Steps) - 1
	a.nextID++
	n := &need{
		view: gateway.NeedView{
			ID: fmt.Sprintf("handoff-%d", a.nextID), Kind: gateway.NeedHandoff,
			DeviceID: r.t.dev.info.ID, DeviceName: r.t.dev.info.Name, App: app, Agent: r.t.view.Agent,
			TaskID: r.t.view.ID, Title: reason, Detail: r.t.view.Agent + " · " + r.t.view.Title, Rule: "Handoff",
			Created: now, Deadline: now.Add(15 * time.Minute),
		},
		decision: make(chan decision, 1),
	}
	a.needs = append(a.needs, n)
	r.t.view.State = gateway.TaskWaiting
	a.mu.Unlock()
	a.emitAll(r.t.dev.info.ID)

	dec, open := <-n.decision
	if !open {
		return decision{}, false
	}
	a.mu.Lock()
	defer func() {
		a.mu.Unlock()
		a.emitAll(r.t.dev.info.ID)
	}()
	if !r.alive() {
		return decision{}, false
	}
	if dec.d == gateway.TakeOver {
		return dec, true // HandBack settles the step
	}
	st := &r.t.view.Steps[idx]
	st.Elapsed = a.opt.Now().Sub(now)
	st.Status = "settled"
	if dec.d == gateway.Deny {
		st.Status = "approval_denied"
	}
	if dec.note != "" {
		st.Text += "\nYour note: “" + dec.note + "”"
	}
	r.t.view.State = gateway.TaskWorking
	return dec, true
}

// signIn is the handoff scenario: Dinely wants the person to sign in,
// which the agent leaves to them.
func (a *API) signIn(gen int, d *device) func() {
	t := a.newTask(d, "Reorder last week's lunch", "Reorder lunch")
	return func() {
		r := &run{a: a, gen: gen, t: t}
		if !r.do("Opening Dinely", "Opened Dinely", "phone_launch", `{"app":"Dinely"}`, pixelDinelyIcon, 1500*time.Millisecond) ||
			!r.do("Reading the screen", "Dinely asks to sign in", "phone_observe", `{}`, screen.Rect{}, 1200*time.Millisecond) {
			return
		}
		dec, ok := r.handoff("Sign in to Dinely, please", "Dinely")
		if !ok {
			return
		}
		switch dec.d {
		case gateway.Deny:
			if r.do("Ending the task", agentName+" ended the task", "phone_release", `{"outcome":"denied"}`, screen.Rect{}, 800*time.Millisecond) {
				r.finish(gateway.TaskDone)
			}
			return
		case gateway.TakeOver:
			if !r.sleep(0) { // until the person hands back
				return
			}
		}
		if r.do("Finding last week's order", "Found last week's order", "phone_tap", `{"ref":7,"label":"Orders"}`, pixelOrdersTab, 1800*time.Millisecond) &&
			r.do("Adding 1 chicken rice bowl", "Added 1 chicken rice bowl", "phone_tap", `{"ref":21}`, pixelReorder, 1500*time.Millisecond) &&
			r.do("Checking the cart", "Checked the cart: $12.80", "phone_observe", `{}`, screen.Rect{}, time.Second) {
			r.finish(gateway.TaskDone)
		}
	}
}

// newAgent is the first-connection scenario: Codex asks to use the phones
// and, once allowed, checks the weather on Pixel 9. a.mu must be held.
func (a *API) newAgent(gen int, d *device) func() {
	const name = "Codex"
	a.agents[name] = &agent{name: name, scope: gateway.Scope{Mode: gateway.ScopeEverything}}
	now := a.opt.Now()
	a.nextID++
	n := &need{
		view: gateway.NeedView{
			ID: fmt.Sprintf("agent-%d", a.nextID), Kind: gateway.NeedApproval, Agent: name, Rule: "New agent",
			Title:   "Let " + name + " use your phones?",
			Detail:  name + " wants to connect for “Check tomorrow's weather”. Allowing gives it everything access.",
			Created: now, Deadline: now.Add(a.opt.ApprovalTimeout),
		},
		decision: make(chan decision, 1),
	}
	a.needs = append(a.needs, n)
	return func() {
		timer := time.NewTimer(a.opt.ApprovalTimeout)
		defer timer.Stop()
		var dec decision
		select {
		case d, open := <-n.decision:
			if !open {
				return
			}
			dec = d
		case <-timer.C:
			a.mu.Lock()
			for i, x := range a.needs {
				if x == n {
					a.needs = append(a.needs[:i], a.needs[i+1:]...)
					break
				}
			}
			a.mu.Unlock()
			a.emitAll("")
			return
		}
		a.mu.Lock()
		if a.gen != gen || dec.d == gateway.Deny {
			a.mu.Unlock()
			return
		}
		a.agents[name].approved = true // the scope stays what SetAgentScope set
		t := a.newTaskBy(d, name, "Check tomorrow's weather", "Check the weather")
		a.mu.Unlock()
		a.emitAll(d.info.ID)
		r := &run{a: a, gen: gen, t: t}
		if r.do("Opening Weather", "Opened Weather", "phone_launch", `{"app":"Weather"}`, pixelDinelyIcon, 1500*time.Millisecond) &&
			r.do("Reading tomorrow's forecast", "Tomorrow: 18–24 °C, light rain", "phone_observe", `{}`, screen.Rect{}, 2*time.Second) {
			r.finish(gateway.TaskDone)
		}
	}
}

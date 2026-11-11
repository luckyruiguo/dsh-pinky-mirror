package fakeapi

import (
	"time"

	"github.com/DIYgod/pinky/internal/gateway"
)

// seedHistory returns the prototype's Activity: two tasks today and three
// yesterday.
func seedHistory(now time.Time) []gateway.TaskView {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	yesterday := day.AddDate(0, 0, -1)
	at := func(base time.Time, h, m int) time.Time {
		return base.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
	}
	// Today's tasks happened before the demo started, and today whatever
	// the hour: early in the morning they move closer to now.
	today := func(ago time.Duration) time.Time {
		t := now.Add(-ago)
		if t.Before(day) {
			t = day.Add(now.Sub(day) * time.Duration(max(1, 8-int(ago/(20*time.Minute)))) / 8)
		}
		return t
	}
	summary := func(text string) string { return `{"outcome":"done","summary":"` + text + `"}` }

	agent := func(text, tool, args string, sec float64) gateway.StepView {
		return gateway.StepView{Text: text, Tool: tool, Args: args, Status: "settled", Elapsed: time.Duration(sec * float64(time.Second))}
	}
	you := func(text string, waited time.Duration) gateway.StepView {
		return gateway.StepView{Text: text, Status: "user", Elapsed: waited}
	}
	task := func(id, title, device string, start time.Time, dur time.Duration, state gateway.TaskState, steps ...gateway.StepView) gateway.TaskView {
		t := gateway.TaskView{ID: id, DeviceID: deviceID(device), DeviceName: device, Agent: agentName, Title: title,
			State: state, Started: start, Ended: start.Add(dur)}
		step := dur / time.Duration(len(steps)+1)
		for i, s := range steps {
			s.Index = i
			s.At = start.Add(step * time.Duration(i+1))
			if s.Status == "settled" && s.Tool != "" {
				s.After = mediaID(t.DeviceID)
			}
			t.Steps = append(t.Steps, s)
		}
		return t
	}

	return []gateway.TaskView{
		task("h-lunch", "Reorder last week's lunch", "Pixel 9", today(26*time.Minute), 108*time.Second, gateway.TaskDone,
			agent("Opened Dinely", "phone_launch", `{"app":"Dinely"}`, 1.2),
			agent("Found last week's order", "phone_tap", `{"ref":7}`, 0.8),
			agent("Added 1 chicken rice bowl", "phone_tap", `{"ref":21}`, 0.8),
			agent("Added the note “No cilantro”", "phone_type", `{"ref":14,"text":"No cilantro"}`, 0.5),
			you("You allowed: pay $12.80", 14*time.Second),
			agent("Placed the order", "phone_tap", `{"ref":15}`, 15.2),
			agent("Checked the delivery time\n25–35 min", "phone_release", summary("Reordered last week's chicken rice bowl from Lucky Bowl for $12.80, after you allowed the payment."), 2.1),
		),
		task("h-sam", "Reply to Sam about Saturday", "Pixel 9", today(163*time.Minute), 52*time.Second, gateway.TaskDone,
			agent("Opened Chatter", "phone_launch", `{"app":"Chatter"}`, 1.0),
			agent("Opened the chat with Sam", "phone_tap", `{"ref":9}`, 0.7),
			agent("Wrote “Saturday works, see you at 2pm”", "phone_type", `{"ref":22}`, 1.1),
			you("You denied: send the message\nYour note: “Say 3pm, not 2pm”", 9*time.Second),
			agent("Changed the time to 3pm", "phone_type", `{"ref":22}`, 0.9),
			you("You allowed: send the message", 4*time.Second),
			agent("Message delivered", "phone_release", summary("Told Sam that Saturday at 3pm works. You corrected the time before the message went out."), 0.6),
		),
		task("h-gate", "Check boarding pass gate", "iPhone 17", at(yesterday, 18, 20), 24*time.Second, gateway.TaskDone,
			agent("Read the open boarding pass", "phone_observe", `{}`, 0.9),
			agent("Opened trip details", "phone_tap", `{"ref":5}`, 0.7),
			agent("Found gate B14, boarding 19:05", "phone_release", summary("Found gate B14 and boarding at 19:05 on your boarding pass. Read-only mode was on."), 0.4),
		),
		task("h-table", "Book a table for Friday", "Pixel 9", at(yesterday, 13, 10), 40*time.Second, gateway.TaskStopped,
			agent("Opened Dinely", "phone_launch", `{"app":"Dinely"}`, 1.1),
			agent("Searched for Lucky Bowl", "phone_type", `{"ref":3,"text":"Lucky Bowl"}`, 1.3),
			agent("Opened Friday's free tables", "phone_tap", `{"ref":18}`, 0.9),
			gateway.StepView{Text: "You stopped the task", Status: "user", Args: summary("Started booking a table for Friday. You stopped it before anything was booked.")},
		),
		task("h-gym", "Cancel Thursday spin class", "iPhone 17", at(yesterday, 8, 2), 31*time.Second, gateway.TaskDone,
			agent("Opened Fitloop", "phone_launch", `{"app":"Fitloop"}`, 1.0),
			agent("Found Thursday's spin class", "phone_tap", `{"ref":12}`, 0.8),
			you("You denied: cancel the class", 7*time.Second),
			agent("Left the class booked", "phone_release", summary("Tried to cancel Thursday's spin class. You said no, so the class is still booked."), 0.3),
		),
	}
}

func deviceID(name string) string {
	switch name {
	case "Pixel 9":
		return "pixel-9"
	case "iPhone 17":
		return "iphone-17"
	}
	return name
}

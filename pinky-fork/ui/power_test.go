package ui

import (
	"slices"
	"testing"
)

// TestKeepAwake checks what the client asks MyGo's Power for while agent
// tasks run and while a phone is away: the computer kept from idle sleep
// under each reason, never its display, and each hold ended once. MyGo's
// own TestPower checks with pmset that such a hold keeps a Mac awake.
func TestKeepAwake(t *testing.T) {
	type hold struct {
		reason  string
		display bool
	}
	var held []hold
	released := map[string]int{}
	old := powerKeepAwake
	powerKeepAwake = func(reason string, display bool) func() {
		held = append(held, hold{reason, display})
		return func() { released[reason]++ }
	}
	t.Cleanup(func() { powerKeepAwake = old })

	keepAwake(true)
	keepAwake(true)
	keepAwakeAway(true)
	keepAwake(false)
	keepAwake(false)
	if want := []hold{{awakeReason, false}, {awayReason, false}}; !slices.Equal(held, want) {
		t.Fatalf("held %v, want %v", held, want)
	}
	if released[awakeReason] != 1 || released[awayReason] != 0 {
		t.Fatalf("released %v once the tasks ended, want only the tasks' hold, once", released)
	}
	keepAwakeAway(false)
	keepAwakeAway(false)
	if released[awayReason] != 1 {
		t.Errorf("released %v once the phone came back, want the phone's hold once", released)
	}
}

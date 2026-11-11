package ui

import (
	"testing"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// TestLockedHandoffSaysWhatUnlockingDoes: on a locked phone this computer
// can unlock, a handoff card says that unlocking hands the phone back only
// when unlocking is all the agent asked for; otherwise the person unlocks,
// does the rest and clicks Done (field test #26).
func TestLockedHandoffSaysWhatUnlockingDoes(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.waitFor(t, "the phones and the keychain probe", func() bool { return ta.pin.ready && len(ta.devices) > 0 })
	d := ta.devices[0]
	d.Status, d.Features.Unlock = gateway.DeviceLocked, true
	ta.devices[0] = d
	ta.settings.UnlockFromComputer = true
	ta.pin.saved[d.Info.ID] = true
	ta.unlocks = map[string]bool{d.Info.ID: true}
	if !ta.canUnlockHere(d.Info.ID) {
		t.Fatal("the phone cannot be unlocked from here")
	}
	n := gateway.NeedView{ID: "handoff-1", Kind: gateway.NeedHandoff, DeviceID: d.Info.ID, Agent: "Claude Code",
		Title: "Unlock the phone, then unblock Wise in Settings"}
	for _, c := range []struct {
		ends      bool
		want, not string
	}{
		{false, "The phone is locked. Unlock it here or on the phone, do what Claude Code asked, then click Done.", "hands it back"},
		{true, "The phone is locked. Unlocking it here hands it back to Claude Code.", "do what Claude Code asked"},
	} {
		n.EndsOnUnlock = c.ends
		tt := mg.NewTester(func(ctx *mg.Context) { ta.handoffControls(ctx, n) }, 420, 400)
		tt.Frame()
		if !hasText(tt, c.want) || hasText(tt, c.not) {
			t.Errorf("EndsOnUnlock %v: the card reads %q", c.ends, tt.Texts())
		}
	}
}

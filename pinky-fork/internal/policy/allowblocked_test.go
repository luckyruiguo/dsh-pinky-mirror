package policy

import (
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

// AllowBlocked lifts the rules that block a whole app, for an app the
// person allowed for a task, and nothing else: payments in it still ask,
// password managers stay blocked, and read-only mode still holds.
func TestAllowBlocked(t *testing.T) {
	e := Default()
	e.AddApps(CatPasswords, CategoryEntries{IDs: []string{"com.example.vault"}})
	wise := screen.App{ID: "com.transferwise.Transferwise", Name: "Wise"}
	if _, blocked := e.Blocked(wise); !blocked {
		t.Fatal("Wise is not blocked by default")
	}
	launch := Input{Op: OpLaunch, Launch: wise}
	if v := e.Evaluate(launch); v.Decision != Deny {
		t.Fatalf("launch without AllowBlocked: %+v", v)
	}
	launch.AllowBlocked = true
	if v := e.Evaluate(launch); v.Decision != Allow {
		t.Fatalf("launch with AllowBlocked: %+v", v)
	}

	balance := &screen.Element{Role: screen.RoleButton, Label: "Balances"}
	tap := Input{Op: OpTap, App: wise, Target: balance, AllowBlocked: true}
	if v := e.Evaluate(tap); v.Decision != Allow {
		t.Fatalf("a plain tap in an allowed app: %+v", v)
	}
	pay := &screen.Element{Role: screen.RoleButton, Label: "Send S$500.00"}
	tap.Target = pay
	if v := e.Evaluate(tap); v.Decision != Ask || !v.Has(Payment) {
		t.Fatalf("a payment in an allowed app: %+v", v)
	}
	tap.ReadOnly = true
	if v := e.Evaluate(tap); v.Decision != Deny || v.Rule != RuleReadOnly {
		t.Fatalf("a payment in an allowed app, read-only: %+v", v)
	}

	vault := screen.App{ID: "com.example.vault", Name: "Vault"}
	if v := e.Evaluate(Input{Op: OpLaunch, Launch: vault, AllowBlocked: true}); v.Decision != Deny {
		t.Fatalf("a password manager with AllowBlocked: %+v", v)
	}
	// A rule about an action, not a whole app, still denies.
	f := DefaultFile()
	f.Rules = append(f.Rules, Rule{Name: "No closing accounts", Match: Match{Label: StringList{"close account"}}, Decision: Deny})
	e2 := New(f, nil)
	closeIt := &screen.Element{Role: screen.RoleButton, Label: "Close account"}
	if v := e2.Evaluate(Input{Op: OpTap, App: wise, Target: closeIt, AllowBlocked: true}); v.Decision != Deny || v.Rule != "No closing accounts" {
		t.Fatalf("a label rule in an allowed app: %+v", v)
	}
}

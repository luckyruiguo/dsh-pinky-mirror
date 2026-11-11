package policy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

// TestFieldTestFinanceApps: the money apps the field test found open to
// agents are blocked by default (field test bug 2): by id, by name on a
// read without an id, and by a name that says bank, wallet, securities,
// remittance or pay when the table does not know the app.
func TestFieldTestFinanceApps(t *testing.T) {
	e := Default()
	finance := []screen.App{
		{ID: "com.dbs.sg.dbsmbanking", Name: "digibank"},
		{ID: "com.dbs.dbspaylah", Name: "PayLah!"},
		{ID: "sg.trust", Name: "Trust"},
		{ID: "com.okex.OKExAppstoreFull", Name: "OKX"},
		{ID: "fi.ether.cashapp", Name: "ether.fi"},
		{ID: "app.ethenapay.co", Name: "Ethena"},
		{ID: "to.plasma.one.application", Name: "Plasma One"},
		{ID: "ag.jup.jupiter.ios", Name: "Jupiter"},
		{ID: "com.panda.exchangerate", Name: "Panda Remit"},
		{Name: "OKX"}, {Name: "DBS digibank"}, {Name: "Plasma One"},
		{ID: "com.example.mybank", Name: "Example Bank"}, {Name: "某某银行"}, {Name: "Example Wallet"},
		{Name: "Example Pay"}, {Name: "Example Securities"}, {Name: "某某证券"}, {Name: "QuickRemit"},
	}
	for _, a := range finance {
		if rule, ok := e.Blocked(a); !ok || rule != "Finance apps" {
			t.Errorf("%+v: blocked %v by %q", a, ok, rule)
		}
	}
	if rule, ok := e.Blocked(screen.App{ID: "sg.ndi.sp", Name: "Singpass"}); !ok || rule != "Password managers" {
		t.Errorf("Singpass: blocked %v by %q", ok, rule)
	}
	for _, a := range []screen.App{dinely, whatsapp, {Name: "Calculator"}, {Name: "Wallpapers"}, {Name: "Paydirt"},
		{Name: "Submit It"}, {Name: "Jupiter"}, {Name: "Trust"}} {
		if rule, ok := e.Blocked(a); ok {
			t.Errorf("%+v is blocked by %q", a, rule)
		}
	}
	// Settings lists them among the apps on the phones, as finance.
	got := e.BlockedApps([]screen.App{{ID: "com.example.mybank", Name: "Example Bank"}})
	found := false
	for _, b := range got {
		found = found || (b.ID == "com.example.mybank" && b.Category == CatFinance)
	}
	if !found {
		t.Fatalf("Settings misses the bank: %+v", got)
	}
}

// TestUnblockOneAppOfAPrefix: unblocking an app that a prefix entry blocks
// unblocks that app alone (field test bug 11), and lasts.
func TestUnblockOneAppOfAPrefix(t *testing.T) {
	dir := t.TempDir()
	rules, apps := filepath.Join(dir, "rules.yaml"), filepath.Join(dir, "apps.yaml")
	e, err := Load(rules, apps)
	if err != nil {
		t.Fatal(err)
	}
	pluto := screen.App{ID: "com.cmbchina.pluto", Name: "掌上生活"}
	other := screen.App{ID: "com.cmbchina.other", Name: "Other CMB app"}
	bank := screen.App{ID: "com.example.mybank", Name: "Example Bank"}
	for _, a := range []screen.App{pluto, other, bank} {
		if _, ok := e.Blocked(a); !ok {
			t.Fatalf("%+v is not blocked to begin with", a)
		}
	}
	for _, a := range []screen.App{pluto, bank} {
		if err := e.SetAppBlocked(a, false); err != nil {
			t.Fatalf("unblocking %+v: %v", a, err)
		}
	}
	check := func(e *Engine, when string) {
		t.Helper()
		if _, ok := e.Blocked(pluto); ok {
			t.Errorf("%s: %+v still blocked", when, pluto)
		}
		if _, ok := e.Blocked(bank); ok {
			t.Errorf("%s: %+v still blocked", when, bank)
		}
		if rule, ok := e.Blocked(other); !ok || rule != "Finance apps" {
			t.Errorf("%s: the rest of the prefix: blocked %v by %q", when, ok, rule)
		}
	}
	check(e, "after unblocking")
	e, err = Load(rules, apps)
	if err != nil {
		t.Fatal(err)
	}
	check(e, "after a restart")

	// Blocked again, it is.
	if err := e.SetAppBlocked(pluto, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Blocked(pluto); !ok {
		t.Fatal("blocking it again did not")
	}
}

// Chinese banks' credit card apps, whose names say no 银行, are blocked by
// default: by their Android packages, their iOS bundle ids and their names.
func TestChineseCardApps(t *testing.T) {
	e := Default()
	apps := []screen.App{
		{ID: "com.bankcomm.maidanba"}, {ID: "com.cs_credit_bank"}, {ID: "com.cgb.creditcard.iphone"},
		{ID: "com.citiccard.mobilebank"}, {ID: "com.citicbank.creditspace"},
		{ID: "com.cmbchina.ccd.pluto.cmbActivity"}, {ID: "com.cmbchina.cmblife"},
		{ID: "com.spdbccc.app"}, {ID: "spdbccc.com.cn"}, {ID: "com.cmbc.cc.mbank"}, {ID: "com.cmbc.mobilePhone"},
		{ID: "com.ebank.creditcard"}, {ID: "com.cebbank.xyk"}, {ID: "com.cib.xyk"}, {ID: "com.HuaXiaBank.HuaCard"},
		{ID: "com.ccb.longjiLife"}, {ID: "com.icbc.elife"}, {ID: "com.yitong.mbank.psbc.creditcard"}, {ID: "com.psbc.creditcard"},
		{ID: "cn.com.spdb.mobilebank.per"}, {ID: "com.ccb.ccbDemo"}, {ID: "com.boc.BOCMBCI"},
		{Name: "买单吧"}, {Name: "发现精彩"}, {Name: "动卡空间"}, {Name: "掌上生活"}, {Name: "浦大喜奔"}, {Name: "全民生活"},
		{Name: "阳光惠生活"}, {Name: "兴业生活"}, {Name: "华彩生活"}, {Name: "建行生活"}, {Name: "工银e生活"},
		{Name: "邮储信用卡"}, {Name: "广银信用卡"},
	}
	for _, a := range apps {
		if rule, ok := e.Blocked(a); !ok || rule != "Finance apps" {
			t.Errorf("%+v: blocked %v by %q", a, ok, rule)
		}
	}
	for _, a := range []screen.App{{Name: "生活"}, {Name: "美团"}, {ID: "com.cs_credit_bank.other"}} {
		if rule, ok := e.Blocked(a); ok {
			t.Errorf("%+v is blocked by %q", a, rule)
		}
	}
}

// Two apps share a name: Trust Bank's iPhone app (sg.trust) and Trust
// Wallet's Android app (com.sixdays.trust). Unblocking one by its id
// neither unblocks the other nor hides it from Settings, and what Settings
// lists is what is blocked.
func TestUnblockOneOfTwoSameNamedApps(t *testing.T) {
	bank := screen.App{ID: "sg.trust", Name: "Trust"}
	wallet := screen.App{ID: "com.sixdays.trust", Name: "Trust"}
	installed := []screen.App{bank, wallet}
	listed := func(e *Engine) map[string]bool {
		out := map[string]bool{}
		for _, b := range e.BlockedApps(installed) {
			out[b.ID+"|"+b.Name] = true
		}
		return out
	}
	agree := func(t *testing.T, e *Engine) {
		t.Helper()
		got := listed(e)
		for _, a := range installed {
			_, blocked := e.Blocked(a)
			if got[a.ID+"|"+a.Name] != blocked {
				t.Errorf("%s: blocked %v, listed %v (list %v)", a.ID, blocked, got[a.ID+"|"+a.Name], got)
			}
		}
	}

	t.Run("bundled table", func(t *testing.T) {
		dir := t.TempDir()
		rules, apps := filepath.Join(dir, "rules.yaml"), filepath.Join(dir, "apps.yaml")
		e, err := Load(rules, apps)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range installed {
			if _, ok := e.Blocked(a); !ok {
				t.Fatalf("%+v is not blocked to begin with", a)
			}
		}
		if err := e.SetAppBlocked(wallet, false); err != nil {
			t.Fatal(err)
		}
		if _, ok := e.Blocked(wallet); ok {
			t.Error("the wallet is still blocked")
		}
		if _, ok := e.Blocked(bank); !ok {
			t.Error("unblocking the wallet unblocked the bank")
		}
		agree(t, e)
		if e, err = Load(rules, apps); err != nil {
			t.Fatal(err)
		}
		agree(t, e)
		if !listed(e)["sg.trust|Trust"] {
			t.Error("the bank is missing from the list after a restart")
		}
		if err := e.SetAppBlocked(bank, false); err != nil {
			t.Fatal(err)
		}
		for _, a := range installed {
			if _, ok := e.Blocked(a); ok {
				t.Errorf("%s still blocked after unblocking both", a.ID)
			}
		}
		agree(t, e)
	})

	// The owner's apps.yaml after the old Settings unblocked the wallet:
	// it excluded the wallet's id and the name "Trust".
	t.Run("old exclude of the name", func(t *testing.T) {
		dir := t.TempDir()
		rules, apps := filepath.Join(dir, "rules.yaml"), filepath.Join(dir, "apps.yaml")
		if err := os.WriteFile(apps, []byte("exclude:\n  - com.sixdays.trust\n  - Trust\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		e, err := Load(rules, apps)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := e.Blocked(bank); !ok {
			t.Fatal("the bank is not blocked")
		}
		if !listed(e)["sg.trust|Trust"] {
			t.Fatalf("the bank is blocked but not listed: %v", listed(e))
		}
		agree(t, e)
	})

	// The person typed the name, which Settings lists on the row of each
	// app of it: removing the wallet's row removes that name, and the
	// bank stays blocked by the table.
	t.Run("blocked by name", func(t *testing.T) {
		dir := t.TempDir()
		e, err := Load(filepath.Join(dir, "rules.yaml"), filepath.Join(dir, "apps.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if err := e.SetAppBlocked(screen.App{Name: "Trust"}, true); err != nil {
			t.Fatal(err)
		}
		if _, ok := e.Blocked(screen.App{ID: "com.other.trust", Name: "Trust"}); !ok {
			t.Fatal("the name is not blocked")
		}
		if err := e.SetAppBlocked(wallet, false); err != nil {
			t.Fatal(err)
		}
		if _, ok := e.Blocked(wallet); ok {
			t.Error("the wallet is still blocked")
		}
		if _, ok := e.Blocked(bank); !ok {
			t.Error("unblocking the wallet unblocked the bank")
		}
		if got := listed(e); len(got) != 1 || !got["sg.trust|Trust"] {
			t.Errorf("listed %v", got)
		}
		agree(t, e)
	})
}

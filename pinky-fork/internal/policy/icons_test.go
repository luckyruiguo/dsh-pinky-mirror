package policy

import (
	"reflect"
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

func TestIdWords(t *testing.T) {
	cases := map[string][]string{
		"btn_send":             {"btn", "send"},
		"btnSendMessage":       {"btn", "send", "message"},
		"IBSendButton":         {"ib", "send", "button"},
		"fab_pay2":             {"fab", "pay", "2"},
		"trash.fill":           {"trash", "fill"},
		"Checkout.PayButton":   {"checkout", "pay", "button"},
		"send_message_btn_ico": {"send", "message", "btn", "ico"},
		"":                     nil,
	}
	for in, want := range cases {
		if got := idWords(in); !reflect.DeepEqual(got, want) {
			t.Errorf("idWords(%q) = %q, want %q", in, got, want)
		}
	}
}

// screenCase is a screen as a driver reports it, and the rule that should
// ask (or deny) when the agent taps each element; "" is a plain tap.
type screenCase struct {
	app  screen.App
	tree []screen.Element
	want map[string]string // element id or label → the rule that decides
}

func el(role, label, id string) screen.Element {
	return screen.Element{Role: role, Label: label, ID: id, Bounds: screen.Rect{W: 120, H: 120}}
}

// TestIconButtons taps every element of realistic screens: only the icon
// buttons that commit ask, and every other tap stays plain.
func TestIconButtons(t *testing.T) {
	chat := screen.App{Name: "Example Chat", ID: "com.example.chat"}
	shop := screen.App{Name: "Example Shop", ID: "com.example.shop"}
	notes := screen.App{Name: "Notes", ID: "com.apple.mobilenotes"}
	cases := map[string]screenCase{
		// uiautomator: a node without text or description gets the last part
		// of its resource id as its label.
		"android chat": {app: chat, tree: []screen.Element{
			el(screen.RoleButton, "Navigate up", ""),
			el(screen.RoleText, "Sam", "com.example.chat:id/toolbar_title"),
			el(screen.RoleButton, "iv_avatar", "com.example.chat:id/iv_avatar"),
			el(screen.RoleButton, "btn_voice_call", "com.example.chat:id/btn_voice_call"),
			el(screen.RoleButton, "menu_overflow", "com.example.chat:id/menu_overflow"),
			el(screen.RoleText, "9:41", "com.example.chat:id/tv_send_time"),
			el(screen.RoleImage, "Sent", "com.example.chat:id/iv_sent_status"),
			el(screen.RoleButton, "btn_emoji", "com.example.chat:id/btn_emoji"),
			el(screen.RoleButton, "iv_attach", "com.example.chat:id/iv_attach"),
			el(screen.RoleButton, "btn_send", "com.example.chat:id/btn_send"),
			el(screen.RoleButton, "ivDelete", "com.example.chat:id/ivDelete"),
		}, want: map[string]string{
			"com.example.chat:id/btn_voice_call": "Calls",
			"com.example.chat:id/btn_send":       "Commits",
			"com.example.chat:id/ivDelete":       "Commits",
		}},
		// The companion app: the content description first, else the text;
		// an unlabeled target keeps an empty label and its resource id.
		"android cart": {app: shop, tree: []screen.Element{
			el(screen.RoleButton, "Back", "com.example.shop:id/back"),
			el(screen.RoleText, "Cart", ""),
			el(screen.RoleText, "Total $12.80", "com.example.shop:id/tv_total"),
			el(screen.RoleButton, "Order status", "com.example.shop:id/order_status"),
			el(screen.RoleButton, "", "com.example.shop:id/payment_methods_row"),
			el(screen.RoleButton, "", "com.example.shop:id/btn_add_quantity"),
			el(screen.RoleButton, "", "com.example.shop:id/fab_favorite"),
			el(screen.RoleButton, "", "com.example.shop:id/post_image"),
			el(screen.RoleButton, "", "com.example.shop:id/buy_again_carousel"),
			el(screen.RoleButton, "", "com.example.shop:id/iv_delete_item"),
			el(screen.RoleButton, "", "com.example.shop:id/btn_place_order"),
			el(screen.RoleButton, "", "com.example.shop:id/btn_pay"),
		}, want: map[string]string{
			// On a screen with an amount, a commit is a payment.
			"com.example.shop:id/iv_delete_item":  "Payments",
			"com.example.shop:id/btn_place_order": "Payments",
			"com.example.shop:id/btn_pay":         "Payments",
		}},
		// iOS: the accessibility label, else the identifier; image buttons
		// may carry the SF Symbol's name.
		"iphone": {app: notes, tree: []screen.Element{
			el(screen.RoleButton, "chevron.left", ""),
			el(screen.RoleButton, "Edit", "editButton"),
			el(screen.RoleButton, "composeButton", "composeButton"),
			el(screen.RoleButton, "square.and.arrow.up", ""),
			el(screen.RoleButton, "ellipsis.circle", ""),
			el(screen.RoleButton, "magnifyingglass", ""),
			el(screen.RoleButton, "Bin", ""), // a delete button on a British iPhone, or a mailbox: it asks
			el(screen.RoleButton, "trash", ""),
			el(screen.RoleButton, "paperplane.fill", ""),
			el(screen.RoleButton, "sendButton", "sendButton"),
			el(screen.RoleButton, "➤", "SubmitReply"),
		}, want: map[string]string{
			"Bin":             "Commits",
			"trash":           "Commits",
			"paperplane.fill": "Commits",
			"sendButton":      "Commits",
			"SubmitReply":     "Commits",
		}},
	}
	e := commitsEngine(t)
	for name, sc := range cases {
		s := &screen.Screen{App: sc.app, Elements: sc.tree}
		for i := range sc.tree {
			target := &sc.tree[i]
			key := target.ID
			if key == "" {
				key = target.Label
			}
			v := e.Evaluate(Input{Op: OpTap, App: sc.app, Target: target, Screen: s})
			want := sc.want[key]
			switch {
			case want == "" && v.Decision != Allow:
				t.Errorf("%s: a tap on %q (%s) asks: %+v", name, target.Label, target.ID, v)
			case want != "" && (v.Decision == Allow || v.Rule != want):
				t.Errorf("%s: a tap on %q (%s): %v %q, want %q", name, target.Label, target.ID, v.Decision, v.Rule, want)
			}
		}
	}
}

// TestIconButtonsKeepDenies: what an icon adds only asks; denies, read-only
// mode and a task's apps decide as before.
func TestIconButtonsKeepDenies(t *testing.T) {
	e := commitsEngine(t)
	pay := el(screen.RoleButton, "", "com.chase.sig.android:id/btn_pay")
	if v := e.Evaluate(Input{Op: OpTap, App: chase, Target: &pay}); v.Decision != Deny || v.Rule != "Finance apps" {
		t.Fatalf("pay icon in a finance app: %+v", v)
	}
	send := el(screen.RoleButton, "btn_send", "com.example.chat:id/btn_send")
	chat := screen.App{Name: "Example Chat", ID: "com.example.chat"}
	if v := e.Evaluate(Input{Op: OpTap, App: chat, Target: &send, ReadOnly: true}); v.Decision != Deny || v.Rule != RuleReadOnly {
		t.Fatalf("send icon by a read-only agent: %+v", v)
	}
	if err := e.SetPreset(PresetEvery); err != nil {
		t.Fatal(err)
	}
	if v := e.Evaluate(Input{Op: OpTap, App: chat, Target: &send}); v.Decision != Ask || v.Rule != "Commits" {
		t.Fatalf("send icon while every action asks: %+v", v)
	}
	// The icon's words never reach a field: typing into a password still
	// is refused, whatever the field is called.
	pw := screen.Element{Role: screen.RoleInput, ID: "com.example.chat:id/send_password", States: []string{screen.StatePassword}}
	if v := e.Evaluate(Input{Op: OpType, App: chat, Target: &pw, Text: "x"}); v.Decision != Deny || v.Rule != RuleCredentials {
		t.Fatalf("password field: %+v", v)
	}
}

func TestIconSliders(t *testing.T) {
	e := commitsEngine(t)
	shop := screen.App{Name: "Example Shop", ID: "com.example.shop"}
	slider := el(screen.RoleOther, "", "com.example.shop:id/slide_to_pay")
	if v := e.Evaluate(Input{Op: OpSwipe, App: shop, Target: &slider}); v.Decision != Ask || v.Rule != "Payments" {
		t.Fatalf("slide to pay: %+v", v)
	}
	list := el(screen.RoleList, "", "com.example.shop:id/pay_later_offers")
	list.States = []string{screen.StateScrollable}
	if v := e.Evaluate(Input{Op: OpSwipe, App: shop, Target: &list}); v.Decision != Allow {
		t.Fatalf("scrolling a list: %+v", v)
	}
}

func TestInstallIcons(t *testing.T) {
	e := commitsEngine(t)
	get := el(screen.RoleButton, "", "com.android.vending:id/download_button")
	if v := e.Evaluate(Input{Op: OpTap, App: play, Target: &get}); v.Decision != Ask || v.Rule != "Installs and links" {
		t.Fatalf("download icon in Play Store: %+v", v)
	}
	shop := screen.App{Name: "Example Shop", ID: "com.example.shop"}
	if v := e.Evaluate(Input{Op: OpTap, App: shop, Target: &get}); v.Decision != Allow {
		t.Fatalf("download icon outside a store: %+v", v)
	}
}

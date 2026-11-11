package ui

import (
	"image"
	"testing"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/internal/screen"
)

// fakeCheckout draws a food app's checkout page as a phone's screenshot
// shows it: w × h points at scale pixels a point, in Chinese when zh is
// set, two columns when it is wider than tall. It returns the picture and
// the Place order button's bounds in its pixels, for the approval card's
// tests and pictures: nothing here comes from a real phone.
func fakeCheckout(t *testing.T, w, h int, scale float32, zh bool) (image.Image, screen.Rect) {
	t.Helper()
	words := map[string]string{
		"back": "Cart", "title": "Checkout", "shop": "Golden Wok", "eta": "Delivery · 20–30 min",
		"order": "YOUR ORDER", "item": "Chicken rice bowl", "note": "No cilantro", "price": "$11.50",
		"sub": "Subtotal", "fee": "Delivery", "feeP": "$1.30", "total": "Total", "totalP": "$12.80",
		"pay": "PAY WITH", "card": "Visa •••• 4242", "to": "DELIVER TO", "addr": "88 Market St, Apt 5",
		"button": "Place order · $12.80",
	}
	if zh {
		words = map[string]string{
			"back": "购物车", "title": "确认订单", "shop": "金碗小厨", "eta": "配送 · 20–30 分钟",
			"order": "你的订单", "item": "鸡肉饭", "note": "不要香菜", "price": "¥11.50",
			"sub": "小计", "fee": "配送费", "feeP": "¥1.30", "total": "合计", "totalP": "¥12.80",
			"pay": "支付方式", "card": "招商银行储蓄卡 (4242)", "to": "送至", "addr": "人民路 88 号 5 单元",
			"button": "提交订单 · ¥12.80",
		}
	}
	orange, ink, ink2, line := hex("#ff6a13"), hex("#1c1c1e"), hex("#8a8a8e"), hex("#e6e6ea")
	section := func(c *mg.Context, title string, body func()) {
		mg.Column(c).Gap(8).Children(func() {
			mg.Text(c, title).FontSize(13).FontWeight(600).TextColor(ink2)
			mg.Column(c).Padding(14, 16).Radius(14).Background(hex("#ffffff")).Gap(10).Children(body)
		})
	}
	row := func(c *mg.Context, left, right string, bold bool) {
		mg.Row(c).AlignItems(mg.Center).Children(func() {
			l := mg.Text(c, left).FontSize(16).TextColor(ink)
			mg.Spacer(c)
			r := mg.Text(c, right).FontSize(16).TextColor(ink)
			if bold {
				l.FontWeight(700)
				r.FontWeight(700)
			}
		})
	}
	order := func(c *mg.Context) {
		mg.Row(c).Gap(12).AlignItems(mg.Center).Padding(14, 16).Radius(14).Background(hex("#ffffff")).Children(func() {
			mg.Box(c).Size(48, 48).Radius(12).Background(orange).Center().Children(func() {
				mg.Text(c, string([]rune(words["shop"])[:1])).FontSize(22).FontWeight(800).TextColor(hex("#ffffff"))
			})
			mg.Column(c).Gap(2).Children(func() {
				mg.Text(c, words["shop"]).FontSize(17).FontWeight(650).TextColor(ink)
				mg.Text(c, words["eta"]).FontSize(14).TextColor(ink2)
			})
		})
		section(c, words["order"], func() {
			mg.Row(c).Gap(10).AlignItems(mg.Start).Children(func() {
				mg.Text(c, "1×").FontSize(16).FontWeight(600).TextColor(orange)
				mg.Column(c).Grow(1).Gap(2).Children(func() {
					mg.Text(c, words["item"]).FontSize(16).TextColor(ink)
					mg.Text(c, words["note"]).FontSize(14).TextColor(ink2)
				})
				mg.Text(c, words["price"]).FontSize(16).TextColor(ink)
			})
			mg.Box(c).Height(1).FillWidth().Background(line)
			row(c, words["sub"], words["price"], false)
			row(c, words["fee"], words["feeP"], false)
			row(c, words["total"], words["totalP"], true)
		})
	}
	pay := func(c *mg.Context) {
		section(c, words["pay"], func() {
			mg.Row(c).Gap(10).AlignItems(mg.Center).Children(func() {
				mg.Box(c).Size(34, 22).Radius(4).Background(hex("#1a1f71"))
				mg.Text(c, words["card"]).FontSize(16).TextColor(ink).Grow(1)
				mg.Icon(c, icChevR).FontSize(16).TextColor(ink2)
			})
		})
		section(c, words["to"], func() {
			mg.Row(c).Gap(10).AlignItems(mg.Center).Children(func() {
				mg.Text(c, words["addr"]).FontSize(16).TextColor(ink).Grow(1)
				mg.Icon(c, icChevR).FontSize(16).TextColor(ink2)
			})
		})
	}
	place := func(c *mg.Context) {
		mg.Box(c).Height(54).FillWidth().Radius(27).Background(orange).Center().Label("place-order").Children(func() {
			mg.Text(c, words["button"]).FontSize(17).FontWeight(650).TextColor(hex("#ffffff"))
		})
	}
	view := func(c *mg.Context) {
		wide := w > h
		mg.Column(c).Fill().Background(hex("#f2f2f7")).Children(func() {
			// The status bar, the navigation bar.
			mg.Row(c).Height(48).Padding(0, 28).AlignItems(mg.Center).Children(func() {
				mg.Text(c, "9:41").FontSize(16).FontWeight(650).TextColor(ink)
				mg.Spacer(c)
				mg.Box(c).Size(26, 12).Radius(3.5).Border(1, ink2).Padding(2).Children(func() {
					mg.Box(c).Size(17, 6).Radius(1.5).Background(ink)
				})
			})
			mg.Row(c).Height(44).Padding(0, 12).AlignItems(mg.Center).Children(func() {
				mg.Row(c).Gap(2).AlignItems(mg.Center).Width(110).Children(func() {
					mg.Icon(c, icChevL).FontSize(22).TextColor(orange)
					mg.Text(c, words["back"]).FontSize(17).TextColor(orange)
				})
				mg.Text(c, words["title"]).FontSize(17).FontWeight(650).TextColor(ink).Grow(1).TextAlign(mg.Center)
				mg.Box(c).Width(110)
			})
			if wide {
				mg.Row(c).Grow(1).Padding(8, 28, 24, 28).Gap(24).AlignItems(mg.Start).Children(func() {
					mg.Column(c).Grow(1).Basis(0).Gap(18).Children(func() { order(c) })
					mg.Column(c).Grow(1).Basis(0).Gap(18).Children(func() {
						pay(c)
						place(c)
					})
				})
				return
			}
			mg.Column(c).Grow(1).Padding(8, 18, 0, 18).Gap(18).Children(func() {
				order(c)
				pay(c)
			})
			mg.Column(c).Padding(12, 18, 34, 18).Background(hex("#ffffff")).BorderWidth(1, 0, 0, 0).BorderColor(line).Children(func() {
				place(c)
			})
		})
	}
	tt := mg.NewTester(view, w, h)
	tt.SetScale(scale)
	tt.Frame()
	r, ok := tt.Find("place-order")
	if !ok {
		t.Fatal("no Place order button on the fake checkout")
	}
	return tt.Image(), screen.Rect{X: int(r.X * scale), Y: int(r.Y * scale), W: int(r.W * scale), H: int(r.H * scale)}
}

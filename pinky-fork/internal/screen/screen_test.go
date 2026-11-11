package screen

import (
	"strings"
	"testing"
)

func cart() *Screen {
	return &Screen{
		Width: 1080, Height: 2400,
		App:         App{Name: "Dinely", ID: "com.dinely.app"},
		Orientation: "portrait",
		Events:      []Event{{Kind: EventToast, Text: "Added to cart"}},
		Elements: []Element{
			{Role: RoleButton, Label: "Back", Bounds: Rect{0, 48, 80, 80}, Path: "toolbar"},
			{Role: RoleList, Label: "Cart", Bounds: Rect{0, 200, 1080, 1200}, States: []string{StateScrollable}, Path: "root"},
			{Role: RoleInput, Label: "Note for the restaurant", Bounds: Rect{40, 600, 1000, 80}, States: []string{StateFocused}, Path: "root/list"},
			{Role: RoleButton, Label: "Place order · $12.80", Bounds: Rect{40, 2200, 1000, 120}, Path: "footer"},
			{Role: "", Label: "Delivery in 25–35 min", Bounds: Rect{100, 280, 400, 40}, Source: SourceOCR},
		},
	}
}

func TestRenderAndRefs(t *testing.T) {
	s := cart()
	NewRefs().Assign(s)
	got := s.Render()
	want := strings.Join([]string{
		"screen 1080x2400 · app: Dinely (com.dinely.app) · portrait · keyboard hidden · focus [3]",
		`toast: "Added to cart"`,
		`[1] button "Back" @40,88`,
		`[2] list "Cart" @540,800 scrollable`,
		`[3] input "Note for the restaurant" @540,640 focused`,
		`[4] button "Place order · $12.80" @540,2260`,
		`ocr "Delivery in 25–35 min" @300,300`,
	}, "\n")
	if got != want {
		t.Fatalf("render mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRefsStableAndDuplicates(t *testing.T) {
	refs := NewRefs()
	a := &Screen{Elements: []Element{
		{Role: RoleButton, Label: "Add", Path: "list/row"},
		{Role: RoleButton, Label: "Add", Path: "list/row"},
	}}
	refs.Assign(a)
	if a.Elements[0].Ref == a.Elements[1].Ref {
		t.Fatalf("identical siblings share a ref: %d", a.Elements[0].Ref)
	}
	b := &Screen{Elements: []Element{
		{Role: RoleText, Label: "Banner", Path: "top"},
		{Role: RoleButton, Label: "Add", Path: "list/row"},
		{Role: RoleButton, Label: "Add", Path: "list/row"},
	}}
	refs.Assign(b)
	if b.Elements[1].Ref != a.Elements[0].Ref || b.Elements[2].Ref != a.Elements[1].Ref {
		t.Fatalf("refs moved: %v then %v", a.Elements, b.Elements)
	}
}

func TestDiff(t *testing.T) {
	refs := NewRefs()
	prev := cart()
	refs.Assign(prev)
	next := cart()
	next.Events = nil
	next.Elements = append(next.Elements[:3:3], next.Elements[3:]...)
	next.Elements[2].States = nil // input lost focus
	next.Elements = append(next.Elements, Element{Role: RoleText, Label: "Order placed", Bounds: Rect{0, 0, 10, 10}, Path: "root"})
	refs.Assign(next)
	lines, full := Diff(prev, next)
	if full {
		t.Fatal("unexpected full screen")
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{`~ [3] input "Note for the restaurant" @540,640`, `+ [5] text "Order placed" @5,5`} {
		if !strings.Contains(joined, want) {
			t.Errorf("diff misses %q in:\n%s", want, joined)
		}
	}

	other := cart()
	other.App = App{Name: "Chatter", ID: "com.chatter.app"}
	refs.Assign(other)
	if _, full := Diff(prev, other); !full {
		t.Fatal("app change should send the full screen")
	}
}

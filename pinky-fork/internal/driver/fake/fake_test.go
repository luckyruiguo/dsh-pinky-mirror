package fake

import (
	"context"
	"errors"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
)

func TestLinkApps(t *testing.T) {
	ctx := context.Background()
	d := NewDevice("f1", "Fake")
	chrome := driver.App{ID: "com.android.chrome", Name: "Chrome"}
	shop := driver.App{ID: "com.example.shop", Name: "Shop"}
	other := driver.App{ID: "com.example.other", Name: "Other"}
	d.SetLinkApps("https://", chrome)
	d.SetLinkApps("https://shop.example.com/", shop)
	d.SetLinkApps("both://", shop, other)

	for link, want := range map[string]string{
		"https://example.com":             chrome.ID,
		"HTTPS://shop.example.com/item/1": shop.ID,
	} {
		got, err := d.LinkApp(ctx, link)
		if err != nil || got.ID != want {
			t.Errorf("LinkApp(%q) = %v, %v; want %s", link, got, err, want)
		}
	}
	if _, err := d.LinkApp(ctx, "nothing://x"); !errors.Is(err, driver.ErrNotFound) {
		t.Errorf("no handler: %v", err)
	}
	var choice *driver.LinkChoiceError
	if _, err := d.LinkApp(ctx, "both://x"); !errors.As(err, &choice) || len(choice.Apps) != 2 {
		t.Errorf("two handlers: %v", err)
	}

	if err := d.OpenLink(ctx, "https://example.com", chrome); err != nil {
		t.Fatal(err)
	}
	a := d.Actions()
	if len(a) != 1 || a[0].Kind != "link" || a[0].Text != "https://example.com" || a[0].App != chrome.ID {
		t.Errorf("actions = %+v", a)
	}
}

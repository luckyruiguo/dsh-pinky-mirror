package ios

import (
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
)

// The home screen's layout, as SpringBoard sends it (format 2): the dock
// first, then pages; folders hold pages of their own; widgets have no
// bundle. Apps whose home screen name differs get it as an alias (GitHub
// issue #7: WeChat is 微信 on a Chinese iPhone).
func TestHomeScreenNames(t *testing.T) {
	layout := []any{
		[]any{ // the dock
			map[string]any{"bundleIdentifier": "com.apple.mobilephone", "displayName": "电话"},
		},
		[]any{ // a page
			map[string]any{"bundleIdentifier": "com.tencent.xin", "displayName": "微信"},
			map[string]any{"iconType": "custom", "elementType": "widget", "displayName": "Clock"},
			map[string]any{"displayName": "社交", "listType": "folder", "iconLists": []any{
				[]any{map[string]any{"bundleIdentifier": "com.tencent.weread", "displayName": "微信读书"}},
			}},
		},
	}
	names := map[string]string{}
	collectIconNames(layout, names, 0)
	want := map[string]string{"com.apple.mobilephone": "电话", "com.tencent.xin": "微信", "com.tencent.weread": "微信读书"}
	if len(names) != len(want) {
		t.Fatalf("names = %v", names)
	}
	for id, n := range want {
		if names[id] != n {
			t.Errorf("%s = %q, want %q", id, names[id], n)
		}
	}
	apps := []driver.App{
		{ID: "com.tencent.xin", Name: "WeChat"},
		{ID: "com.tencent.weread", Name: "微信读书"},
		{ID: "com.apple.mobilephone", Name: "Phone", System: true},
		{ID: "com.example.hidden", Name: "Hidden"}, // in the App Library only
	}
	addHomeScreenNames(apps, names)
	if len(apps[0].Aliases) != 1 || apps[0].Aliases[0] != "微信" || len(apps[1].Aliases) != 0 ||
		apps[2].Aliases[0] != "电话" || len(apps[3].Aliases) != 0 {
		t.Fatalf("apps = %+v", apps)
	}
}

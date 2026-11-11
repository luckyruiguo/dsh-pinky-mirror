package ios

import (
	"reflect"
	"testing"
)

func TestLaunchInput(t *testing.T) {
	opts := func(in map[string]any) map[string]any { return in["options"].(map[string]any) }
	app, err := launchInput("com.apple.Preferences", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := opts(app)["payloadURL"]; ok {
		t.Error("an app launch carries a payload URL")
	}
	if got := app["applicationSpecifier"]; !reflect.DeepEqual(got, map[string]any{"bundleIdentifier": map[string]any{"_0": "com.apple.Preferences"}}) {
		t.Errorf("applicationSpecifier = %v", got)
	}
	link, err := launchInput(safariID, "https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	if got := opts(link)["payloadURL"]; !reflect.DeepEqual(got, map[string]any{"relative": "https://example.com/"}) {
		t.Errorf("payloadURL = %v", got)
	}
}

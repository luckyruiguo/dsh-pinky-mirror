package ios

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
)

// switchWDA answers the WebDriverAgent routes the switches use, as the
// iOS 26.5 Simulator did.
type switchWDA struct {
	mu          sync.Mutex
	orientation string // UIDeviceOrientation…
	stuck       bool   // the app only runs upright: POST /orientation fails, the device turns
	stays       bool   // the same on iOS 27: the device stays too
	style       string
	lat, lon    *float64
	locale      string
	appState    int // XCUIApplicationState of every app
	calls       []string
}

func (f *switchWDA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	path := r.URL.Path
	if rest, ok := strings.CutPrefix(path, "/session/s1"); ok {
		path = rest
	}
	route := r.Method + " " + path
	f.calls = append(f.calls, strings.TrimSpace(route+" "+string(b)))
	var body map[string]any
	json.Unmarshal(b, &body)
	switch route {
	case "POST /session":
		fmt.Fprint(w, `{"value":{"sessionId":"s1","capabilities":{}},"sessionId":"s1"}`)
	case "POST /appium/settings":
		fmt.Fprint(w, `{"value":null}`)
	case "GET /wda/deviceOrientation":
		fmt.Fprintf(w, `{"value":%q}`, f.orientation)
	case "POST /orientation":
		if f.stays {
			w.WriteHeader(500)
			fmt.Fprint(w, `{"value":{"error":"unknown error","message":"Unable To Rotate Device"}}`)
			return
		}
		f.orientation = map[string]string{"PORTRAIT": "UIDeviceOrientationPortrait", "LANDSCAPE": "UIDeviceOrientationLandscapeLeft",
			"UIA_DEVICE_ORIENTATION_LANDSCAPERIGHT":      "UIDeviceOrientationLandscapeRight",
			"UIA_DEVICE_ORIENTATION_PORTRAIT_UPSIDEDOWN": "UIDeviceOrientationPortraitUpsideDown"}[body["orientation"].(string)]
		if f.stuck {
			w.WriteHeader(500)
			fmt.Fprint(w, `{"value":{"error":"unknown error","message":"Unable To Rotate Device"}}`)
			return
		}
		fmt.Fprint(w, `{"value":null}`)
	case "GET /rotation":
		fmt.Fprint(w, `{"value":{"x":0,"y":0,"z":0}}`)
	case "GET /wda/device/info":
		fmt.Fprintf(w, `{"value":{"userInterfaceStyle":%q,"currentLocale":%q,"model":"iPhone"}}`, f.style, f.locale)
	case "POST /wda/device/appearance":
		f.style = body["name"].(string)
		fmt.Fprint(w, `{"value":null}`)
	case "GET /wda/simulatedLocation":
		if f.lat == nil {
			fmt.Fprint(w, `{"value":{"latitude":null,"longitude":null,"altitude":null}}`)
			return
		}
		fmt.Fprintf(w, `{"value":{"latitude":%v,"longitude":%v,"altitude":null}}`, *f.lat, *f.lon)
	case "POST /wda/simulatedLocation":
		lat, lon := body["latitude"].(float64), body["longitude"].(float64)
		f.lat, f.lon = &lat, &lon
		fmt.Fprint(w, `{"value":null}`)
	case "DELETE /wda/simulatedLocation":
		f.lat, f.lon = nil, nil
		fmt.Fprint(w, `{"value":null}`)
	case "POST /wda/apps/terminate", "POST /wda/apps/launch":
		fmt.Fprint(w, `{"value":true}`)
	case "POST /wda/apps/state":
		fmt.Fprintf(w, `{"value":%d}`, f.appState)
	default:
		w.WriteHeader(404)
		fmt.Fprintf(w, `{"value":{"error":"unknown command","message":"%s"}}`, path)
	}
}

func (f *switchWDA) took(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func newSwitchWDA(t *testing.T) (*switchWDA, *WDADevice) {
	t.Helper()
	f := &switchWDA{orientation: "UIDeviceOrientationPortrait", style: "light", locale: "en_US@rg=sgzzzz"}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, &WDADevice{c: newWDAURL(srv.URL, ""), scale: 3, width: 1179, height: 2556}
}

func TestWDASwitches(t *testing.T) {
	ctx := context.Background()
	f, d := newSwitchWDA(t)
	read := func(name string) string {
		t.Helper()
		v, err := d.Switch(ctx, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return v
	}
	if read(driver.SwitchRotation) != "portrait" || read(driver.SwitchAppearance) != "light" || read(driver.SwitchLocation) != "" {
		t.Fatal("first read")
	}

	for _, r := range []string{"landscape", "reverse_landscape", "upside_down", "portrait"} {
		if err := d.SetSwitch(ctx, driver.SwitchRotation, r); err != nil {
			t.Fatalf("turn to %s: %v", r, err)
		}
		if got := read(driver.SwitchRotation); got != r {
			t.Fatalf("turned to %s, reads %s", r, got)
		}
	}
	// An app that only runs upright: WDA says it could not rotate, but the
	// device turned, which is what the switch is.
	f.mu.Lock()
	f.stuck = true
	f.mu.Unlock()
	if err := d.SetSwitch(ctx, driver.SwitchRotation, "landscape"); err != nil {
		t.Fatalf("turn with an upright app: %v", err)
	}
	// On iOS 27 the device stays as the app in front allows: said in words,
	// not in WDA's.
	f.mu.Lock()
	f.stays = true
	f.mu.Unlock()
	if err := d.SetSwitch(ctx, driver.SwitchRotation, "reverse_landscape"); err == nil ||
		err.Error() != "the app in front does not turn reverse landscape, so the phone stayed landscape" {
		t.Fatalf("turn where the device stays: %v", err)
	}
	f.mu.Lock()
	f.stays = false
	f.mu.Unlock()
	if err := d.SetSwitch(ctx, driver.SwitchRotation, "auto"); !errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("auto: %v", err)
	}

	if err := d.SetSwitch(ctx, driver.SwitchAppearance, "dark"); err != nil || read(driver.SwitchAppearance) != "dark" {
		t.Fatalf("dark: %v", err)
	}
	if err := d.SetSwitch(ctx, driver.SwitchAppearance, "auto"); !errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("automatic appearance: %v", err)
	}

	if err := d.SetSwitch(ctx, driver.SwitchLocation, "35.6812,139.7671"); err != nil {
		t.Fatal(err)
	}
	if got := read(driver.SwitchLocation); got != "35.6812,139.7671" {
		t.Fatalf("location %q", got)
	}
	if err := d.SetSwitch(ctx, driver.SwitchLocation, ""); err != nil || read(driver.SwitchLocation) != "" {
		t.Fatalf("stop the location: %v", err)
	}
	if got := f.took("POST /wda/simulatedLocation"); len(got) != 1 || !strings.Contains(got[0], `"latitude":35.6812`) {
		t.Fatalf("location calls %q", got)
	}

	f.mu.Lock()
	f.style = "unsupported"
	f.mu.Unlock()
	if _, err := d.Switch(ctx, driver.SwitchAppearance); !errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("an unknown style: %v", err)
	}
}

func TestWDALaunchLocale(t *testing.T) {
	ctx := context.Background()
	f, d := newSwitchWDA(t)
	restore, err := d.LaunchLocale(ctx, "com.apple.Preferences", driver.Locale{Language: "zh-Hans", Region: "CN"})
	if err != nil || restore == nil {
		t.Fatalf("launch: %v (restore %v)", err, restore != nil)
	}
	// Region alone: the phone's language goes with it.
	if _, err := d.LaunchLocale(ctx, "com.apple.Preferences", driver.Locale{Region: "DE"}); err != nil {
		t.Fatal(err)
	}
	if got := f.took("POST /wda/apps/terminate"); len(got) != 2 {
		t.Fatalf("terminate calls %q: a running app ignores launch arguments", got)
	}
	launches := f.took("POST /wda/apps/launch")
	if len(launches) != 2 ||
		!strings.Contains(launches[0], `"arguments":["-AppleLanguages","(\"zh-Hans-CN\")","-AppleLocale","zh_Hans_CN"]`) ||
		!strings.Contains(launches[1], `"arguments":["-AppleLocale","en_DE"]`) {
		t.Fatalf("launches %q", launches)
	}

	// Putting the language back quits the app while it runs, and leaves
	// it alone once it has quit.
	f.appState = 4
	if err := restore(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.took("POST /wda/apps/terminate"); len(got) != 3 || !strings.Contains(got[2], "com.apple.Preferences") {
		t.Fatalf("terminate calls after putting back %q", got)
	}
	f.appState = 1
	if err := restore(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.took("POST /wda/apps/terminate"); len(got) != 3 {
		t.Fatalf("quit an app that no longer runs: %q", got)
	}
}

func TestLocaleArguments(t *testing.T) {
	for _, c := range []struct {
		loc   driver.Locale
		phone string
		want  string
	}{
		{driver.Locale{Language: "ja"}, "", `-AppleLanguages ("ja")`},
		{driver.Locale{Language: "pt", Region: "BR"}, "", `-AppleLanguages ("pt-BR") -AppleLocale pt_BR`},
		{driver.Locale{Region: "JP"}, "en_US@rg=sgzzzz", `-AppleLocale en_JP`},
		{driver.Locale{Region: "TW"}, "zh-Hant_HK", `-AppleLocale zh_Hant_TW`},
	} {
		args, err := localeArguments(c.loc, c.phone)
		var parts []string
		for _, a := range args {
			parts = append(parts, a.(string))
		}
		if got := strings.Join(parts, " "); err != nil || got != c.want {
			t.Errorf("localeArguments(%+v, %q) = %q, %v; want %q", c.loc, c.phone, got, err, c.want)
		}
	}
	if _, err := localeArguments(driver.Locale{Region: "JP"}, ""); !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("a region with no language to go with: %v", err)
	}
}

func TestPhoneLanguage(t *testing.T) {
	for in, want := range map[string]string{"en_US@rg=sgzzzz": "en", "zh-Hans_CN": "zh-Hans", "ja_JP": "ja", "fr": "fr", "": ""} {
		if got := phoneLanguage(in); got != want {
			t.Errorf("phoneLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

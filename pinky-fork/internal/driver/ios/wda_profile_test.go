package ios

import (
	"bytes"
	"context"
	"encoding/asn1"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	goios "github.com/danielpaulus/go-ios/ios"
	"howett.net/plist"
)

const testRunner = "com.diygod.WebDriverAgentRunner.xctrunner"

// profileXML is a provisioning profile's property list, as Xcode makes
// one for a free Apple ID.
func profileXML(t *testing.T, appID string, devices []string, expires time.Time) []byte {
	t.Helper()
	m := map[string]any{
		"AppIDName":                   "XC com diygod WebDriverAgentRunner xctrunner",
		"ApplicationIdentifierPrefix": []string{"A1B2C3D4E5"},
		"CreationDate":                expires.Add(-7 * 24 * time.Hour),
		"ExpirationDate":              expires,
		"Entitlements": map[string]any{
			"application-identifier":              appID,
			"com.apple.developer.team-identifier": "A1B2C3D4E5",
			"get-task-allow":                      true,
		},
		"IsXcodeManaged":     true,
		"Name":               "iOS Team Provisioning Profile: " + testRunner,
		"ProvisionedDevices": devices,
		"TeamIdentifier":     []string{"A1B2C3D4E5"},
		"TeamName":           "Someone (Personal Team)",
		"TimeToLive":         7,
		"UUID":               "6F1C9E2A-0000-4000-8000-000000000001",
		"Version":            1,
	}
	b, err := plist.MarshalIndent(m, plist.XMLFormat, "\t")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var (
	oidSignedData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidData       = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
)

// derProfile wraps content in a CMS signed message in DER, with definite
// lengths and the content in one OCTET STRING.
func derProfile(t *testing.T, content []byte) []byte {
	t.Helper()
	type encap struct {
		Type    asn1.ObjectIdentifier
		Content []byte `asn1:"explicit,tag:0"`
	}
	type signedData struct {
		Version     int
		Digests     asn1.RawValue
		Encap       encap
		SignerInfos asn1.RawValue
	}
	type contentInfo struct {
		Type    asn1.ObjectIdentifier
		Content signedData `asn1:"explicit,tag:0"`
	}
	set := asn1.RawValue{Tag: asn1.TagSet, IsCompound: true}
	b, err := asn1.Marshal(contentInfo{oidSignedData, signedData{1, set, encap{oidData, content}, set}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func berLen(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	var b []byte
	for ; n > 0; n >>= 8 {
		b = append([]byte{byte(n)}, b...)
	}
	return append([]byte{0x80 | byte(len(b))}, b...)
}

func tlv(tag byte, content ...[]byte) []byte {
	c := bytes.Join(content, nil)
	return append(append([]byte{tag}, berLen(len(c))...), c...)
}

func indefinite(tag byte, content ...[]byte) []byte {
	return append(append([]byte{tag, 0x80}, bytes.Join(content, nil)...), 0, 0)
}

// berProfile wraps content as Apple's profiles do: BER with indefinite
// lengths, the content a constructed OCTET STRING in pieces.
func berProfile(content []byte, piece int) []byte {
	oid := func(o asn1.ObjectIdentifier) []byte { b, _ := asn1.Marshal(o); return b }
	var pieces [][]byte
	for rest := content; len(rest) > 0; {
		n := min(len(rest), piece)
		pieces = append(pieces, tlv(0x04, rest[:n]))
		rest = rest[n:]
	}
	sha1 := tlv(0x30, oid(asn1.ObjectIdentifier{1, 3, 14, 3, 2, 26}), []byte{0x05, 0x00})
	return indefinite(0x30,
		oid(oidSignedData),
		indefinite(0xa0,
			indefinite(0x30,
				tlv(0x02, []byte{1}),
				tlv(0x31, sha1),
				indefinite(0x30, oid(oidData), indefinite(0xa0, indefinite(0x24, pieces...))),
				tlv(0xa0, tlv(0x30, tlv(0x02, []byte{7}))), // a certificate, roughly
				tlv(0x31),
			),
		),
	)
}

func TestParseProfile(t *testing.T) {
	exp := time.Date(2026, 11, 13, 6, 5, 0, 0, time.UTC)
	udid := "00008110-001E30C9262A801E"
	pl := profileXML(t, "A1B2C3D4E5."+testRunner, []string{udid}, exp)
	for name, b := range map[string][]byte{
		"DER":                   derProfile(t, pl),
		"BER in 1000-byte runs": berProfile(pl, 1000),
		"BER in one run":        berProfile(pl, len(pl)),
	} {
		pr, err := parseProfile(b)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !pr.Expires.Equal(exp) || pr.AppID != "A1B2C3D4E5."+testRunner || len(pr.Devices) != 1 || pr.Devices[0] != udid ||
			pr.UUID == "" || !strings.Contains(pr.Name, testRunner) || pr.TeamName != "Someone (Personal Team)" || pr.TeamID != "A1B2C3D4E5" {
			t.Errorf("%s: %+v", name, pr)
		}
	}
	der := derProfile(t, pl)
	undated, err := plist.Marshal(map[string]any{"Name": "No date", "Entitlements": map[string]any{"application-identifier": "A1B2C3D4E5." + testRunner}}, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{
		"empty":     nil,
		"truncated": der[:len(der)/2],
		"a plist":   pl,
		"no plist":  derProfile(t, []byte("not a property list")),
		"no date":   derProfile(t, undated),
		"too deep":  bytes.Repeat([]byte{0x30, 0x80}, 40),
	} {
		if _, err := parseProfile(b); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestHelperProfile(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	udid := "00008110-001E30C9262A801E"
	mk := func(appID string, devices []string, days int) provisioningProfile {
		return provisioningProfile{AppID: appID, Devices: devices, Expires: now.Add(time.Duration(days) * 24 * time.Hour)}
	}
	old := mk("A1B2C3D4E5."+testRunner, []string{udid}, -2)
	cur := mk("A1B2C3D4E5."+testRunner, []string{strings.ToLower(udid)}, 5)
	other := mk("A1B2C3D4E5.com.diygod.other", []string{udid}, 300)
	elsewhere := mk("A1B2C3D4E5."+testRunner, []string{"00008030-000000000000002E"}, 300)
	teamWild := mk("A1B2C3D4E5.*", []string{udid}, 200) // the team's, as on the test phone
	otherTeam := mk("Z9Y8X7W6V5.com.diygod.*", []string{udid}, 250)
	enterprise := provisioningProfile{AppID: "A1B2C3D4E5.com.diygod.*", AllDevices: true, Expires: now.Add(400 * 24 * time.Hour)}
	signed := wdaRunner{BundleID: testRunner, AppID: "A1B2C3D4E5." + testRunner}
	bare := wdaRunner{BundleID: testRunner} // its entitlements unknown
	for _, c := range []struct {
		name     string
		profiles []provisioningProfile
		runner   wdaRunner
		want     time.Time
		found    bool
	}{
		{"none", nil, signed, time.Time{}, false},
		{"another app's and another phone's", []provisioningProfile{other, elsewhere}, signed, time.Time{}, false},
		{"the old one alone", []provisioningProfile{old}, signed, old.Expires, true},
		{"the newer of two", []provisioningProfile{cur, old, other, elsewhere}, signed, cur.Expires, true},
		{"the team's wildcard", []provisioningProfile{cur, teamWild}, signed, teamWild.Expires, true},
		{"another team's wildcard", []provisioningProfile{cur, otherTeam}, signed, cur.Expires, true},
		{"another team's, the team unknown", []provisioningProfile{cur, otherTeam}, bare, otherTeam.Expires, true},
		{"any device", []provisioningProfile{enterprise}, signed, enterprise.Expires, true},
		{"no application identifier", []provisioningProfile{{Devices: []string{udid}, Expires: now}}, signed, time.Time{}, false},
	} {
		pr, ok := helperProfile(c.profiles, c.runner, udid)
		if ok != c.found || !pr.Expires.Equal(c.want) {
			t.Errorf("%s: %v %v, want %v %v", c.name, pr.Expires, ok, c.want, c.found)
		}
	}
	if (provisioningProfile{AppID: "A1B2C3D4E5.com.diygod.*"}).grants(wdaRunner{BundleID: "com.other.WebDriverAgentRunner.xctrunner"}) {
		t.Error("a wildcard granted another prefix")
	}
}

// fakeMisagent answers one request as misagent does.
type fakeMisagent struct {
	sent   bytes.Buffer
	answer []byte
}

func (f *fakeMisagent) Send(b []byte) error { f.sent.Write(b); return nil }
func (f *fakeMisagent) Reader() io.Reader   { return bytes.NewReader(f.answer) }

func TestCopyProfiles(t *testing.T) {
	codec := goios.NewPlistCodec()
	exp := time.Date(2026, 11, 13, 6, 5, 0, 0, time.UTC)
	pl := profileXML(t, "A1B2C3D4E5."+testRunner, nil, exp)
	answer, err := codec.Encode(map[string]any{"Status": 0, "Payload": [][]byte{derProfile(t, pl), berProfile(pl, 512)}})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeMisagent{answer: answer}
	payloads, err := copyProfiles(f)
	if err != nil || len(payloads) != 2 {
		t.Fatalf("copyProfiles = %d payloads, %v", len(payloads), err)
	}
	req, err := codec.Decode(&f.sent)
	if err != nil {
		t.Fatal(err)
	}
	m, err := goios.ParsePlist(req)
	if err != nil || m["MessageType"] != "CopyAll" || m["ProfileType"] != "Provisioning" || len(m) != 2 {
		t.Fatalf("request %v, %v; want only CopyAll of provisioning profiles", m, err)
	}
	for _, b := range payloads {
		if pr, err := parseProfile(b); err != nil || !pr.Expires.Equal(exp) {
			t.Errorf("payload: %+v %v", pr, err)
		}
	}
	refusal, _ := codec.Encode(map[string]any{"Status": 0xe8008001})
	if _, err := copyProfiles(&fakeMisagent{answer: refusal}); err == nil {
		t.Error("a refusal was not an error")
	}
}

// TestProviderHelperExpiry reads when the helper's profile runs out while
// inspecting a phone that is driven through it, Automatic below iOS 27 or
// chosen, and lists one whose profile ran out as needing it installed again.
func TestProviderHelperExpiry(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	p := NewProvider()
	p.now = func() time.Time { return now }
	p.legacySetup = func(context.Context, goios.DeviceEntry) (bool, error) { return true, nil }
	p.findRunner = func(goios.DeviceEntry) (string, error) { return testRunner, nil }
	exp, readErr := now.Add(5*24*time.Hour), error(nil)
	p.helperFn = func(_ context.Context, _ goios.DeviceEntry, r wdaRunner) (time.Time, error) {
		if r.BundleID != testRunner {
			t.Errorf("read the profile of %+v", r)
		}
		return exp, readErr
	}
	ctx, e := context.Background(), entry("old", 1, "USB")
	base := driver.Info{ID: "old", State: driver.StateOnline, Backend: backendCoreDevice, OSVersion: "18.6"}
	ways := map[string]func(context.Context, goios.DeviceEntry, driver.Info) driver.Info{"automatic": p.inspectLegacy, "chosen": p.inspectWDA}
	for way, inspect := range ways {
		if got := inspect(ctx, e, base); got.State != driver.StateOnline || got.Backend != backendWDA || !got.HelperExpires.Equal(exp) || got.Setup != "" {
			t.Errorf("%s, 5 days left: %+v", way, got)
		}
	}
	exp = now.Add(-time.Minute)
	for way, inspect := range ways {
		got := inspect(ctx, e, base)
		if got.State != driver.StateNeedsSetup || got.Setup != driver.SetupInstallHelper || !got.HelperExpires.Equal(exp) ||
			!strings.Contains(got.Detail, "ran out on") || !strings.Contains(got.Detail, "Install it again on a Mac with Xcode") {
			t.Errorf("%s, ran out: %+v", way, got)
		}
	}
	exp, readErr = time.Time{}, errors.New("misagent: no answer")
	for way, inspect := range ways {
		if got := inspect(ctx, e, base); got.State != driver.StateOnline || !got.HelperExpires.IsZero() {
			t.Errorf("%s, profile unreadable: %+v", way, got)
		}
	}
	// Chosen without the helper installed: the setup step says so.
	p.findRunner = func(goios.DeviceEntry) (string, error) { return "", nil }
	if got := p.inspectWDA(ctx, e, base); got.State != driver.StateNeedsSetup || got.Setup != driver.SetupInstallHelper || got.Detail != wdaMissing {
		t.Errorf("no helper: %+v", got)
	}
}

// TestProviderHelperRecheck reads an online phone's helper profile again
// every half hour, every minute within the reminder, notices the helper
// installed again, and inspects the phone again once the profile ran out.
func TestProviderHelperRecheck(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	p := NewProvider()
	p.now = func() time.Time { return now }
	p.legacySetup = func(context.Context, goios.DeviceEntry) (bool, error) { return true, nil }
	exp := now.Add(3 * 24 * time.Hour)
	reads, inspects := 0, 0
	p.helperFn = func(context.Context, goios.DeviceEntry, wdaRunner) (time.Time, error) { reads++; return exp, nil }
	p.inspectFn = func(ctx context.Context, e goios.DeviceEntry) driver.Info {
		inspects++
		return p.withHelper(ctx, e, driver.Info{ID: "phone", State: driver.StateOnline, Backend: backendWDA}, wdaRunner{BundleID: testRunner})
	}
	ctx, e := context.Background(), entry("phone", 1, "USB")
	step := func(d time.Duration, state driver.State, wantReads, wantInspects int) driver.Info {
		t.Helper()
		now = now.Add(d)
		got := p.info(ctx, e)
		if got.State != state || reads != wantReads || inspects != wantInspects {
			t.Fatalf("after %v more: %s, %d reads, %d inspections; want %s, %d and %d", d, got.State, reads, inspects, state, wantReads, wantInspects)
		}
		return got
	}
	step(0, driver.StateOnline, 1, 1)
	step(10*time.Minute, driver.StateOnline, 1, 1)
	step(21*time.Minute, driver.StateOnline, 2, 1) // half an hour on
	// Within the reminder: every minute.
	now = exp.Add(-driver.HelperReminder + time.Hour)
	step(0, driver.StateOnline, 3, 1)
	step(30*time.Second, driver.StateOnline, 3, 1)
	exp = now.Add(7 * 24 * time.Hour) // installed again from Xcode
	if got := step(31*time.Second, driver.StateOnline, 4, 1); !got.HelperExpires.Equal(exp) {
		t.Fatalf("the new profile was not noticed: %v, want %v", got.HelperExpires, exp)
	}
	if got := step(time.Second, driver.StateOnline, 4, 1); !got.HelperExpires.Equal(exp) {
		t.Fatalf("the new profile was not kept: %v", got.HelperExpires)
	}
	// It runs out: the phone is inspected again, which reads the profile
	// once more, and needs the helper.
	now = exp
	if got := step(time.Second, driver.StateNeedsSetup, 5, 2); got.Setup != driver.SetupInstallHelper {
		t.Fatalf("ran out: %+v", got)
	}
	step(10*time.Second, driver.StateNeedsSetup, 5, 2)
	exp = now.Add(7 * 24 * time.Hour) // installed again
	step(21*time.Second, driver.StateOnline, 6, 3)
	// A phone gone is forgotten.
	p.forgetExcept(nil)
	if len(p.helpers) != 0 {
		t.Fatalf("helpers %v after the phone left", p.helpers)
	}
}

func TestCheckHelperProfile(t *testing.T) {
	srv := httptest.NewServer(&fakeWDA{})
	defer srv.Close()
	now := time.Now()
	var exp time.Time
	var expErr error
	d := &WDADevice{c: newWDAURL(srv.URL, ""), helperFn: func(context.Context) (time.Time, error) { return exp, expErr }}
	ctx := context.Background()
	for _, c := range []struct {
		name   string
		exp    time.Time
		err    error
		status driver.CheckStatus
		fix    string
		detail string
	}{
		{"unknown", time.Time{}, nil, driver.CheckPass, "", "WebDriverAgent answers (version 16.17.6)"},
		{"5 days", now.Add(5 * 24 * time.Hour), nil, driver.CheckPass, "", "runs out on"},
		{"30 hours", now.Add(30 * time.Hour), nil, driver.CheckWarn, driver.FixInstallHelper, "before then"},
		{"ran out", now.Add(-time.Hour), nil, driver.CheckWarn, driver.FixInstallHelper, "will not start it again"},
		{"unreadable", time.Time{}, errors.New("misagent: no answer"), driver.CheckPass, "", "unknown"},
	} {
		exp, expErr = c.exp, c.err
		ck := d.checkHelper(ctx)
		if ck.ID != driver.CheckHelper || ck.Status != c.status || ck.Fix != c.fix || !strings.Contains(ck.Detail, c.detail) {
			t.Errorf("%s: %+v", c.name, ck)
		}
	}
	srv.Close()
	exp, expErr = now.Add(-time.Hour), nil
	if ck := d.checkHelper(ctx); ck.Status != driver.CheckFail || ck.Fix != driver.FixInstallHelper || !strings.Contains(ck.Detail, "ran out on") {
		t.Errorf("not answering, ran out: %+v", ck)
	}
	exp = time.Time{}
	if ck := d.checkHelper(ctx); ck.Status != driver.CheckFail || !strings.Contains(ck.Detail, "does not answer") {
		t.Errorf("not answering: %+v", ck)
	}
}

package policy

import (
	"strings"
	"testing"
)

// TestForgetDevice: a removed phone loses its read-only switch, from the
// device card and from the rules file, and the other phones keep theirs.
func TestForgetDevice(t *testing.T) {
	e, rules, _ := loadIn(t)
	handEdit(t, rules, "preset: 2\nmask_codes: true\nread_only_devices: [phone-9, phone-7]\n")
	e.Refresh()
	if err := e.SetReadOnly("phone-3", true); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"phone-9", "phone-7", "phone-3"} {
		if !e.ReadOnly(id) {
			t.Fatalf("%s is not read-only to start with", id)
		}
	}
	if err := e.ForgetDevice("phone-9"); err != nil {
		t.Fatal(err)
	}
	if err := e.ForgetDevice("phone-3"); err != nil {
		t.Fatal(err)
	}
	if e.ReadOnly("phone-9") || e.ReadOnly("phone-3") {
		t.Fatal("a removed phone stayed read-only")
	}
	if !e.ReadOnly("phone-7") {
		t.Fatal("another phone lost its read-only switch")
	}
	fileHas(t, rules, "ForgetDevice", "- phone-7", "mask_codes: true")
	if data := readFile(t, rules); strings.Contains(data, "phone-9") {
		t.Fatalf("rules.yaml still names the removed phone:\n%s", data)
	}

	// A phone the file does not name leaves the file alone.
	before := readFile(t, rules)
	if err := e.ForgetDevice("phone-1"); err != nil {
		t.Fatal(err)
	}
	if readFile(t, rules) != before {
		t.Fatalf("forgetting a phone the file does not name rewrote it:\n%s", readFile(t, rules))
	}

	// Read-only mode for every phone is not the removed phone's to change.
	if err := e.SetReadOnly("", true); err != nil {
		t.Fatal(err)
	}
	if err := e.ForgetDevice("phone-7"); err != nil {
		t.Fatal(err)
	}
	if !e.ReadOnly("phone-7") {
		t.Fatal("ForgetDevice turned off read-only mode for every phone")
	}
}

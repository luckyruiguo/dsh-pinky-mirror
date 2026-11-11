package policy

import (
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

// TestCredentialFieldIdentifiers checks how an input's identifier, which
// agents do not see, counts towards the credentials rule (GitHub issue
// #47: Notes' body on iOS 27.2 was taken for a code field). A code word in
// it counts as a whole word, or joined to a word that says what code it is
// ("smscode"); a word that merely contains one does not make a note or a
// message box a code field. A password word counts anywhere.
func TestCredentialFieldIdentifiers(t *testing.T) {
	for _, c := range []struct {
		label, id string
		want      bool
	}{
		// Real fields, as apps name them.
		{"", "com.example.bank:id/et_verify_code", true},
		{"", "com.example:id/otp_input", true},
		{"", "otpInput", true},
		{"", "OTPCodeField", true},
		{"", "smsCodeEditText", true},
		{"", "verificationCode", true},
		{"", "one-time-code", true},
		{"", "twoFactorCode", true},
		{"", "pin_entry", true},
		{"", "PINField", true},
		{"", "passwordField", true},
		{"", "etpassword", true}, // no word breaks: a password word counts anywhere
		{"", "login.passcode", true},
		{"", "codice_sms", true},
		{"", "codigoVerificacion", true},
		{"", "验证码输入框", true},
		// Words joined without breaks, as many Android apps write them.
		{"", "et_smscode", true},
		{"", "verifycode", true},
		{"", "edt_smsverifycode", true},
		{"", "vcode", true},
		{"", "otpinput", true},
		{"", "otp1", true},
		{"", "pin_1", true},
		{"", "pin2", true},
		{"", "code_3", true},
		{"", "2FACode", true},
		{"Code", "", true},
		{"Verification code", "field_1", true},
		// Words that only contain a code word.
		{"Note", "footprintTextView", false},
		{"Note", "rootPaneBody", false},
		{"Note", "EncodedContentView", false},
		{"Message", "unicodeComposer", false},
		{"Note", "barcodeNotes", false},
		{"Note", "spinnerHost", false},
		{"Note", "maintenanceLog", false},
		{"Note", "snapshotPreviewText", false},
		{"Note", "rootpane", false},
		{"Email", "verifiedEmailInput", false},
		{"Address", "geocodeField", false},
		{"", "zipcode", false},
		{"", "promocode", false},
		{"", "qrcode_scan", false},
		// Codes that are not secrets still do not count.
		{"", "promoCodeInput", false},
		{"", "zip_code", false},
		{"", "countryCodePicker", false},
	} {
		e := &screen.Element{Role: screen.RoleInput, Label: c.label, ID: c.id, States: []string{screen.StateEditable}}
		if got := IsCredentialField(e); got != c.want {
			t.Errorf("label %q, id %q: credential %v, want %v", c.label, c.id, got, c.want)
		}
	}
}

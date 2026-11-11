package driver

import (
	"strings"
	"testing"
)

// TestPairRecordFixCommand: the command the client copies must work on a
// Windows whose ProgramData is on another drive than C:, so it asks PowerShell
// for the folder; and it grants Modify, which is what replacing the record
// needs, rather than full control of a file that holds this computer's private
// key for the phone.
func TestPairRecordFixCommand(t *testing.T) {
	cmd := PairRecordFixCommand("00008120-001A7DCC0162601E")
	want := `icacls "$env:ProgramData\Apple\Lockdown\00008120-001A7DCC0162601E.plist" /grant "*S-1-5-19:(M)"`
	if cmd != want {
		t.Fatalf("PairRecordFixCommand = %q, want %q", cmd, want)
	}
}

// TestPairRecordDirUsesProgramData: the record lives under ProgramData, which
// can be on another drive, and the environment says where it is.
func TestPairRecordDirUsesProgramData(t *testing.T) {
	t.Setenv("ProgramData", `D:\ProgramData`)
	if got, want := PairRecordFile("udid"), `D:\ProgramData\Apple\Lockdown\udid.plist`; got != want {
		t.Fatalf("PairRecordFile = %q, want %q", got, want)
	}
	t.Setenv("ProgramData", "")
	if got, want := PairRecordFile("udid"), `C:\ProgramData\Apple\Lockdown\udid.plist`; got != want {
		t.Fatalf("PairRecordFile without ProgramData = %q, want %q", got, want)
	}
	if strings.Contains(PairRecordFixCommand("udid"), `C:\ProgramData`) {
		t.Fatal("PairRecordFixCommand hardcodes C:\\ProgramData")
	}
}

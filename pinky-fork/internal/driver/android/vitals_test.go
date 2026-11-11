package android

import "testing"

func TestParseBattery(t *testing.T) {
	out := `Current Battery Service state:
  AC powered: false
  USB powered: true
  Wireless powered: false
  Dock powered: false
  Max charging current: 500000
  Max charging voltage: 5000000
  Charge counter: 3912000
  status: 2
  health: 2
  present: true
  level: 87
  scale: 100
  voltage: 4291
  temperature: 312
  technology: Li-ion
`
	b := parseBattery(out)
	if b == nil {
		t.Fatal("no battery")
	}
	if b.Level != 87 || !b.Charging || !b.Plugged || b.Full || b.TempC != 31.2 {
		t.Errorf("got %+v", *b)
	}

	// Full on the charger, and a scale other than 100.
	b = parseBattery("  AC powered: true\n  status: 5\n  level: 50\n  scale: 50\n")
	if b == nil || b.Level != 100 || b.Charging || !b.Plugged || !b.Full || b.TempC != 0 {
		t.Errorf("full: got %+v", b)
	}

	// Plugged in but held (adaptive charging): status 4, not charging.
	b = parseBattery("  USB powered: true\n  status: 4\n  level: 80\n  scale: 100\n")
	if b == nil || b.Charging || !b.Plugged {
		t.Errorf("held: got %+v", b)
	}

	for _, bad := range []string{"", "Can't find service: battery", "  level: x\n", "  level: 150\n  scale: 100\n"} {
		if b := parseBattery(bad); b != nil {
			t.Errorf("parseBattery(%q) = %+v, want nil", bad, *b)
		}
	}
}

func TestParseDF(t *testing.T) {
	out := `Filesystem       1K-blocks     Used Available Use% Mounted on
/dev/block/dm-41 115060828 98472140  16457616  86% /data
`
	s := parseDF(out)
	if s == nil {
		t.Fatal("no storage")
	}
	if s.Total != 128_000_000_000 || s.Free != 16457616*1024 {
		t.Errorf("got %+v", *s)
	}

	// The filesystem's name on a line of its own.
	s = parseDF("Filesystem 1K-blocks Used Available Use% Mounted on\n/dev/block/bootdevice/by-name/userdata\n 52428800 10485760 41943040 20% /data\n")
	if s == nil || s.Total != 64_000_000_000 || s.Free != 41943040*1024 {
		t.Errorf("wrapped: got %+v", s)
	}

	for _, bad := range []string{"", "df: /data: Permission denied", "Filesystem 1K-blocks Used Available\nnone 0 0 0 0% /data\n"} {
		if s := parseDF(bad); s != nil {
			t.Errorf("parseDF(%q) = %+v, want nil", bad, *s)
		}
	}
}

func TestRoundStorageSize(t *testing.T) {
	for _, c := range []struct{ in, want int64 }{
		{115_060_828 * 1024, 128_000_000_000},
		{240_000_000_000, 256_000_000_000},
		{256_000_000_000, 256_000_000_000},
		{52_000_000_000, 64_000_000_000},
		{950_000_000_000, 1_000_000_000_000},
		{1_800_000_000_000, 2_000_000_000_000},
	} {
		if got := roundStorageSize(c.in); got != c.want {
			t.Errorf("roundStorageSize(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

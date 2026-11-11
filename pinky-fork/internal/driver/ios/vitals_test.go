package ios

import "testing"

func TestBatteryOf(t *testing.T) {
	b := batteryOf(map[string]any{"BatteryCurrentCapacity": uint64(64), "BatteryIsCharging": true, "ExternalConnected": true, "FullyCharged": false})
	if b == nil || b.Level != 64 || !b.Charging || !b.Plugged || b.Full {
		t.Errorf("got %+v", b)
	}
	if b := batteryOf(map[string]any{"BatteryIsCharging": true}); b != nil {
		t.Errorf("without a level: got %+v", *b)
	}
	if b := batteryOf(map[string]any{"BatteryCurrentCapacity": uint64(101)}); b != nil {
		t.Errorf("over 100: got %+v", *b)
	}
}

func TestStorageOf(t *testing.T) {
	s := storageOf(map[string]any{"TotalDiskCapacity": uint64(128_000_000_000), "TotalDataAvailable": uint64(87_700_000_000)})
	if s == nil || s.Total != 128_000_000_000 || s.Free != 87_700_000_000 {
		t.Errorf("got %+v", s)
	}
	if s := storageOf(map[string]any{"TotalDiskCapacity": uint64(128_000_000_000)}); s != nil {
		t.Errorf("without free: got %+v", *s)
	}
	if s := storageOf(map[string]any{"TotalDiskCapacity": "x", "TotalDataAvailable": uint64(1)}); s != nil {
		t.Errorf("not a number: got %+v", *s)
	}
}

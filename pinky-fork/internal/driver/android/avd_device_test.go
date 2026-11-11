//go:build device

package android

import (
	"context"
	"testing"
)

// TestAVDsHere lists this computer's AVDs and the emulators that run
// them, starting none.
func TestAVDsHere(t *testing.T) {
	p := NewProvider(Config{Logf: t.Logf})
	vp, err := p.VirtualPhones(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("SDK %s, missing %q %s", vp.Where, vp.Missing, vp.Detail)
	for _, v := range vp.Phones {
		t.Logf("%+v", v)
	}
	infos, err := p.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.Virtual {
			t.Logf("emulator %s: %q %q Android %s", info.ID, info.Name, info.Model, info.OSVersion)
		}
	}
}

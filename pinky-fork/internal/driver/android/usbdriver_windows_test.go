package android

import "testing"

// TestSystemUSBNodes lists this computer's USB devices through SetupAPI,
// as List does on Windows.
func TestSystemUSBNodes(t *testing.T) {
	nodes, err := systemUSBNodes()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		t.Logf("%s %q parent %s compatible %v driverless %v", n.ID, n.Name, n.Parent, n.Compatible, n.Driverless)
	}
	t.Logf("%d USB devices, phones without a driver: %v", len(nodes), driverless(nodes, nil))
}

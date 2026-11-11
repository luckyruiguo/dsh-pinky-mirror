package main

import (
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
)

func TestPickAndroid(t *testing.T) {
	mi := driver.Info{ID: "3e8be48e", Name: "Mi 10 Pro", State: driver.StateOnline}
	pixel := driver.Info{ID: "FA6C20303860", Name: "Pixel", State: driver.StateOnline}
	off := driver.Info{ID: "x", Name: "Old", State: driver.StateOffline}

	if got, err := pickAndroid([]driver.Info{mi, off}, ""); err != nil || got.ID != mi.ID {
		t.Fatalf("only one online: %v %v", got, err)
	}
	if got, err := pickAndroid([]driver.Info{mi, pixel}, "pixel"); err != nil || got.ID != pixel.ID {
		t.Fatalf("by name: %v %v", got, err)
	}
	if got, err := pickAndroid([]driver.Info{mi, pixel}, "3e8be48e"); err != nil || got.ID != mi.ID {
		t.Fatalf("by id: %v %v", got, err)
	}
	if _, err := pickAndroid([]driver.Info{mi, pixel}, ""); err == nil || !strings.Contains(err.Error(), "several") {
		t.Fatalf("two online, none named: %v", err)
	}
	if _, err := pickAndroid([]driver.Info{off}, ""); err == nil || !strings.Contains(err.Error(), "no Android phone") {
		t.Fatalf("none online: %v", err)
	}
	if _, err := pickAndroid([]driver.Info{mi}, "Galaxy"); err == nil {
		t.Fatal("unknown name accepted")
	}
}

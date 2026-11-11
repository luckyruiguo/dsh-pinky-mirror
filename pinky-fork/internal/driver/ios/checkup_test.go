package ios

import (
	"context"
	"errors"
	"image"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
	goios "github.com/danielpaulus/go-ios/ios"
)

// fakeLink is a link whose phone offers services, with no tunnel behind it.
func fakeLink(services ...string) *link {
	rsd := goios.RsdHandshakeResponse{Services: map[string]goios.RsdServiceEntry{}}
	for i, s := range services {
		rsd.Services[s] = goios.RsdServiceEntry{Port: uint32(50000 + i)}
	}
	return &link{rsd: rsd}
}

func TestCheckImage(t *testing.T) {
	defer func(d string) { xcodeDDI = d }(xcodeDDI)
	xcodeDDI = filepath.Join(t.TempDir(), "none")
	t.Setenv(ddiEnv, "")
	ctx := context.Background()

	if c := checkImage(ctx, nil, "", true); c.ID != driver.CheckDiskImage || c.Status != driver.CheckSkip {
		t.Errorf("no link: %+v", c)
	}
	// iOS 27 without the screen and HID services: the image is missing.
	if c := checkImage(ctx, fakeLink(instrumentsService), "", true); c.Status != driver.CheckFail || c.Fix != driver.SetupDiskImage {
		t.Errorf("services missing: %+v", c)
	}
	// Mounted, but nothing on this computer to mount again should it go
	// missing.
	l := fakeLink(displayService, universalHIDService)
	if c := checkImage(ctx, l, t.TempDir(), true); c.Status != driver.CheckWarn || c.Fix != driver.SetupNoDiskImage || !strings.Contains(c.Detail, "should it go missing") {
		t.Errorf("no image here: %+v", c)
	}
	// With an image in Pinky's folder.
	own := t.TempDir()
	writeDiskImage(t, own, "27B52", "27.2.52.0")
	if c := checkImage(ctx, l, own, true); c.Status != driver.CheckPass || !strings.Contains(c.Detail, "Pinky's folder 27B52") {
		t.Errorf("an image in Pinky's folder: %+v", c)
	}
	// Below iOS 27 the screen and HID services are not asked for, but one
	// of the image's own: iOS lists instruments with no image mounted.
	if c := checkImage(ctx, fakeLink(instrumentsService, deviceInfoService), own, false); c.Status != driver.CheckPass {
		t.Errorf("below iOS 27: %+v", c)
	}
	if c := checkImage(ctx, fakeLink(instrumentsService), own, false); c.Status != driver.CheckFail || c.Fix != driver.SetupDiskImage {
		t.Errorf("below iOS 27 without the image: %+v", c)
	}
}

func TestCheckScreenshot(t *testing.T) {
	ok := func(context.Context) (image.Image, error) { return image.NewRGBA(image.Rect(0, 0, 1125, 2436)), nil }
	if c := checkScreenshot(context.Background(), ok); c.ID != driver.CheckScreen || c.Status != driver.CheckPass || c.Detail != "a 1125×2436 screenshot" {
		t.Errorf("a screenshot: %+v", c)
	}
	bad := func(context.Context) (image.Image, error) { return nil, errors.New("instruments: timed out") }
	if c := checkScreenshot(context.Background(), bad); c.Status != driver.CheckFail || c.Fix != driver.FixScreenshot || !strings.Contains(c.Detail, "timed out") {
		t.Errorf("no screenshot: %+v", c)
	}
}

func TestCheckInputWithoutHID(t *testing.T) {
	d := &Device{l: fakeLink(displayService)}
	if c := d.checkInput(context.Background()); c.ID != driver.CheckInput || c.Status != driver.CheckFail {
		t.Errorf("no HID service: %+v", c)
	}
}

func TestImageName(t *testing.T) {
	for _, c := range []struct {
		img  diskImage
		want string
	}{
		{diskImage{Source: sourceXcode, Build: "27B52"}, "Xcode's image 27B52"},
		{diskImage{Source: sourceMirror, Build: "27A5228h"}, "the image downloaded from github.com/doronz88/DeveloperDiskImage 27A5228h"},
		{diskImage{Source: sourceEnv}, "the image PINKY_DDI names"},
	} {
		if got := imageName(c.img); got != c.want {
			t.Errorf("imageName(%+v) = %q, want %q", c.img, got, c.want)
		}
	}
}

func TestRunChecksKeepsOrder(t *testing.T) {
	mk := func(id string) func(context.Context) driver.Check {
		return func(context.Context) driver.Check { return driver.Check{ID: id, Status: driver.CheckPass} }
	}
	cu, err := runChecks(context.Background(), driver.Info{OSVersion: "27.0.1"}, mk("a"), mk("b"), mk("c"))
	if err != nil || cu.System != "iOS 27.0.1" || len(cu.Checks) != 3 || cu.Checks[0].ID != "a" || cu.Checks[2].ID != "c" {
		t.Fatalf("runChecks = %+v, %v", cu, err)
	}
}

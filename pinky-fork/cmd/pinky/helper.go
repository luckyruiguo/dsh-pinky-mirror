package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/ios"
)

// helperUsage is how `pinky helper` is used.
const helperUsage = "usage: pinky helper install [--team ID] [--bundle-id ID] [--local-signing] [phone id or name]"

// runHelper is `pinky helper install`: it builds the WebDriverAgent helper
// with Xcode, gives it Pinky's name and icon, signs it with the person's
// team and installs it on an iPhone (internal/driver/ios/helperinstall.go).
// Run again to sign it anew once its signature runs out.
func runHelper(args []string) error {
	if len(args) == 0 || args[0] != "install" {
		return errors.New(helperUsage)
	}
	fs := flag.NewFlagSet("pinky helper install", flag.ContinueOnError)
	team := fs.String("team", "", "Apple developer `team ID` to sign with (default: the team of the helper on the phone, or the only one on this Mac)")
	bundleID := fs.String("bundle-id", "", "the helper's `bundle id` (default: the one on the phone)")
	local := fs.Bool("local-signing", false, "sign only with the profiles on this Mac; never let Xcode register the phone or make a profile")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New(helperUsage)
	}
	dirs, err := api.DefaultDirs()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	err = ios.InstallHelper(ctx, ios.HelperInstall{
		Dir:          filepath.Join(dirs.Data, "WebDriverAgent"),
		Phone:        fs.Arg(0),
		Team:         *team,
		BundleID:     *bundleID,
		LocalSigning: *local,
		Out:          os.Stdout,
	})
	var teams *driver.HelperTeamsError
	if errors.As(err, &teams) {
		return fmt.Errorf("%w\nName one with --team.", err)
	}
	return err
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// runDevices lists phones: from the running gateway, or straight from the
// providers when none runs.
func runDevices(args []string) error {
	fs := flag.NewFlagSet("pinky devices", flag.ContinueOnError)
	fake := fs.Bool("fake", false, "list the fake phones when no gateway runs")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dirs, err := api.DefaultDirs()
	if err != nil {
		return err
	}
	var devs []gateway.DeviceView
	source := "gateway"
	if ctl, err := api.DialControl(dirs); err == nil {
		defer ctl.Close()
		if err := ctl.Call("devices", nil, &devs); err != nil {
			return err
		}
	} else {
		source = "no gateway running"
		var providers []driver.Provider
		if *fake {
			providers, _ = demo.World()
		} else {
			providers = realProviders(slog.New(slog.NewTextHandler(os.Stderr, nil)))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, p := range providers {
			infos, err := p.List(ctx)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", p.Platform(), err)
				continue
			}
			if hc, ok := p.(driver.HostChecker); ok {
				if c := hc.CheckHost(); c.Missing != "" {
					fmt.Fprintf(os.Stderr, "%s: %s\n", p.Platform(), c.Detail)
				}
			}
			for _, info := range infos {
				devs = append(devs, listedView(info))
			}
		}
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(devs)
	}
	if len(devs) == 0 {
		fmt.Printf("No phones (%s).\n", source)
		if source != "gateway" && !*fake {
			fmt.Println("Connect a phone with USB debugging on (Android) or an iPhone on iOS 27 or later, or start an iOS Simulator or Android emulator in the app (Add phone › Virtual phone); `pinky devices --fake` lists the fake phones.")
		}
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tID\tPLATFORM\tCONNECTION\tSTATUS")
	for _, d := range devs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", d.Info.Name, d.Info.ID, d.Info.Platform, d.Info.Connection, statusColumn(d))
	}
	if source != "gateway" {
		fmt.Fprintf(w, "(%s)\n", source)
	}
	return w.Flush()
}

// listedView is a phone as its provider lists it when no gateway runs,
// with the status the gateway gives a phone it has not opened: one that
// waits for a step needs setup (gateway.DeviceNeedsSetup), not offline.
// The status line is the driver's state.
func listedView(info driver.Info) gateway.DeviceView {
	v := gateway.DeviceView{Info: info, Status: gateway.DeviceIdle, StatusLine: string(info.State)}
	switch info.State {
	case driver.StateOnline:
	case driver.StateUnauthorized, driver.StateNeedsSetup:
		v.Status = gateway.DeviceNeedsSetup
	default:
		v.Status = gateway.DeviceOffline
	}
	return v
}

// statusColumn is a phone's STATUS: its status line, then the driver's
// detail when the phone is not online, unless the line already ends with
// it (the gateway's "Offline · <detail>").
func statusColumn(d gateway.DeviceView) string {
	line := d.StatusLine
	if d.Info.Detail != "" && d.Info.State != driver.StateOnline && !strings.HasSuffix(line, d.Info.Detail) {
		line += " · " + d.Info.Detail
	}
	return line
}

// runNeeds lists what waits for the person. A development aid.
func runNeeds(args []string) error {
	ctl, err := control()
	if err != nil {
		return err
	}
	defer ctl.Close()
	var needs []api.NeedInfo
	if err := ctl.Call("needs", nil, &needs); err != nil {
		return err
	}
	if len(needs) == 0 {
		fmt.Println("Nothing needs you.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tKIND\tDEVICE\tRULE\tTITLE\tLEFT")
	for _, n := range needs {
		left := ""
		if !n.Deadline.IsZero() {
			left = time.Until(n.Deadline).Round(time.Second).String()
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", n.ID, n.Kind, n.DeviceName, n.Rule, n.Title, left)
	}
	return w.Flush()
}

// runDecide answers a need. The gateway accepts it only when it runs fake
// phones; real approvals happen in the app.
func runDecide(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: pinky decide <need-id> allow|allow_task|allow_always|deny|take_over [note]")
	}
	d := gateway.Decision(args[1])
	if args[1] == "allow" {
		d = gateway.AllowOnce
	}
	ctl, err := control()
	if err != nil {
		return err
	}
	defer ctl.Close()
	return ctl.Call("decide", api.DecideParams{Need: args[0], Decision: d, Note: strings.Join(args[2:], " ")}, nil)
}

func control() (*api.Control, error) {
	dirs, err := api.DefaultDirs()
	if err != nil {
		return nil, err
	}
	ctl, err := api.DialControl(dirs)
	if err != nil {
		return nil, fmt.Errorf("no gateway running (%w)", err)
	}
	return ctl, nil
}

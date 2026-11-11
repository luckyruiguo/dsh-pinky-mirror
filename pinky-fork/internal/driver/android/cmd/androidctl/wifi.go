package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"rsc.io/qr"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
	"github.com/DIYgod/pinky/internal/driver/android/mdns"
)

// Wi-Fi commands: they need no phone selected, and talk to the adb server
// at ADB_SERVER_SOCKET (tcp:127.0.0.1:5038 for a second server started with
// ADB_MDNS=0 ADB_USB=0 ADB_EMU=0, which stands in for a server whose
// discovery has stopped).
func wifi(ctx context.Context, cfg android.Config, cmd string, args []string) (bool, error) {
	switch cmd {
	case "wifi-search":
		fs := flag.NewFlagSet(cmd, flag.ExitOnError)
		seconds := fs.Int("seconds", 10, "how long to search")
		fs.Parse(args)
		return true, wifiSearch(ctx, time.Duration(*seconds)*time.Second)
	case "pair-qr":
		if len(args) != 1 {
			return true, fmt.Errorf("usage: pair-qr FILE.png")
		}
		return true, pairQR(ctx, android.NewProvider(cfg), args[0])
	case "pair-code":
		if len(args) != 2 {
			return true, fmt.Errorf("usage: pair-code ADDR CODE")
		}
		start := time.Now()
		info, err := android.NewProvider(cfg).PairWithCode(ctx, args[0], args[1])
		if err != nil {
			return true, err
		}
		fmt.Printf("paired %s (%s, %s) in %v\n", info.Name, info.ID, info.Connection, time.Since(start).Round(time.Millisecond))
		return true, nil
	case "wifi-connect":
		if len(args) != 1 {
			return true, fmt.Errorf("usage: wifi-connect ADDR")
		}
		info, err := android.NewProvider(cfg).ConnectWiFi(ctx, args[0])
		if err != nil {
			return true, err
		}
		fmt.Printf("connected %s (%s, %s)\n", info.Name, info.ID, info.Connection)
		return true, nil
	case "wifi-reconnect":
		fs := flag.NewFlagSet(cmd, flag.ExitOnError)
		seconds := fs.Int("seconds", 60, "how long to keep listing phones")
		fs.Parse(args)
		cfg.ReconnectWiFi = true
		return true, wifiReconnect(ctx, android.NewProvider(cfg), time.Duration(*seconds)*time.Second)
	}
	return false, nil
}

func wifiSearch(ctx context.Context, d time.Duration) error {
	b, err := mdns.Browse([]string{adb.ServiceConnect, adb.ServicePairing}, mdns.Options{})
	if err != nil {
		return err
	}
	defer b.Close()
	start := time.Now()
	seen := map[string]bool{}
	for time.Since(start) < d {
		for _, s := range b.Services() {
			if k := s.String(); !seen[k] {
				seen[k] = true
				fmt.Printf("%6.1fs  %v\n", time.Since(start).Seconds(), s)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	if err := b.Err(); err != nil {
		fmt.Println("search error:", err)
	}
	return nil
}

func pairQR(ctx context.Context, p *android.Provider, file string) error {
	pg, err := p.StartPairing(ctx)
	if err != nil {
		return err
	}
	defer pg.Close()
	code, err := qr.Encode(pg.QR(), qr.M)
	if err != nil {
		return err
	}
	code.Scale = 12
	if err := os.WriteFile(file, code.PNG(), 0o644); err != nil {
		return err
	}
	fmt.Printf("scan %s under Wireless debugging › Pair device with QR code\n", file)
	start := time.Now()
	done := make(chan struct{})
	defer close(done)
	if r, ok := pg.(driver.PairingReporter); ok {
		go func() {
			var last driver.PairingProgress
			for {
				select {
				case <-done:
					return
				case <-time.After(250 * time.Millisecond):
				}
				if pr := r.Progress(); pr != last {
					last = pr
					fmt.Printf("%6.1fs  progress %+v\n", time.Since(start).Seconds(), pr)
				}
			}
		}()
	}
	info, err := pg.Wait(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("%6.1fs  paired %s (%s, %s)\n", time.Since(start).Seconds(), info.Name, info.ID, info.Connection)
	return nil
}

// wifiReconnect lists the phones every two seconds, as the gateway does,
// and prints each change: List reconnects the paired phones.
func wifiReconnect(ctx context.Context, p *android.Provider, d time.Duration) error {
	start := time.Now()
	last := ""
	for time.Since(start) < d {
		infos, err := p.List(ctx)
		if err != nil {
			return err
		}
		now := ""
		for _, i := range infos {
			now += fmt.Sprintf("  %s %s %s %s\n", i.ID, i.Connection, i.State, i.Name)
		}
		if now != last {
			last = now
			fmt.Printf("%6.1fs\n%s", time.Since(start).Seconds(), now)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return nil
}

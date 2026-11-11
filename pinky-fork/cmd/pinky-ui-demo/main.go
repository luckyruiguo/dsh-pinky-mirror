// Command pinky-ui-demo runs Pinky's desktop client against a scripted fake
// gateway, with ffmpeg test patterns in place of phone screens.
//
//	go run ./cmd/pinky-ui-demo                    # one phone
//	go run ./cmd/pinky-ui-demo -scenario three    # three phones
//	go run ./cmd/pinky-ui-demo -gateway           # the real gateway on demo phones
//	go run ./cmd/pinky-ui-demo bench              # measure video decoding
//
// The Demo menu switches scenarios, plugs in a new phone and restarts.
// With -gateway, the client runs against internal/gateway itself, on
// internal/demo's drawn phones, and its Demo menu starts scripted agents
// that call the gateway as an MCP client would.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"github.com/DIYgod/pinky/internal/secret"
	"github.com/DIYgod/pinky/ui"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// demoToken is the agent token Settings › Agents shows in the demo; no
// agent can use it.
const demoToken = "demo-0f3c9a7e51b24d6c8e2a"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "bench" {
		if err := bench(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	var names []string
	for _, s := range fakeapi.Scenarios {
		names = append(names, string(s))
	}
	scenario := flag.String("scenario", "one", "what to start with: "+strings.Join(names, ", "))
	speed := flag.Float64("speed", 1, "how fast the agents work; 2 is twice as fast")
	theme := flag.String("theme", "", `"light" or "dark"; empty follows the system`)
	timeout := flag.Duration("approval-timeout", 60*time.Second, "how long an approval waits before it denies")
	dev := flag.Bool("dev", false, "start with Developer mode on")
	page := flag.String("page", "", `open "activity", "settings" or "add-phone" at start`)
	gw := flag.Bool("gateway", false, "run the real gateway on internal/demo's phones, with scripted agents")
	hidden := flag.Bool("hidden", false, "start with only the menu bar icon, as when an agent starts Pinky")
	withVideo := flag.Bool("video", true, "with -gateway, stream the demo phones' screens; false leaves them without video, as pinky --fake does")
	flag.Parse()
	if *gw {
		if err := runGateway(*theme, *page, *dev, *hidden, *withVideo, *timeout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	api := fakeapi.New(fakeapi.Options{
		Scenario:        fakeapi.Scenario(*scenario),
		Speed:           *speed,
		ApprovalTimeout: *timeout,
	})
	settings := ui.DefaultSettings()
	settings.DeveloperMode = *dev

	var items []*mygo.MenuItem
	for _, s := range fakeapi.Scenarios {
		s := s
		items = append(items, &mygo.MenuItem{
			Label: s.Title(),
			Click: func(*mygo.MenuItem, *mygo.Window) { api.Play(s) },
		})
	}
	items = append(items,
		mygo.Separator(),
		&mygo.MenuItem{Label: "Restart Scenario", Accelerator: "CmdOrCtrl+Shift+R", Click: func(*mygo.MenuItem, *mygo.Window) { api.Play(api.Scenario()) }},
		&mygo.MenuItem{Label: "Plug In a New Phone", Click: func(*mygo.MenuItem, *mygo.Window) { api.ConnectPhone() }},
		&mygo.MenuItem{Label: "Plug In a New iPhone", Click: func(*mygo.MenuItem, *mygo.Window) { api.ConnectIPhone() }},
		&mygo.MenuItem{Label: "Find an iPad on Wi-Fi", Click: func(*mygo.MenuItem, *mygo.Window) { api.FindOnWiFi() }},
		&mygo.MenuItem{Label: "Scan the Pairing QR Code", Click: func(*mygo.MenuItem, *mygo.Window) { api.ScanPairing() }},
	)
	err := ui.Run(api, ui.Options{
		Version:   "0.0.0-demo",
		Theme:     *theme,
		Settings:  &settings,
		StartPage: *page,
		Hidden:    *hidden,
		Menus:     []*mygo.MenuItem{{Label: "Demo", Submenu: items}},
		// The demo keeps PINs in memory, never in the keychain, and shows
		// a made-up agent token.
		Secrets:    &secret.Memory{},
		AgentToken: demoToken,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

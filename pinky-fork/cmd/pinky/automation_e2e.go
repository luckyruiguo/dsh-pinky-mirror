//go:build e2e

package main

// The e2e build can be driven over MyGo's automation socket, for end-to-end
// checks of the desktop app:
//
//	go build -tags e2e -o /tmp/pinky-e2e ./cmd/pinky
//	MYGO_AUTOMATION=/tmp/pinky-ui.sock /tmp/pinky-e2e --fake
import _ "github.com/egoist/mygo/plugins/automation"

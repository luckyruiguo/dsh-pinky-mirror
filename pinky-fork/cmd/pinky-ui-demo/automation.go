//go:build e2e

package main

// The e2e build drives the demo through MyGo's automation socket:
//
//	go build -tags e2e -o /tmp/pinky-ui-demo ./cmd/pinky-ui-demo
//	MYGO_AUTOMATION=/tmp/pinky.sock /tmp/pinky-ui-demo
import _ "github.com/egoist/mygo/plugins/automation"

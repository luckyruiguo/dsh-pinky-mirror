package adb

import (
	"context"
	"fmt"
	"strings"
)

// TCPIP has the phone's adb listen on the network at port, as `adb tcpip`
// does: adbd restarts, dropping the phone's connections for a moment, and
// then takes connections on every network the phone is on, from the
// computers it trusts (any other makes the phone ask), unencrypted, until
// the phone restarts or USB turns it back. Wireless debugging, when it is
// on, stays on.
func (c *Client) TCPIP(ctx context.Context, serial string, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("adb: tcpip: no such port %d", port)
	}
	return c.restart(ctx, serial, fmt.Sprintf("tcpip:%d", port), "restarting in TCP mode")
}

// USB has the phone's adb stop listening on the network, as `adb usb`
// does: adbd restarts, dropping the phone's connections for a moment, and
// then takes them over USB again (and over Wi-Fi when wireless debugging
// is on).
func (c *Client) USB(ctx context.Context, serial string) error {
	return c.restart(ctx, serial, "usb:", "restarting in USB mode")
}

// restart asks adbd to restart in another mode and checks its answer.
// adbd may restart before its answer gets through (Android 17 over
// Wireless debugging): no answer at all counts as restarting, which the
// caller confirms by connecting.
func (c *Client) restart(ctx context.Context, serial, service, want string) error {
	out, err := c.readAll(ctx, serial, service)
	reply := strings.TrimSpace(string(out))
	if strings.HasPrefix(reply, want) || (reply == "" && (err == nil || isClosedErr(err))) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("adb: %s: %s", firstWord(strings.TrimSuffix(service, ":")), reply)
}

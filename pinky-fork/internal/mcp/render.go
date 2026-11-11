package mcp

import (
	"bytes"
	"fmt"
	"image/jpeg"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DIYgod/pinky/internal/gateway"
)

// render turns a gateway result into an MCP tool result. The text is for
// models: the first line is the status (or "error: <code>") with
// "key: value" details separated by " · ", then the message, then the screen
// in full or the lines that changed.
func render(res *gateway.Result) *sdk.CallToolResult {
	var b strings.Builder
	writeResult(&b, res, "")
	out := &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: strings.TrimRight(b.String(), "\n")}},
		IsError: res.IsError()}
	if res.Screenshot != nil {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, res.Screenshot, &jpeg.Options{Quality: 80}); err == nil {
			out.Content = append(out.Content, &sdk.ImageContent{Data: buf.Bytes(), MIMEType: "image/jpeg"})
		}
	}
	return out
}

// StatusLine is the first line of a result.
func StatusLine(res *gateway.Result) string {
	var parts []string
	if res.IsError() {
		parts = append(parts, "error: "+string(res.Error))
	} else {
		parts = append(parts, string(res.Status))
	}
	if res.DeviceName != "" && !res.IsError() {
		parts = append(parts, res.DeviceName)
	}
	if res.ApprovalID != "" {
		parts = append(parts, "approval_id: "+res.ApprovalID)
	}
	if res.Rule != "" {
		parts = append(parts, "rule: "+res.Rule)
	}
	if res.Decision != "" {
		parts = append(parts, "decision: "+res.Decision)
	}
	if res.Settle != "" {
		parts = append(parts, "settle: "+res.Settle)
	}
	if res.Matched != nil {
		parts = append(parts, fmt.Sprintf("matched: %v", *res.Matched))
	}
	if res.Holder != "" {
		parts = append(parts, "holder: "+res.Holder)
	}
	if res.Setup != "" {
		parts = append(parts, "setup: "+res.Setup)
	}
	if !res.Full && res.Screen != nil && len(res.Diff) == 0 && len(res.Steps) == 0 && !res.Early {
		// After a read that settle_ms made early, the action may not have
		// shown yet: "no change" could make the agent repeat it.
		parts = append(parts, "no change")
	}
	return strings.Join(parts, " · ")
}

func writeResult(b *strings.Builder, res *gateway.Result, indent string) {
	line := func(s string) {
		for _, l := range strings.Split(s, "\n") {
			b.WriteString(indent)
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	line(StatusLine(res))
	if res.Note != "" {
		line(fmt.Sprintf("note: %q", res.Note))
	}
	if res.Message != "" {
		line(res.Message)
	}
	if res.Log != "" {
		line(res.Log)
	}
	if res.Task != "" {
		line("device: " + res.Device)
	}
	for _, f := range res.Saved {
		line(fmt.Sprintf("saved: %s · %s", f.Path, gateway.SizeText(f.Size)))
	}
	for i, st := range res.Steps {
		b.WriteString(indent)
		fmt.Fprintf(b, "step %d: ", i+1)
		var sb strings.Builder
		writeResult(&sb, st, "")
		text := strings.TrimRight(sb.String(), "\n")
		first, rest, _ := strings.Cut(text, "\n")
		b.WriteString(first)
		b.WriteByte('\n')
		if rest != "" {
			line(indentLines(rest, "  "))
		}
	}
	if len(res.Steps) > 0 {
		return
	}
	if res.Folder != "" {
		line("folder: " + res.Folder)
		for _, f := range res.Files {
			line(fmt.Sprintf("%s · %s · %s · %s", f.Path, gateway.SizeText(f.Size), f.ModTime.Format("2006-01-02 15:04"), f.Where))
		}
	}
	if a := res.About; a != nil {
		aboutLines(a, line)
	}
	for _, sw := range res.Switches {
		switch {
		case sw.Unsupported != "":
			line(sw.Name + ": unsupported · " + sw.Unsupported)
		case sw.Changed:
			line(sw.Name + ": " + switchValue(sw.Value) + " (was " + switchValue(sw.Was) + ")")
		default:
			line(sw.Name + ": " + switchValue(sw.Value))
		}
	}
	if len(res.Apps) > 0 {
		for _, a := range res.Apps {
			l := a.Name
			if len(a.Aliases) > 0 {
				l += " · " + strings.Join(a.Aliases, " · ")
			}
			l += " (" + a.ID + ")"
			if a.System {
				l += " · system"
			}
			if a.CloneOf != nil {
				l += " · clone of " + a.CloneOf.Name
			}
			line(l)
		}
	}
	switch {
	case res.Screen == nil:
	case res.Full:
		line(res.Screen.Render())
	case len(res.Diff) > 0:
		line(strings.Join(res.Diff, "\n"))
	}
}

// aboutLines are the phone in a phone_device read: "phone: Pixel 8 ·
// Android 16", "battery: 64%, charging", "storage: 87.7 GB free of 128 GB".
func aboutLines(a *gateway.PhoneAbout, line func(string)) {
	var parts []string
	for _, s := range []string{a.Model, a.OS} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) > 0 {
		line("phone: " + strings.Join(parts, " · "))
	}
	if b := a.Battery; b != nil {
		s := fmt.Sprintf("%d%%", b.Level)
		switch {
		case b.Charging:
			s += ", charging"
		case b.Full && b.Plugged:
			s += ", full, plugged in"
		case b.Plugged:
			s += ", plugged in, not charging"
		}
		if b.TempC > 0 {
			s += fmt.Sprintf(", %.0f °C", b.TempC)
		}
		line("battery: " + s)
	}
	if st := a.Storage; st != nil {
		line(fmt.Sprintf("storage: %s free of %s", gateway.StorageText(st.Free), gateway.StorageText(st.Total)))
	}
}

// switchValue is a switch's value in a phone_device result: none for no
// simulated location.
func switchValue(v string) string {
	if v == "" {
		return "none"
	}
	return v
}

func indentLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

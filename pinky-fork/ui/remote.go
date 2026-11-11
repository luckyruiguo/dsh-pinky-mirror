package ui

import (
	"context"
	"net"
	"strconv"
	"strings"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
)

// Remote agents: MCP over Streamable HTTP, for agents that cannot start
// `pinky mcp` on this computer. Off by default, at DefaultRemoteAddr unless
// the person changes it. The agent token is the bearer token; the first
// connection asks the person as over stdio.

// RemotePath is the path of the MCP endpoint Settings tells remote agents
// to use: http://<RemoteAddr>/mcp.
const RemotePath = "/mcp"

// RemoteURL is the URL agents connect to over HTTP.
func RemoteURL(addr string) string { return "http://" + addr + RemotePath }

// RemoteTailnet is how agents on the person's tailnet reach remote agents,
// through Tailscale on this computer.
type RemoteTailnet struct {
	// Name is this computer's tailnet name, such as
	// "macbook.tail1234.ts.net"; "" while Tailscale is not connected here.
	Name string
	// URL is where `tailscale serve` gives the tailnet Pinky's endpoint,
	// such as "https://macbook.tail1234.ts.net/mcp"; "" when it does not.
	URL string
}

// remoteState is Settings › Agents › Remote agents'.
type remoteState struct {
	addr      string // the address being typed
	editing   bool
	showToken bool
	err       string

	// Tailscale on this computer, read off the main thread.
	tn     RemoteTailnet
	tnAddr string // the address tn is for
	tnAt   time.Time
	tnBusy bool
}

// tailnetEveryRemote is how often Remote agents reads Tailscale again.
const tailnetEveryRemote = 5 * time.Second

// remoteTailnet returns what Tailscale last said about reaching Pinky at
// addr through the tailnet, and reads it again off the main thread when
// it is old or for another address.
func (a *app) remoteTailnet(c *mg.Context, addr string) RemoteTailnet {
	r := &a.remote
	read := a.opt.RemoteTailnet
	if read == nil {
		return RemoteTailnet{}
	}
	if r.tnAddr != addr {
		r.tn, r.tnAt = RemoteTailnet{}, time.Time{}
	}
	if !r.tnBusy && time.Since(r.tnAt) >= tailnetEveryRemote {
		r.tnBusy, r.tnAddr = true, addr
		go func() {
			ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
			defer cancel()
			tn := read(ctx, addr)
			a.post(func() {
				r.tnBusy = false
				if r.tnAddr == addr {
					r.tn, r.tnAt = tn, time.Now()
				}
			})
		}()
	}
	c.After(tailnetEveryRemote)
	return r.tn
}

// serveCommand is the command that has Tailscale serve Pinky, listening at
// addr, to the tailnet over HTTPS.
func serveCommand(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	if ip := net.ParseIP(host); host == "" || host == "localhost" || ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		return "tailscale serve --bg " + port
	}
	return "tailscale serve --bg http://" + addr
}

// checkRemoteAddr says what is wrong with an address to listen on, and
// whether other computers can reach it.
func checkRemoteAddr(addr string) (msg string, others bool) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return locales.RemoteAddrInvalid(), false
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return locales.RemotePortInvalid(), false
	}
	switch {
	case host == "localhost":
		return "", false
	case host == "":
		return "", true // every address of this computer
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return locales.RemoteHostInvalid(), false
	}
	return "", !ip.IsLoopback()
}

// remoteAgents is the Remote agents part of Settings › Agents.
func (a *app) remoteAgents(c *mg.Context) {
	p := pal(c)
	st := &a.settings
	r := &a.remote
	if st.RemoteAddr == "" {
		st.RemoteAddr = DefaultRemoteAddr
	}
	mg.Column(c).Gap(10).Label(locales.RemoteTitle()).Children(func() {
		settingsHeading(c, locales.RemoteTitle(), locales.RemoteIntro())
		group(c).Children(func() {
			a.toggleRow(c, "remote", locales.RemoteToggle(), "", !st.RemoteAgents, &st.RemoteAgents)
			if !st.RemoteAgents {
				return
			}
			_, others := checkRemoteAddr(st.RemoteAddr)
			settingRow(c, locales.RemoteAddress(), locales.RemoteAddressDesc(), false, func() {
				if !r.editing {
					mg.Row(c).Gap(10).AlignItems(mg.Center).Children(func() {
						mg.Text(c, st.RemoteAddr).Font("monospace").FontSize(13).Selectable()
						if button(c, locales.SettingsChange(), btnOpt{small: true}).Label(locales.RemoteChangeAddress()).Clicked() {
							r.editing, r.addr, r.err = true, st.RemoteAddr, ""
						}
					})
					return
				}
				mg.Row(c).Gap(8).AlignItems(mg.Center).Children(func() {
					in := mg.TextInput(c.Key("remote-addr"), &r.addr).Label(locales.RemoteAddress()).Placeholder(DefaultRemoteAddr).Width(170).AutoFocus()
					save := button(c, locales.SettingsSave(), btnOpt{small: true, kind: btnPrimary})
					if save.Clicked() || in.Submitted() {
						addr := strings.TrimSpace(r.addr)
						if msg, _ := checkRemoteAddr(addr); msg != "" {
							r.err = msg
						} else {
							st.RemoteAddr, r.editing, r.err = addr, false, ""
							a.changed()
						}
					}
					if button(c, locales.SettingsCancel(), btnOpt{small: true}).Clicked() {
						r.editing, r.err = false, ""
					}
				})
			})
			if r.err != "" {
				mg.Text(c, r.err).FontSize(fsSmall).TextColor(p.red).Role(mg.RoleStatus).Margin(0, sp4, sp2, sp4)
			}
			if others {
				mg.Row(c).Gap(sp2).AlignItems(mg.Start).Padding(10, sp3).Radius(rControl).Background(p.amberTint).Border(1, p.amberLine).Margin(0, sp4, sp2, sp4).
					Children(func() {
						mg.Icon(c, icAlert).FontSize(16).TextColor(p.amber)
						mg.Text(c, locales.RemoteOthersWarning()).
							FontSize(fsSmall).LineHeight(1.4).Grow(1).MinWidth(0)
					})
			}
			url := RemoteURL(st.RemoteAddr)
			a.copyRow(c, locales.RemoteURLTitle(), url, url, locales.RemoteCopyURL(), locales.RemoteCopiedURL(), false)
			tn := a.remoteTailnet(c, st.RemoteAddr)
			a.remoteTailnetURL(c, tn)
			tok := a.opt.AgentToken
			if tok == "" {
				settingRow(c, locales.RemoteToken(), locales.RemoteTokenMissing(), false, nil)
			} else {
				shown := strings.Repeat("•", 16)
				if r.showToken {
					shown = tok
				}
				settingRow(c, locales.RemoteToken(), locales.RemoteTokenDesc(), false, func() {
					mg.Row(c).Gap(8).AlignItems(mg.Center).Children(func() {
						mg.Text(c, shown).Font("monospace").FontSize(12.5).MaxWidth(180).SingleLine()
						word, label := locales.RemoteShow(), locales.RemoteShowToken()
						if r.showToken {
							word, label = locales.RemoteHide(), locales.RemoteHideToken()
						}
						if button(c, word, btnOpt{small: true}).Label(label).Clicked() {
							r.showToken = !r.showToken
						}
						if button(c, "", btnOpt{small: true, icon: icCopy, label: locales.RemoteCopyToken()}).Clicked() {
							a.copy(c, tok, locales.RemoteCopiedToken())
						}
					})
				})
			}
			token := "<agent token>"
			if tok != "" {
				token = tok
			}
			cmd := `claude mcp add --transport http pinky ` + url + ` --header "Authorization: Bearer ` + token + `"`
			shownCmd := cmd
			if tok != "" && !r.showToken {
				shownCmd = strings.Replace(cmd, tok, "…", 1)
			}
			serve := tn.Name != "" && tn.URL == "" && serveCommand(st.RemoteAddr) != ""
			commandRow(c, "Claude Code", locales.RemoteRunHint(), shownCmd, locales.RemoteCommandLabel(), !serve, func() {
				if button(c, "", btnOpt{small: true, icon: icCopy, label: locales.RemoteCopyCommand()}).Clicked() {
					a.copy(c, cmd, locales.RemoteCopiedCommand())
				}
			})
			a.remoteTailnetServe(c, tn, st.RemoteAddr)
		})
	})
}

// Agents on the person's tailnet, such as Grok Bot, reach Pinky through
// Tailscale while it is connected here: at the URL where `tailscale serve`
// gives Pinky to the tailnet (remoteTailnetURL), or once the person runs
// the command that has it do so (remoteTailnetServe). Pinky only reads
// Tailscale's state.

// remoteTailnetURL is the row of the URL `tailscale serve` gives Pinky at.
func (a *app) remoteTailnetURL(c *mg.Context, tn RemoteTailnet) {
	if tn.URL == "" {
		return
	}
	settingRow(c, locales.RemoteTailnetURL(), locales.RemoteTailnetURLDesc(), false, func() {
		mg.Row(c).Gap(8).AlignItems(mg.Center).Children(func() {
			mg.Text(c, tn.URL).Font("monospace").FontSize(12.5).Selectable()
			if button(c, "", btnOpt{small: true, icon: icCopy, label: locales.RemoteCopyTailnetURL()}).Clicked() {
				a.copy(c, tn.URL, locales.RemoteCopiedURL())
			}
		})
	})
}

// remoteTailnetServe is the command that has `tailscale serve` give Pinky
// to the tailnet, while it does not.
func (a *app) remoteTailnetServe(c *mg.Context, tn RemoteTailnet, addr string) {
	if tn.Name == "" || tn.URL != "" {
		return
	}
	if cmd := serveCommand(addr); cmd != "" {
		a.commandBlock(c, locales.RemoteTailnetTitle(), locales.RemoteTailnetHint("https://"+tn.Name+RemotePath), cmd,
			locales.RemoteTailnetCommandLabel(), locales.RemoteCopyCommand(), true)
	}
}

// copyRow is a setting row whose value has a Copy button, named copyLabel,
// which says copied once it copies.
func (a *app) copyRow(c *mg.Context, title, shown, value, copyLabel, copied string, last bool) {
	settingRow(c, title, "", last, func() {
		mg.Row(c).Gap(8).AlignItems(mg.Center).Children(func() {
			mg.Text(c, shown).Font("monospace").FontSize(12.5).Selectable()
			if button(c, "", btnOpt{small: true, icon: icCopy, label: copyLabel}).Clicked() {
				a.copy(c, value, copied)
			}
		})
	})
}

// copy puts text on the clipboard and says so.
func (a *app) copy(c *mg.Context, text, said string) {
	c.WriteClipboard(text)
	c.Toast(said)
}

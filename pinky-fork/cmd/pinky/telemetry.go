package main

import (
	"context"
	"log/slog"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/telemetry"
	"github.com/DIYgod/pinky/ui"
)

// Usage statistics, crash reports and feedback (docs/TELEMETRY.md). The app
// and the daemon capture their crashes from the start; whichever holds the
// gateway counts usage and sends it with the crash reports, while
// Settings › Privacy allows. Scripted fake phones count nothing.

// captureCrashes starts capturing this process's crashes in the data
// folder. Call Stop on a clean exit.
func captureCrashes(dirs api.Dirs, process string, log *slog.Logger) *telemetry.Crashes {
	return telemetry.CaptureCrashes(telemetry.CrashOptions{Dir: dirs.Data, Process: process, Version: version, Log: log})
}

// startTelemetry opens usage statistics for the process that holds the
// gateway: it counts what agents and the person do and sends what is due
// until ctx ends. Close it when the gateway stops.
func startTelemetry(ctx context.Context, rg *runningGateway, cr *telemetry.Crashes, s ui.Settings, process string, fake bool, log *slog.Logger) *telemetry.Client {
	tel := telemetry.Open(telemetry.Options{Dir: rg.Dirs.Data, Version: version, Process: process, Crashes: cr,
		Usage: s.UsageStats, Log: log})
	telemetry.SetDefault(tel)
	if !fake {
		tel.Count(telemetry.Key(process, "starts"))
		rg.MCP.Use(tel.MCPMiddleware())
		go tel.Observe(ctx, rg.G)
	}
	go tel.Run(ctx)
	return tel
}

// clientAPI is the gateway the desktop client uses: one that counts what
// the person does, unless the phones are fake.
func clientAPI(rg *runningGateway, tel *telemetry.Client, fake bool) gateway.API {
	if fake {
		return rg.G
	}
	return telemetry.WrapGateway(rg.G, tel)
}

// applyTelemetry passes Settings › Privacy's choice to the client.
func applyTelemetry(tel *telemetry.Client, s ui.Settings) {
	tel.SetUsage(s.UsageStats)
}

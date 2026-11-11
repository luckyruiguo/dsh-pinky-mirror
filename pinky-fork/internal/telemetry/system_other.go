//go:build !darwin && !windows && !linux

package telemetry

func osVersion() string { return "" }

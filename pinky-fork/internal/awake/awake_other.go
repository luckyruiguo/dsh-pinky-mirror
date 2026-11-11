//go:build !darwin && !windows && !linux

package awake

func hold(string) (func(), error) { return nil, ErrUnsupported }

//go:build !windows

package main

// allowForeground does nothing: other systems let the running app come to
// the front by itself.
func allowForeground() {}

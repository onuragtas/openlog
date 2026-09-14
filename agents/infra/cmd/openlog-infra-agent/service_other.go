//go:build !windows

package main

// runWindowsService is Windows-only.
func runWindowsService() (int, bool) { return 0, false }

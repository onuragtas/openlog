//go:build !darwin && !windows

package inventory

// collectNative is never used on Linux: hostfs.FS.NativeOS is false there.
func (c *Collector) collectNative() *Data { return &Data{} }

// NativeFingerprint is only meaningful on macOS and Windows.
func NativeFingerprint() uint64 { return 0 }

//go:build !linux

package hostfs

func statfs(string) (Statfs, error) { return Statfs{}, ErrUnsupported }

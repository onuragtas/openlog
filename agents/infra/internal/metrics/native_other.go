//go:build !darwin && !windows

package metrics

import "github.com/onuragtas/openlog/agents/infra/internal/config"

// nativeCollectors is never used on Linux: hostfs.FS.NativeOS is false there.
func nativeCollectors(*config.Config) ([]Collector, serviceLookupCollector) { return nil, nil }

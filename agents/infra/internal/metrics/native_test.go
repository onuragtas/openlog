//go:build darwin || windows

package metrics

import (
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/mem"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
)

func TestNativeMemoryInfo(t *testing.T) {
	vm := &mem.VirtualMemoryStat{Total: 16 << 30, Available: 10 << 30, Free: 2 << 30, Inactive: 6 << 30}
	sw := &mem.SwapMemoryStat{Total: 4 << 30, Free: 3 << 30}

	d := ComputeMemory(NativeMemoryInfo("darwin", vm, sw))
	if d.Total != 16<<30 || d.Free != 2<<30 || d.Cached != 6<<30 || d.Buffers != 0 || d.Used != 8<<30 {
		t.Errorf("darwin states = %+v", d)
	}
	w := ComputeMemory(NativeMemoryInfo("windows", vm, nil))
	if w.Free != 10<<30 || w.Cached != 0 || w.Used != 6<<30 {
		t.Errorf("windows states = %+v", w)
	}
	ms := MemoryMetrics(NativeMemoryInfo("darwin", vm, sw), time.Now())
	names := map[string]bool{}
	for _, m := range ms {
		names[m.Name] = true
	}
	for _, n := range []string{"system.memory.usage", "system.memory.limit", "system.memory.utilization", "system.paging.usage"} {
		if !names[n] {
			t.Errorf("missing %s", n)
		}
	}
}

func TestNativeFilesystemKept(t *testing.T) {
	for _, c := range []struct {
		mp, fs string
		want   bool
	}{
		{"/", "apfs", true},
		{"/System/Volumes/Data", "apfs", true},
		{"/System/Volumes/VM", "apfs", false},
		{"/System/Volumes/Preboot", "apfs", false},
		{"/dev", "devfs", false},
		{"/Volumes/Backup", "hfs", true},
		{`C:\`, "NTFS", true},
		{"/private/tmp/x", "tmpfs", false},
	} {
		if got := NativeFilesystemKept(c.mp, c.fs); got != c.want {
			t.Errorf("NativeFilesystemKept(%q, %q) = %v", c.mp, c.fs, got)
		}
	}
	for name, want := range map[string]bool{"lo0": true, "Loopback Pseudo-Interface 1": true, "en0": false, "Ethernet": false} {
		if NativeLoopback(name) != want {
			t.Errorf("NativeLoopback(%q) != %v", name, want)
		}
	}
}

func TestNativeCollectorsProduceHostMetrics(t *testing.T) {
	cs, top := nativeCollectors(config.Default())
	if top == nil || len(cs) < 7 {
		t.Fatalf("collectors = %d, top = %v", len(cs), top)
	}
	now := time.Now()
	names := map[string]bool{}
	// Utilization needs CPU counters that moved between two rounds; they advance in scheduler ticks, so a busy
	// or idle host can need more than one extra round.
	for round := 0; round < 2 || (round < 8 && !names["system.cpu.utilization"]); round++ {
		if round > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		for _, c := range cs {
			ms, err := c.Collect(now.Add(time.Duration(round) * time.Second))
			if err != nil && c.Name() != "disk" { // disk counters can be unavailable (Windows without diskperf)
				t.Errorf("%s: %v", c.Name(), err)
			}
			for _, m := range ms {
				names[m.Name] = true
			}
		}
	}
	for _, n := range []string{"system.cpu.time", "system.cpu.utilization", "system.memory.usage", "system.filesystem.usage",
		"system.network.io", "system.uptime", "system.process.count", "process.memory.usage"} {
		if !names[n] {
			t.Errorf("metric %s not produced", n)
		}
	}
}

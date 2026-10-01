package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/onuragtas/openlog/internal/diskspace"
)

func disk(usedPercent uint64) diskspace.Disk {
	return diskspace.Disk{Host: "clickhouse", Name: "default", Total: 100, Free: 100 - usedPercent}
}

func onePlan() []diskspace.Drop {
	return []diskspace.Drop{
		{Unit: "traces", Partition: "20260901", Tables: []string{"spans_local", "trace_index_local"}, Bytes: 1 << 30},
	}
}

func render(d diskspace.Disk, eff diskspace.Effective, plan, done []diskspace.Drop, applied bool) string {
	var b bytes.Buffer
	printClean(&b, d, eff, "stored settings", plan, done, applied, false)
	return b.String()
}

func TestAPreviewSaysPlainlyThatNothingWasDeleted(t *testing.T) {
	out := render(disk(95), diskspace.Defaults(), onePlan(), nil, false)
	for _, want := range []string{"nothing was deleted", "--apply"} {
		if !strings.Contains(out, want) {
			t.Errorf("preview did not say %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "cannot be undone") {
		t.Errorf("a preview claimed something irreversible happened:\n%s", out)
	}
}

func TestAPreviewSaysSheddingIsStillOff(t *testing.T) {
	// The question this command answers while shedding is off is "what would it take from me", so the answer has
	// to say that it is off; otherwise the table reads as a list of things already gone.
	out := render(disk(95), diskspace.Defaults(), onePlan(), nil, false)
	if !strings.Contains(out, "shedding is off") {
		t.Errorf("preview did not mention that shedding is off:\n%s", out)
	}
}

func TestApplyingSaysWhatWentAndThatItIsFinal(t *testing.T) {
	plan := onePlan()
	out := render(disk(95), diskspace.Defaults(), plan, plan, true)
	for _, want := range []string{"deleted 1 of 1", "cannot be undone"} {
		if !strings.Contains(out, want) {
			t.Errorf("applied output did not say %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "--apply") {
		t.Errorf("applied output still invited --apply:\n%s", out)
	}
}

func TestBelowTheStartLevelIsNotTheSameAsNothingToDrop(t *testing.T) {
	// Two very different situations, and an operator at 95 % needs to know which one they are in.
	calm := render(disk(50), diskspace.Defaults(), nil, nil, false)
	if !strings.Contains(calm, "below the start level") {
		t.Errorf("a disk under the level was not reported as such:\n%s", calm)
	}
	stuck := render(disk(95), diskspace.Defaults(), nil, nil, false)
	if !strings.Contains(stuck, "nothing can be given up") {
		t.Errorf("a full disk with nothing droppable was not reported as such:\n%s", stuck)
	}
}

func TestTheFullestDiskAndItsLevelsAreShown(t *testing.T) {
	out := render(disk(81), diskspace.Defaults(), nil, nil, false)
	for _, want := range []string{"clickhouse/default", "81% used", "start 90%", "stop 85%"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
}

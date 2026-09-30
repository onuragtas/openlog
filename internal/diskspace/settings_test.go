package diskspace

import (
	"fmt"
	"testing"
)

func ptr(v int) *int { return &v }

func TestUnsetFieldsFallBackToTheBuiltInLevels(t *testing.T) {
	e := Settings{HighPercent: ptr(95)}.Resolve()
	if e.Warn != DefaultWarnPercent || e.High != 95 || e.Hysteresis != DefaultHysteresis {
		t.Fatalf("resolved = %+v, want warn %d, high 95, hysteresis %d", e, DefaultWarnPercent, DefaultHysteresis)
	}
}

func TestEmptyIsHowAClearedSettingIsTold(t *testing.T) {
	if !(Settings{}).Empty() {
		t.Fatal("no field set did not read as empty")
	}
	// Zero is a value an operator may deliberately choose, so it must not read as "not set".
	if (Settings{Hysteresis: ptr(0)}).Empty() {
		t.Fatal("hysteresis 0 read as empty")
	}
}

func TestWarnMustStayBelowHigh(t *testing.T) {
	// Inverted levels would report the high level first and never the warning, which is the opposite of what the
	// operator asked for, so it is refused rather than silently reordered.
	for _, s := range []Settings{
		{WarnPercent: ptr(90), HighPercent: ptr(80)},
		{WarnPercent: ptr(85), HighPercent: ptr(85)},
		{WarnPercent: ptr(95)}, // against the default high of 90
	} {
		if err := s.Validate(); err == nil {
			t.Errorf("%+v was accepted", s)
		}
	}
}

func TestLevelsOutsideTheUsefulRangeAreRefused(t *testing.T) {
	for _, s := range []Settings{
		{WarnPercent: ptr(0)},
		{HighPercent: ptr(100)},
		{Hysteresis: ptr(-1)},
		{Hysteresis: ptr(51)},
	} {
		if err := s.Validate(); err == nil {
			t.Errorf("%+v was accepted", s)
		}
	}
}

func TestAPlausibleChangeIsAccepted(t *testing.T) {
	s := Settings{WarnPercent: ptr(70), HighPercent: ptr(85), Hysteresis: ptr(10)}
	if err := s.Validate(); err != nil {
		t.Fatalf("rejected: %v", err)
	}
	if e := s.Resolve(); e.Warn != 70 || e.High != 85 || e.Hysteresis != 10 {
		t.Fatalf("resolved = %+v", e)
	}
	if got := fmt.Sprint(s.Resolve().Thresholds()); got != "[70 85]" {
		t.Fatalf("thresholds = %s", got)
	}
}

package api

import (
	"math"
	"testing"
)

// The score is the only judgement this feature makes, so it is the thing that has to be right: a
// correlation a person cannot check is one they cannot act on.
func TestCorrelationScore(t *testing.T) {
	cases := []struct {
		name                         string
		windowMean, baseMean, stddev float64
		want                         func(score float64) bool
		describe                     string
	}{
		{
			name:       "a clear move against a quiet baseline scores high",
			windowMean: 200, baseMean: 100, stddev: 5,
			want:     func(s float64) bool { return s > 15 },
			describe: "100 above a baseline that moves by 5 is twenty standard deviations",
		},
		{
			name:       "the same move against a noisy baseline scores low",
			windowMean: 200, baseMean: 100, stddev: 100,
			want:     func(s float64) bool { return s < 2 },
			describe: "a series that always swings by 100 has not done anything new",
		},
		{
			name:       "no change scores zero and is dropped",
			windowMean: 100, baseMean: 100, stddev: 5,
			want:     func(s float64) bool { return s == 0 },
			describe: "nothing happened here is not a correlation",
		},
		{
			name:       "a flat baseline nudged by a rounding error does not win",
			windowMean: 100.01, baseMean: 100, stddev: 0,
			// The floor is one percent of the baseline, so 0.01 out of 100 scores 0.01 — not infinity.
			want:     func(s float64) bool { return s < 1 },
			describe: "a perfectly flat series must not out-score one that doubled",
		},
		{
			name:       "a flat baseline that actually moved still scores",
			windowMean: 500, baseMean: 100, stddev: 0,
			want:     func(s float64) bool { return s > 100 },
			describe: "five times the value with no prior variation is the strongest kind of signal",
		},
		{
			name:       "a zero baseline that starts reporting is scored by its magnitude",
			windowMean: 42, baseMean: 0, stddev: 0,
			want:     func(s float64) bool { return s > 0 && !math.IsInf(s, 0) },
			describe: "a rate that was zero and is not any more cannot be expressed in its own deviations",
		},
		{
			name:       "a move downwards scores like a move upwards",
			windowMean: 10, baseMean: 100, stddev: 5,
			want:     func(s float64) bool { return s > 15 },
			describe: "a request rate falling off a cliff is as interesting as one spiking",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := correlationScore(tc.windowMean, tc.baseMean, tc.stddev)
			if math.IsNaN(got) || math.IsInf(got, 0) {
				t.Fatalf("score = %v; a score that is not a number cannot be ranked", got)
			}
			if !tc.want(got) {
				t.Errorf("score = %v: %s", got, tc.describe)
			}
		})
	}
}

// Ranking has to be stable and finite whatever the data, because it decides what a person reads first.
func TestCorrelationScoreIsOrdered(t *testing.T) {
	strong := correlationScore(200, 100, 5)
	weak := correlationScore(105, 100, 5)
	if !(strong > weak) {
		t.Fatalf("strong = %v, weak = %v; the bigger move must rank higher", strong, weak)
	}
	// Symmetry: the same distance in either direction is the same strength of signal.
	up := correlationScore(150, 100, 10)
	down := correlationScore(50, 100, 10)
	if math.Abs(up-down) > 1e-9 {
		t.Errorf("up = %v, down = %v; direction is reported separately from strength", up, down)
	}
}

package tailsampling

import (
	"math"
	"testing"

	"github.com/onuragtas/openlog/internal/apm"
)

func TestThresholdRoundTripWithProcessorWeight(t *testing.T) {
	for _, p := range []float64{1, 0.5, 0.25, 0.1, 0.125 * 0.3, 1e-3, 1e-6} {
		th := EncodeThreshold(Threshold(p))
		w := apm.SampleWeight("ot=th:"+th, nil, nil)
		if math.Abs(w*p-1) > 1e-9 {
			t.Errorf("p=%v th=%s: processor weight %v, want %v", p, th, w, 1/p)
		}
	}
	if EncodeThreshold(Threshold(0.5)) != "8" || EncodeThreshold(0) != "0" {
		t.Errorf("unexpected encodings %q %q", EncodeThreshold(Threshold(0.5)), EncodeThreshold(0))
	}
}

func TestWithThreshold(t *testing.T) {
	cases := map[string]string{
		"":                         "ot=th:4",
		"ot=th:8":                  "ot=th:4",
		"ot=p:1;rv:0123456789abcd": "ot=th:4;rv:0123456789abcd",
		"vendor=a,ot=th:8;x:1,b=2": "ot=th:4;x:1,vendor=a,b=2",
	}
	for in, want := range cases {
		if got := WithThreshold(in, "4"); got != want {
			t.Errorf("WithThreshold(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTraceRandomness(t *testing.T) {
	id := []byte{0, 1, 2, 3, 4, 5, 6, 7, 0xff, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77}
	if got := traceRandomness(id, ""); got != 0x11223344556677 {
		t.Errorf("randomness from id = %x", got)
	}
	if got := traceRandomness(id, "ot=th:8;rv:00000000000001"); got != 1 {
		t.Errorf("explicit rv = %x", got)
	}
}

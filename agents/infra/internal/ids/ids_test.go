package ids

import (
	"regexp"
	"testing"
	"time"
)

func TestUUIDs(t *testing.T) {
	v7 := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	v4 := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	a := NewV7(time.UnixMilli(1000))
	b := NewV7(time.UnixMilli(2000))
	if !v7.MatchString(a) || !v7.MatchString(b) {
		t.Fatalf("bad v7: %s %s", a, b)
	}
	if a >= b {
		t.Errorf("v7 not time ordered: %s >= %s", a, b)
	}
	if id := NewV4(); !v4.MatchString(id) {
		t.Errorf("bad v4: %s", id)
	}
}

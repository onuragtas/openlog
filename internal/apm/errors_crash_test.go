package apm

import "testing"

// An obfuscated frame changes with every build, so a crash must not group on it (mobile-crash.md §4), and an ordinary
// error still must.
func TestCrashGroupIgnoresTheFrame(t *testing.T) {
	group := func(stack string, attrs map[string]string) uint64 {
		_, _, id := ErrorGroup("app", "", "", &Input{Attributes: attrs, EventsName: []string{"exception"}, EventsAttributes: []map[string]string{{
			"exception.type": "IllegalState", "exception.message": "boom", "exception.stacktrace": stack,
		}}}, 0)
		return id
	}
	crash := map[string]string{crashKindAttr: "uncaught_exception"}
	a, b := "IllegalState: boom\n\tat a.b.c(SourceFile:1)", "IllegalState: boom\n\tat x.y.z(SourceFile:1)"
	if group(a, crash) != group(b, crash) {
		t.Error("two builds of one crash landed in different groups")
	}
	if group(a, nil) == group(b, nil) {
		t.Error("ordinary errors lost their frame")
	}
}

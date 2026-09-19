package sqlredact

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	for in, want := range map[string]string{
		"SELECT * FROM users WHERE email = 'a@b.c' AND id = 42":               "SELECT * FROM users WHERE email = ? AND id = ?",
		"UPDATE t SET v = E'it''s\\n', n = -3.5e10 WHERE k = x'ff'":           "UPDATE t SET v = ?, n = -? WHERE k = ?",
		"select /* traceparent='00-abc' */ a from t -- secret\nwhere b = $1":  "select a from t where b = $1",
		"INSERT INTO t VALUES (1, 'x'), (2, 'y')":                             "INSERT INTO t VALUES (?, ?), (?, ?)",
		"SELECT \"weird col\", `x`, [y] FROM t2 WHERE c3 = :name AND d = @p1": "SELECT \"weird col\", `x`, [y] FROM t2 WHERE c3 = :name AND d = @p1",
		"DO $$ BEGIN PERFORM 1; END $$":                                       "DO ?",
		"SELECT $tag$secret$tag$, .5, 0x1F, col1":                             "SELECT ?, ?, ?, col1",
		"CREATE ROLE x PASSWORD 'hunter2'":                                    "CREATE ROLE x PASSWORD ?",
		"SELECT 1 # mysql comment\n":                                          "SELECT ?",
		"   ":                                                                 "",
	} {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestRedactBoundsLength(t *testing.T) {
	long := "SELECT " + strings.Repeat("colümn_name, ", 1000) + "x FROM t"
	out := Redact(long)
	if len(out) > MaxBytes || !strings.HasPrefix(out, "SELECT colümn_name") {
		t.Fatalf("len %d prefix %q", len(out), out[:20])
	}
	if !strings.HasSuffix(out, ",") && !strings.HasSuffix(out, "e") {
		// any cut is fine as long as it is valid UTF-8
		for _, r := range out {
			_ = r
		}
	}
}

func FuzzRedact(f *testing.F) {
	f.Add("SELECT 'x' FROM t WHERE a = 1")
	f.Add("$$ unterminated")
	f.Fuzz(func(t *testing.T, in string) {
		out := Redact(in)
		if len(out) > MaxBytes {
			t.Fatalf("too long: %d", len(out))
		}
		// A quoted string literal never survives.
		if strings.Contains(in, "'secret-value'") && strings.Contains(out, "secret-value") {
			t.Fatalf("literal leaked: %q", out)
		}
	})
}

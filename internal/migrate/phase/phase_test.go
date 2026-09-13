package phase

import (
	"errors"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	ok := []struct {
		sql  string
		want Header
	}{
		{"-- openlog:phase expand\nCREATE TABLE x (a int);", Header{Phase: Expand}},
		{"-- openlog:phase expand\n-- 0001_init: comment\n\nCREATE TABLE x (a int);", Header{Phase: Expand}},
		{"--openlog:phase contract\n-- why we drop it\n-- openlog:requires-all-at-least v0.9.1\nALTER TABLE x DROP COLUMN a;", Header{Phase: Contract, RequiresAllAtLeast: "0.9.1"}},
		{"\uFEFF-- openlog:phase expand\n", Header{Phase: Expand}},
	}
	for _, c := range ok {
		got, err := Parse(c.sql)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.sql, err)
			continue
		}
		if got != c.want {
			t.Errorf("Parse(%q) = %+v, want %+v", c.sql, got, c.want)
		}
	}
	bad := map[string]string{
		"CREATE TABLE x (a int);":                                              "missing header",
		"-- some comment\n-- openlog:phase expand\n":                           "must be the first line",
		"-- openlog:phase shrink\n":                                            "unknown phase",
		"-- openlog:phase contract\nDROP TABLE x;":                             "must declare",
		"-- openlog:phase expand\n-- openlog:requires-all-at-least 0.9.0\n":    "only allowed on contract",
		"-- openlog:phase contract\n-- openlog:requires-all-at-least banana\n": "version",
		"-- openlog:phase expand\n-- openlog:typo x\n":                         "unknown directive",
		"-- openlog:phase contract\n-- openlog:requires-all-at-least 1.0.0\n-- openlog:requires-all-at-least 1.0.1\n": "duplicate",
		"": "missing header",
	}
	for sql, want := range bad {
		if _, err := Parse(sql); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%q) error = %v, want containing %q", sql, err, want)
		}
	}
}

func TestDecide(t *testing.T) {
	contract := Header{Phase: Contract, RequiresAllAtLeast: "0.9.1"}
	inst := func(v string) Instance { return Instance{Component: "openlog-api", InstanceID: "api-1", Version: v} }
	cases := []struct {
		name  string
		gate  *Gate
		h     Header
		apply bool
		why   string
	}{
		{"expand always", nil, Header{Phase: Expand}, true, "expand"},
		{"nil gate skips contract", nil, contract, false, "not enabled"},
		{"old instance", &Gate{Self: "0.9.1", Known: true, Instances: []Instance{inst("0.9.1"), inst("0.9.0")}}, contract, false, "runs 0.9.0 < 0.9.1"},
		{"all new", &Gate{Self: "0.9.2", Known: true, Instances: []Instance{inst("0.9.1"), inst("0.10.0")}}, contract, true, "all 2 live"},
		{"no instances", &Gate{Self: "0.9.1", Known: true}, contract, true, "all 0 live"},
		{"unknown heartbeats", &Gate{Self: "0.9.1", Err: errors.New("relation does not exist")}, contract, false, "unknown: relation"},
		{"dev instance", &Gate{Self: "0.9.1", Known: true, Instances: []Instance{inst("0.0.0-dev")}}, contract, false, "0.0.0-dev < 0.9.1"},
		{"prerelease below", &Gate{Self: "0.9.1", Known: true, Instances: []Instance{inst("0.9.1-beta.1")}}, contract, false, "< 0.9.1"},
		{"garbage version", &Gate{Self: "0.9.1", Known: true, Instances: []Instance{inst("latest")}}, contract, false, "unparseable"},
		{"old migrator", &Gate{Self: "0.9.0", Known: true}, contract, false, "this binary is 0.9.0"},
		{"force", &Gate{Self: "0.0.0-dev", Force: true}, contract, true, "forced"},
		{"build metadata ignored", &Gate{Self: "0.9.1+abc", Known: true, Instances: []Instance{inst("0.9.1+def")}}, contract, true, "all 1"},
	}
	for _, c := range cases {
		d := c.gate.Decide(c.h)
		if d.Apply != c.apply || !strings.Contains(d.Reason, c.why) {
			t.Errorf("%s: got %+v, want apply=%v reason containing %q", c.name, d, c.apply, c.why)
		}
	}
}

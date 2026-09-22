package version

import "testing"

func TestCurrent(t *testing.T) {
	oldV, oldC := Version, Commit
	defer func() { Version, Commit = oldV, oldC }()
	for _, c := range []struct{ version, commit, want string }{
		{"0.4.0", "abc", "0.4.0"},
		{"v0.5.0-beta.1", "abc", "0.5.0-beta.1"},
		{DevVersion, "abc123", "0.0.0-dev+abc123"},
		{DevVersion, "", "0.0.0-dev+unknown"},
		{"", "a/b_c", "0.0.0-dev+abc"},
	} {
		Version, Commit = c.version, c.commit
		if got := Current(); got != c.want {
			t.Errorf("Current(%q, %q) = %q, want %q", c.version, c.commit, got, c.want)
		}
	}
}

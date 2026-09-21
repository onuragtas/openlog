package vuln

import "testing"

// The cases are the ones the schemes themselves are specified by: deb-version(7)'s examples, rpm's
// rpmvercmp test suite, Alpine's apk version tests and the SemVer precedence list. Getting these wrong is
// how a vulnerability feature turns into noise — either a patched package keeps being reported, or a
// vulnerable one never is.
func TestCompare(t *testing.T) {
	cases := []struct {
		scheme, a, b string
		want         int
	}{
		// dpkg
		{SchemeDpkg, "1.0", "1.0", 0},
		{SchemeDpkg, "1.0", "1.1", -1},
		{SchemeDpkg, "1.10", "1.9", 1},
		{SchemeDpkg, "1.0-1", "1.0-2", -1},
		{SchemeDpkg, "1.0", "1.0-0", 0},    // a missing revision is "0"
		{SchemeDpkg, "1.0~rc1", "1.0", -1}, // "~" sorts before the end of the string
		{SchemeDpkg, "1.0~rc1", "1.0~rc2", -1},
		{SchemeDpkg, "1:1.0", "2.0", 1}, // the epoch wins over everything
		{SchemeDpkg, "1.2.3-4ubuntu0.2", "1.2.3-4ubuntu0.3", -1},
		{SchemeDpkg, "2.4.52-1ubuntu4.7", "2.4.52-1ubuntu4.10", -1}, // digit runs are numeric, not lexical
		{SchemeDpkg, "1.0+dfsg-1", "1.0-1", 1},                      // "+" sorts after the empty string
		{SchemeDpkg, "0", "", 0},                                    // an empty numeric part is 0, as in dpkg

		// rpm
		{SchemeRPM, "1.0", "1.0", 0},
		{SchemeRPM, "1.0-1", "1.0-2", -1},
		{SchemeRPM, "1.0", "1.0.1", -1},
		{SchemeRPM, "1.0.1", "1.0.1a", -1}, // an alpha run after digits is newer
		{SchemeRPM, "2a", "2.0", -1},       // a digit run beats an alpha run
		{SchemeRPM, "1.0~rc1", "1.0", -1},
		{SchemeRPM, "1.0", "1.0^", -1}, // "^" is newer than the end of the string
		{SchemeRPM, "1:1.0-1", "2.0-1", 1},
		{SchemeRPM, "1.2.3-4.el9_2.1", "1.2.3-4.el9_2.2", -1},
		{SchemeRPM, "5.14.0-284.11.1.el9_2", "5.14.0-284.9.1.el9_2", 1},

		// apk
		{SchemeAPK, "1.0", "1.0", 0},
		{SchemeAPK, "1.0-r0", "1.0-r1", -1},
		{SchemeAPK, "1.0_alpha1", "1.0", -1},
		{SchemeAPK, "1.0_alpha1", "1.0_beta1", -1},
		{SchemeAPK, "1.0_rc1", "1.0_p1", -1}, // a post-release is newer than the release
		{SchemeAPK, "1.2.3-r10", "1.2.3-r9", 1},
		{SchemeAPK, "3.1.4", "3.1.4a", -1},

		// semver
		{SchemeSemVer, "1.2.3", "1.2.3", 0},
		{SchemeSemVer, "v1.2.3", "1.2.3", 0},
		{SchemeSemVer, "1.2.3", "1.10.0", -1},
		{SchemeSemVer, "1.0.0-alpha", "1.0.0", -1},
		{SchemeSemVer, "1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{SchemeSemVer, "1.0.0-rc.1", "1.0.0-rc.2", -1},
		{SchemeSemVer, "1.0.0+build1", "1.0.0+build2", 0}, // build metadata is not precedence
	}
	for _, tc := range cases {
		t.Run(tc.scheme+" "+tc.a+" vs "+tc.b, func(t *testing.T) {
			if got := Compare(tc.scheme, tc.a, tc.b); got != tc.want {
				t.Errorf("Compare(%q, %q, %q) = %d, want %d", tc.scheme, tc.a, tc.b, got, tc.want)
			}
			// Every comparison must be antisymmetric, or a range check would accept and reject the same
			// version depending on which side it was written on.
			if got := Compare(tc.scheme, tc.b, tc.a); got != -tc.want {
				t.Errorf("Compare(%q, %q, %q) = %d, want %d (antisymmetry)", tc.scheme, tc.b, tc.a, got, -tc.want)
			}
		})
	}
}

func TestSchemeFor(t *testing.T) {
	cases := map[string]string{
		"dpkg": SchemeDpkg, "apt": SchemeDpkg, "rpm": SchemeRPM, "dnf": SchemeRPM, "zypper": SchemeRPM,
		"apk": SchemeAPK, "npm": SchemeSemVer, "": SchemeSemVer, "DPKG": SchemeDpkg,
	}
	for manager, want := range cases {
		if got := SchemeFor(manager); got != want {
			t.Errorf("SchemeFor(%q) = %q, want %q", manager, got, want)
		}
	}
}

// A long version number must not overflow into a wrong answer.
func TestCompareLongDigitRuns(t *testing.T) {
	a := "1.99999999999999999999999999"
	b := "1.99999999999999999999999998"
	if got := Compare(SchemeDpkg, a, b); got != 1 {
		t.Errorf("Compare(%q, %q) = %d, want 1", a, b, got)
	}
}

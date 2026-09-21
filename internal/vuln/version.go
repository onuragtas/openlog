package vuln

import (
	"strings"
)

// Package version comparison, per ecosystem. This is the part that decides whether the whole feature is
// useful or noise: "is 1.2.3-4ubuntu0.2 older than 1.2.3-4ubuntu0.3" cannot be answered by comparing
// strings, and answering it wrongly either hides a real vulnerability or reports one that was patched.
//
// Three schemes cover what the inventory reports (agents/infra/internal/inventory/packages.go):
//
//   - dpkg (Debian, Ubuntu): epoch:upstream-revision, compared the way dpkg --compare-versions does,
//     including the rule that makes "~" sort before the empty string so 1.0~rc1 < 1.0.
//   - rpm (RHEL, Rocky, Fedora, SUSE): epoch:version-release, rpmvercmp's alternating digit/alpha runs,
//     where a digit run always beats an alpha run and "~" again sorts lowest.
//   - apk (Alpine): the same shape with a letter suffix and _alpha/_beta/_rc pre-release parts.
//
// Everything else (a language ecosystem, an unknown manager) is compared as SemVer, which is what OSV means
// by an ECOSYSTEM range there.

// Ecosystems openlog knows how to compare versions in.
const (
	SchemeDpkg   = "dpkg"
	SchemeRPM    = "rpm"
	SchemeAPK    = "apk"
	SchemeSemVer = "semver"
)

// SchemeFor maps a package manager as the inventory reports it to the comparison scheme.
func SchemeFor(manager string) string {
	switch strings.ToLower(manager) {
	case "dpkg", "apt", "deb":
		return SchemeDpkg
	case "rpm", "dnf", "yum", "zypper":
		return SchemeRPM
	case "apk":
		return SchemeAPK
	}
	return SchemeSemVer
}

// Compare returns -1, 0 or 1 for a < b, a == b, a > b in the given scheme.
func Compare(scheme, a, b string) int {
	switch scheme {
	case SchemeDpkg:
		return compareDpkg(a, b)
	case SchemeRPM:
		return compareRPM(a, b)
	case SchemeAPK:
		return compareAPK(a, b)
	}
	return compareSemVer(a, b)
}

// ---- dpkg ----

// compareDpkg implements the algorithm of deb-version(7): epoch, then upstream version, then revision, each
// compared by alternating non-digit and digit parts.
func compareDpkg(a, b string) int {
	ea, ua, ra := splitDpkg(a)
	eb, ub, rb := splitDpkg(b)
	if c := compareInts(ea, eb); c != 0 {
		return c
	}
	if c := compareDebianPart(ua, ub); c != 0 {
		return c
	}
	return compareDebianPart(ra, rb)
}

// splitDpkg splits [epoch:]upstream[-revision]. A missing epoch is 0 and a missing revision is "0", which is
// what dpkg does, so 1.0 and 1.0-0 are the same version.
func splitDpkg(v string) (epoch int, upstream, revision string) {
	v = strings.TrimSpace(v)
	if at := strings.Index(v, ":"); at >= 0 {
		epoch = atoiSafe(v[:at])
		v = v[at+1:]
	}
	if at := strings.LastIndex(v, "-"); at >= 0 {
		return epoch, v[:at], v[at+1:]
	}
	return epoch, v, "0"
}

// debianOrder is the character order of deb-version(7): "~" sorts before everything including the end of the
// string, letters sort before the other characters, and everything is compared by these values.
func debianOrder(c byte) int {
	switch {
	case c == '~':
		return -1
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		return int(c)
	case c == 0:
		return 0
	}
	return int(c) + 256
}

func compareDebianPart(a, b string) int {
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		// Non-digit run, compared character by character in the Debian order.
		for (i < len(a) && !isDigit(a[i])) || (j < len(b) && !isDigit(b[j])) {
			var ca, cb byte
			if i < len(a) && !isDigit(a[i]) {
				ca = a[i]
				i++
			}
			if j < len(b) && !isDigit(b[j]) {
				cb = b[j]
				j++
			}
			if d := debianOrder(ca) - debianOrder(cb); d != 0 {
				return sign(d)
			}
		}
		// Digit run, compared numerically with leading zeroes ignored.
		si, sj := i, j
		for i < len(a) && isDigit(a[i]) {
			i++
		}
		for j < len(b) && isDigit(b[j]) {
			j++
		}
		if c := compareDigitRuns(a[si:i], b[sj:j]); c != 0 {
			return c
		}
	}
	return 0
}

// ---- rpm ----

// compareRPM implements rpmvercmp plus the epoch and release split of an NEVRA version.
func compareRPM(a, b string) int {
	ea, va, ra := splitRPM(a)
	eb, vb, rb := splitRPM(b)
	if c := compareInts(ea, eb); c != 0 {
		return c
	}
	if c := rpmvercmp(va, vb); c != 0 {
		return c
	}
	return rpmvercmp(ra, rb)
}

func splitRPM(v string) (epoch int, version, release string) {
	v = strings.TrimSpace(v)
	if at := strings.Index(v, ":"); at >= 0 {
		epoch = atoiSafe(v[:at])
		v = v[at+1:]
	}
	if at := strings.Index(v, "-"); at >= 0 {
		return epoch, v[:at], v[at+1:]
	}
	return epoch, v, ""
}

// rpmvercmp compares two version strings the way rpm does: runs of digits and runs of letters are compared
// in turn, a digit run is newer than a letter run, everything else is a separator, and "~" sorts lowest so a
// pre-release is older than the release it precedes.
func rpmvercmp(a, b string) int {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		// Separators are skipped in both, but only together: a version that still has characters when the
		// other ran out is the newer one.
		for i < len(a) && !isAlnum(a[i]) && a[i] != '~' && a[i] != '^' {
			i++
		}
		for j < len(b) && !isAlnum(b[j]) && b[j] != '~' && b[j] != '^' {
			j++
		}
		// "~" is older than anything, including the end of the string.
		if (i < len(a) && a[i] == '~') || (j < len(b) && b[j] == '~') {
			switch {
			case i >= len(a) || a[i] != '~':
				return 1
			case j >= len(b) || b[j] != '~':
				return -1
			}
			i++
			j++
			continue
		}
		// "^" is newer than the end of the string but older than anything else.
		if (i < len(a) && a[i] == '^') || (j < len(b) && b[j] == '^') {
			switch {
			case i >= len(a):
				return -1
			case j >= len(b):
				return 1
			case a[i] != '^':
				return 1
			case b[j] != '^':
				return -1
			}
			i++
			j++
			continue
		}
		if i >= len(a) || j >= len(b) {
			break
		}
		si, sj := i, j
		isNum := isDigit(a[i])
		if isNum {
			for i < len(a) && isDigit(a[i]) {
				i++
			}
			for j < len(b) && isDigit(b[j]) {
				j++
			}
		} else {
			for i < len(a) && isAlpha(a[i]) {
				i++
			}
			for j < len(b) && isAlpha(b[j]) {
				j++
			}
		}
		if si == i {
			return -1 // a has a letter run where b has digits: digits win
		}
		if sj == j {
			if isNum {
				return 1
			}
			return -1
		}
		if isNum {
			if c := compareDigitRuns(a[si:i], b[sj:j]); c != 0 {
				return c
			}
			continue
		}
		if c := strings.Compare(a[si:i], b[sj:j]); c != 0 {
			return c
		}
	}
	switch {
	case i >= len(a) && j >= len(b):
		return 0
	case i >= len(a):
		// What is left in b decides: a trailing "~" still makes b the older one.
		if j < len(b) && b[j] == '~' {
			return 1
		}
		return -1
	}
	if i < len(a) && a[i] == '~' {
		return -1
	}
	return 1
}

// ---- apk ----

// apkSuffixOrder is the order of Alpine's pre-release and post-release suffixes.
var apkSuffixOrder = map[string]int{"alpha": 0, "beta": 1, "pre": 2, "rc": 3, "": 4, "cvs": 5, "svn": 6, "git": 7, "hg": 8, "p": 9}

// compareAPK compares Alpine versions: numeric parts, an optional single letter, then _suffix parts, then
// the -rN package release.
func compareAPK(a, b string) int {
	va, sa, ra := splitAPK(a)
	vb, sb, rb := splitAPK(b)
	if c := compareDottedNumbers(va, vb); c != 0 {
		return c
	}
	if c := compareAPKSuffix(sa, sb); c != 0 {
		return c
	}
	return compareInts(ra, rb)
}

// splitAPK splits version[_suffix…][-rN].
func splitAPK(v string) (version, suffix string, release int) {
	v = strings.TrimSpace(v)
	if at := strings.LastIndex(v, "-r"); at >= 0 {
		release = atoiSafe(v[at+2:])
		v = v[:at]
	}
	if at := strings.Index(v, "_"); at >= 0 {
		return v[:at], v[at+1:], release
	}
	return v, "", release
}

func compareAPKSuffix(a, b string) int {
	na, va := splitAPKSuffix(a)
	nb, vb := splitAPKSuffix(b)
	oa, ok := apkSuffixOrder[na]
	if !ok {
		oa = apkSuffixOrder[""]
	}
	ob, ok := apkSuffixOrder[nb]
	if !ok {
		ob = apkSuffixOrder[""]
	}
	if c := compareInts(oa, ob); c != 0 {
		return c
	}
	return compareInts(va, vb)
}

func splitAPKSuffix(s string) (name string, number int) {
	i := 0
	for i < len(s) && isAlpha(s[i]) {
		i++
	}
	return s[:i], atoiSafe(s[i:])
}

// ---- semver and shared helpers ----

// compareSemVer compares dotted numbers and then the pre-release, which is enough for the ECOSYSTEM ranges
// of the language ecosystems: a pre-release is older than the release it precedes.
func compareSemVer(a, b string) int {
	a, b = strings.TrimPrefix(strings.TrimSpace(a), "v"), strings.TrimPrefix(strings.TrimSpace(b), "v")
	ca, pa := cutAt(a, '-')
	cb, pb := cutAt(b, '-')
	// Build metadata never affects precedence.
	ca, _ = cutAt(ca, '+')
	cb, _ = cutAt(cb, '+')
	pa, _ = cutAt(pa, '+')
	pb, _ = cutAt(pb, '+')
	if c := compareDottedNumbers(ca, cb); c != 0 {
		return c
	}
	switch {
	case pa == "" && pb == "":
		return 0
	case pa == "":
		return 1 // a release is newer than any of its pre-releases
	case pb == "":
		return -1
	}
	return comparePreRelease(pa, pb)
}

func comparePreRelease(a, b string) int {
	fa, fb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(fa) && i < len(fb); i++ {
		na, oka := allDigits(fa[i])
		nb, okb := allDigits(fb[i])
		switch {
		case oka && okb:
			if c := compareInts(na, nb); c != 0 {
				return c
			}
		case oka:
			return -1 // numeric identifiers are lower than alphanumeric ones
		case okb:
			return 1
		default:
			if c := strings.Compare(fa[i], fb[i]); c != 0 {
				return c
			}
		}
	}
	return compareInts(len(fa), len(fb))
}

func compareDottedNumbers(a, b string) int {
	fa, fb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(fa) || i < len(fb); i++ {
		var x, y string
		if i < len(fa) {
			x = fa[i]
		}
		if i < len(fb) {
			y = fb[i]
		}
		// A trailing letter ("1.2.3a") sorts after the same number without one.
		nx, lx := splitTrailingLetters(x)
		ny, ly := splitTrailingLetters(y)
		if c := compareDigitRuns(nx, ny); c != 0 {
			return c
		}
		if c := strings.Compare(lx, ly); c != 0 {
			return c
		}
	}
	return 0
}

func splitTrailingLetters(s string) (digits, letters string) {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return s[:i], s[i:]
}

// compareDigitRuns compares two runs of digits numerically without converting them, so a version number
// longer than an int64 still compares correctly.
func compareDigitRuns(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return sign(len(a) - len(b))
	}
	return strings.Compare(a, b)
}

func compareInts(a, b int) int { return sign(a - b) }

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isAlpha(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isAlnum(c byte) bool { return isDigit(c) || isAlpha(c) }

func atoiSafe(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			break
		}
		n = n*10 + int(s[i]-'0')
		if n > 1<<30 {
			return 1 << 30
		}
	}
	return n
}

func allDigits(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			return 0, false
		}
	}
	return atoiSafe(s), true
}

func cutAt(s string, sep byte) (before, after string) {
	if at := strings.IndexByte(s, sep); at >= 0 {
		return s[:at], s[at+1:]
	}
	return s, ""
}

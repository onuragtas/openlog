// Package release implements openlog's release primitives shared by agents, the backend and the
// release tooling: SemVer handling, release manifests and indexes, and their Ed25519 signatures.
//
// The contract is docs/contracts/releases-updates.md. This package is Apache-2.0 licensed so that
// agents may depend on it.
package release

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed SemVer 2.0 version. Build metadata is dropped because it does not take part
// in precedence.
type Version struct {
	Major, Minor, Patch uint64
	Pre                 []string
}

// ParseVersion parses a SemVer 2.0 version, with or without a leading "v".
func ParseVersion(s string) (Version, error) {
	in := s
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		if err := checkIdentifiers(s[i+1:], false); err != nil {
			return Version{}, fmt.Errorf("version %q: build metadata: %w", in, err)
		}
		s = s[:i]
	}
	var pre string
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre = s[i+1:]
		s = s[:i]
		if err := checkIdentifiers(pre, true); err != nil {
			return Version{}, fmt.Errorf("version %q: pre-release: %w", in, err)
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("version %q: want MAJOR.MINOR.PATCH", in)
	}
	nums := make([]uint64, 3)
	for i, p := range parts {
		if !isNumeric(p) || (len(p) > 1 && p[0] == '0') {
			return Version{}, fmt.Errorf("version %q: invalid number %q", in, p)
		}
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return Version{}, fmt.Errorf("version %q: %w", in, err)
		}
		nums[i] = n
	}
	v := Version{Major: nums[0], Minor: nums[1], Patch: nums[2]}
	if pre != "" {
		v.Pre = strings.Split(pre, ".")
	}
	return v, nil
}

// MustParseVersion is ParseVersion for constants; it panics on error.
func MustParseVersion(s string) Version {
	v, err := ParseVersion(s)
	if err != nil {
		panic(err)
	}
	return v
}

// String renders the version without a leading "v".
func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	return s
}

// IsPrerelease reports whether the version has pre-release identifiers.
func (v Version) IsPrerelease() bool { return len(v.Pre) > 0 }

// Compare returns -1, 0 or +1 following SemVer 2.0 precedence.
func Compare(a, b Version) int {
	for _, d := range [][2]uint64{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a.Pre) == 0 && len(b.Pre) == 0:
		return 0
	case len(a.Pre) == 0:
		return 1
	case len(b.Pre) == 0:
		return -1
	}
	for i := 0; i < len(a.Pre) && i < len(b.Pre); i++ {
		if c := compareIdentifier(a.Pre[i], b.Pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.Pre) < len(b.Pre):
		return -1
	case len(a.Pre) > len(b.Pre):
		return 1
	}
	return 0
}

// Less reports whether v precedes w.
func (v Version) Less(w Version) bool { return Compare(v, w) < 0 }

func compareIdentifier(a, b string) int {
	an, bn := isNumeric(a), isNumeric(b)
	switch {
	case an && bn:
		// Numeric identifiers have no leading zeros, so length then lexical order is numeric order.
		if len(a) != len(b) {
			if len(a) < len(b) {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	case an:
		return -1
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}

func checkIdentifiers(s string, noLeadingZeros bool) error {
	if s == "" {
		return fmt.Errorf("empty")
	}
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return fmt.Errorf("empty identifier")
		}
		for _, r := range id {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-') {
				return fmt.Errorf("invalid character %q", r)
			}
		}
		if noLeadingZeros && isNumeric(id) && len(id) > 1 && id[0] == '0' {
			return fmt.Errorf("numeric identifier %q has a leading zero", id)
		}
	}
	return nil
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

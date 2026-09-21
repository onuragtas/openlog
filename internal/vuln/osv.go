package vuln

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// The OSV format (https://ossf.github.io/osv-schema/), parsed to the fields openlog matches on. Everything
// else in the document is left alone: openlog is not a vulnerability database, it is the thing that knows
// which of your hosts is running the version in question.

// osvRecord is one advisory as the feed publishes it.
type osvRecord struct {
	ID         string        `json:"id"`
	Aliases    []string      `json:"aliases"`
	Summary    string        `json:"summary"`
	Details    string        `json:"details"`
	Modified   time.Time     `json:"modified"`
	Published  time.Time     `json:"published"`
	Withdrawn  *string       `json:"withdrawn"`
	Severity   []osvSeverity `json:"severity"`
	References []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"references"`
	Affected []struct {
		Package struct {
			Ecosystem string `json:"ecosystem"`
			Name      string `json:"name"`
		} `json:"package"`
		Ranges []struct {
			Type   string `json:"type"`
			Events []struct {
				Introduced   string `json:"introduced"`
				Fixed        string `json:"fixed"`
				LastAffected string `json:"last_affected"`
				Limit        string `json:"limit"`
			} `json:"events"`
		} `json:"ranges"`
		Versions []string      `json:"versions"`
		Severity []osvSeverity `json:"severity"`
	} `json:"affected"`
}

type osvSeverity struct {
	Type  string `json:"type"`
	Score string `json:"score"`
}

// MaxSummaryBytes bounds the stored summary; the details are what the advisory link is for.
const MaxSummaryBytes = 1024

// MaxDetailsBytes bounds the stored details.
const MaxDetailsBytes = 8192

// MaxReferences bounds the stored links.
const MaxReferences = 10

// ParseOSV turns one OSV document into an advisory. A document that names no package range produces no
// advisory, because there is nothing to match it against.
func ParseOSV(data []byte) (Vulnerability, bool) {
	var rec osvRecord
	if err := json.Unmarshal(data, &rec); err != nil || rec.ID == "" {
		return Vulnerability{}, false
	}
	v := Vulnerability{
		ID:        rec.ID,
		Aliases:   rec.Aliases,
		Summary:   truncate(strings.TrimSpace(rec.Summary), MaxSummaryBytes),
		Details:   truncate(strings.TrimSpace(rec.Details), MaxDetailsBytes),
		Published: rec.Published,
		Modified:  rec.Modified,
		Withdrawn: rec.Withdrawn != nil && *rec.Withdrawn != "",
	}
	v.Score, v.Vector = cvssOf(rec.Severity)
	for _, ref := range rec.References {
		if len(v.References) >= MaxReferences {
			break
		}
		if ref.URL != "" {
			v.References = append(v.References, ref.URL)
		}
	}
	for _, aff := range rec.Affected {
		ecosystem, name := aff.Package.Ecosystem, aff.Package.Name
		if ecosystem == "" || name == "" {
			continue
		}
		// A per-package severity overrides the advisory's own, which is how the distributions say "this is
		// critical for us even though upstream rated it lower".
		if score, vector := cvssOf(aff.Severity); score > 0 && v.Score == 0 {
			v.Score, v.Vector = score, vector
		}
		found := false
		for _, r := range aff.Ranges {
			// GIT ranges are commit hashes, which an installed package never carries.
			if strings.EqualFold(r.Type, "GIT") {
				continue
			}
			introduced := ""
			for _, ev := range r.Events {
				switch {
				case ev.Introduced != "":
					introduced = ev.Introduced
				case ev.Fixed != "":
					v.Affected = append(v.Affected, Affected{Ecosystem: ecosystem, Package: name,
						Introduced: introduced, Fixed: ev.Fixed})
					found = true
					introduced = ""
				case ev.LastAffected != "":
					v.Affected = append(v.Affected, Affected{Ecosystem: ecosystem, Package: name,
						Introduced: introduced, LastAffected: ev.LastAffected})
					found = true
					introduced = ""
				}
			}
			// An introduced version with no fix and no last-affected: everything from there on is affected,
			// which is the case that matters most and must not be dropped.
			if introduced != "" {
				v.Affected = append(v.Affected, Affected{Ecosystem: ecosystem, Package: name, Introduced: introduced})
				found = true
			}
		}
		// Some ecosystems list the affected versions one by one instead of as a range. Each becomes its own
		// closed range, so the match is exact rather than "everything from here on".
		if !found {
			for _, ver := range aff.Versions {
				if ver == "" {
					continue
				}
				v.Affected = append(v.Affected, Affected{Ecosystem: ecosystem, Package: name,
					Introduced: ver, LastAffected: ver})
			}
		}
	}
	if len(v.Affected) == 0 {
		return Vulnerability{}, false
	}
	v.Severity = SeverityFor(v.Score)
	return v, true
}

// cvssOf returns the highest CVSS base score the record carries and its vector. CVSS v4 is preferred over
// v3 and v3 over v2, because that is the order the scores were defined in, not because one is larger.
func cvssOf(list []osvSeverity) (float64, string) {
	best, bestRank, bestVector := 0.0, -1, ""
	for _, s := range list {
		rank := 0
		switch strings.ToUpper(s.Type) {
		case "CVSS_V4":
			rank = 3
		case "CVSS_V3":
			rank = 2
		case "CVSS_V2":
			rank = 1
		default:
			continue
		}
		score, ok := scoreFromVector(s.Score)
		if !ok {
			continue
		}
		if rank > bestRank {
			best, bestRank, bestVector = score, rank, s.Score
		}
	}
	return best, bestVector
}

// scoreFromVector reads the base score out of an OSV severity value, which is either a bare number or a
// CVSS vector string. A vector without a score is not guessed at: an advisory whose score openlog cannot
// read is stored with none rather than with a made-up one.
func scoreFromVector(value string) (float64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if score, err := strconv.ParseFloat(value, 64); err == nil {
		return score, true
	}
	// CVSS:3.1/AV:N/AC:L/... carries no numeric score; some feeds append it as "…/9.8".
	if at := strings.LastIndex(value, "/"); at >= 0 {
		if score, err := strconv.ParseFloat(value[at+1:], 64); err == nil {
			return score, true
		}
	}
	return cvss31Score(value)
}

// cvss31Score computes the CVSS v3.x base score from its vector. The formula is short and fully specified,
// and a feed that gives only the vector is common enough that dropping those advisories would leave the
// most serious ones unrated.
func cvss31Score(vector string) (float64, bool) {
	if !strings.HasPrefix(strings.ToUpper(vector), "CVSS:3") {
		return 0, false
	}
	metrics := map[string]string{}
	for _, part := range strings.Split(vector, "/")[1:] {
		k, v, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}
		metrics[strings.ToUpper(k)] = strings.ToUpper(v)
	}
	weight := func(key string, table map[string]float64) (float64, bool) {
		v, ok := table[metrics[key]]
		return v, ok
	}
	av, ok1 := weight("AV", map[string]float64{"N": 0.85, "A": 0.62, "L": 0.55, "P": 0.2})
	ac, ok2 := weight("AC", map[string]float64{"L": 0.77, "H": 0.44})
	ui, ok3 := weight("UI", map[string]float64{"N": 0.85, "R": 0.62})
	c, ok4 := weight("C", map[string]float64{"H": 0.56, "L": 0.22, "N": 0})
	i, ok5 := weight("I", map[string]float64{"H": 0.56, "L": 0.22, "N": 0})
	a, ok6 := weight("A", map[string]float64{"H": 0.56, "L": 0.22, "N": 0})
	if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6) {
		return 0, false
	}
	scopeChanged := metrics["S"] == "C"
	prTable := map[string]float64{"N": 0.85, "L": 0.62, "H": 0.27}
	if scopeChanged {
		prTable = map[string]float64{"N": 0.85, "L": 0.68, "H": 0.5}
	}
	pr, ok := prTable[metrics["PR"]]
	if !ok {
		return 0, false
	}
	iss := 1 - (1-c)*(1-i)*(1-a)
	var impact float64
	if scopeChanged {
		impact = 7.52*(iss-0.029) - 3.25*pow(iss-0.02, 15)
	} else {
		impact = 6.42 * iss
	}
	if impact <= 0 {
		return 0, true
	}
	exploitability := 8.22 * av * ac * pr * ui
	score := impact + exploitability
	if scopeChanged {
		score *= 1.08
	}
	if score > 10 {
		score = 10
	}
	return roundUp1(score), true
}

func pow(base float64, exp int) float64 {
	out := 1.0
	for range exp {
		out *= base
	}
	return out
}

// roundUp1 is CVSS's Roundup: the smallest number with one decimal that is not less than the input.
func roundUp1(v float64) float64 {
	i := int(v*100000 + 0.5)
	if i%10000 == 0 {
		return float64(i) / 100000
	}
	return (float64(i/10000) + 1) / 10
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ParseOSVZip reads an OSV "all.zip" export: one JSON document per entry. Entries that do not parse are
// skipped rather than failing the sync, because one malformed advisory must not cost the other 50 000.
func ParseOSVZip(data []byte, limit int) ([]Vulnerability, int, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, 0, fmt.Errorf("not a zip archive: %w", err)
	}
	out := make([]Vulnerability, 0, len(zr.File))
	skipped := 0
	for _, f := range zr.File {
		if limit > 0 && len(out) >= limit {
			break
		}
		if f.FileInfo().IsDir() || !strings.HasSuffix(f.Name, ".json") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			skipped++
			continue
		}
		body, err := io.ReadAll(io.LimitReader(rc, maxRecordBytes))
		rc.Close()
		if err != nil {
			skipped++
			continue
		}
		v, ok := ParseOSV(body)
		if !ok {
			skipped++
			continue
		}
		out = append(out, v)
	}
	return out, skipped, nil
}

// maxRecordBytes bounds one advisory document.
const maxRecordBytes = 1 << 20

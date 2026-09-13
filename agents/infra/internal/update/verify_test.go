package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	lib "github.com/onuragtas/openlog/libs/release"
)

func TestVerifyInstructionRules(t *testing.T) {
	key := newKey(t)
	other := newKey(t)
	archive := []byte("archive")
	const base = "https://releases.example/v"

	good := func(version string, compat map[string]string) []byte {
		return manifestFor(t, version, base, archive, compat, nil)
	}
	floorManifest := func(floor string) func() (*lib.Manifest, error) {
		return func() (*lib.Manifest, error) {
			c := map[string]string{}
			if floor != "" {
				c["rollback_floor"] = floor
			}
			return lib.ParseManifest(good("0.9.1", c))
		}
	}

	type tc struct {
		name     string
		ins      func() *Instruction
		trusted  []ed25519.PublicKey
		running  string
		manifest func() (*lib.Manifest, error)
		arch     string
		rule     int // -1 = accepted
		msg      string
	}
	up := func(target string, m []byte, sig string) func() *Instruction {
		return func() *Instruction { return instruction(ActionUpgrade, target, m, sig, "") }
	}
	rb := func(target string) func() *Instruction {
		m := good(target, nil)
		return func() *Instruction { return instruction(ActionRollback, target, m, sign(m, key), "") }
	}
	m100 := good("1.0.0", nil)
	tampered := append([]byte{}, m100...)
	tampered = []byte(strings.Replace(string(tampered), `"size":7`, `"size":8`, 1))

	cases := []tc{
		{name: "valid upgrade", ins: up("1.0.0", m100, sign(m100, key)), rule: -1},
		{name: "valid upgrade, second of two signatures", ins: up("1.0.0", m100, sign(m100, other, key)), rule: -1},

		{name: "rule 1: no trusted keys", ins: up("1.0.0", m100, sign(m100, key)), trusted: []ed25519.PublicKey{}, rule: 1, msg: "no trusted release keys"},
		{name: "rule 1: signed by untrusted key", ins: up("1.0.0", m100, sign(m100, other)), rule: 1, msg: "no signature from a trusted key"},
		{name: "rule 1: tampered manifest", ins: up("1.0.0", tampered, sign(m100, key)), rule: 1, msg: "does not verify"},
		{name: "rule 1: missing signature", ins: up("1.0.0", m100, ""), rule: 1},
		{name: "rule 1: manifest not base64", ins: func() *Instruction {
			i := instruction(ActionUpgrade, "1.0.0", m100, sign(m100, key), "")
			i.Manifest = "%%%"
			return i
		}, rule: 1},

		{name: "rule 2: wrong product", ins: func() *Instruction {
			m := manifestFor(t, "1.0.0", base, archive, nil, func(o map[string]any) { o["product"] = "other" })
			return instruction(ActionUpgrade, "1.0.0", m, sign(m, key), "")
		}, rule: 2, msg: "product"},
		{name: "rule 2: wrong schema", ins: func() *Instruction {
			m := manifestFor(t, "1.0.0", base, archive, nil, func(o map[string]any) { o["schema"] = 2 })
			return instruction(ActionUpgrade, "1.0.0", m, sign(m, key), "")
		}, rule: 2, msg: "schema"},
		{name: "rule 2: version differs from target", ins: up("1.0.1", m100, sign(m100, key)), rule: 2, msg: "does not match target_version"},
		{name: "rule 2: invalid target", ins: up("latest", m100, sign(m100, key)), rule: 2},

		{name: "rule 3: no artifact for arch", ins: up("1.0.0", m100, sign(m100, key)), arch: "arm64", rule: 3, msg: "linux/arm64"},
		{name: "rule 3: wrong format only", ins: func() *Instruction {
			m := manifestFor(t, "1.0.0", base, archive, nil, func(o map[string]any) {
				o["artifacts"].([]any)[0].(map[string]any)["format"] = "deb"
			})
			return instruction(ActionUpgrade, "1.0.0", m, sign(m, key), "")
		}, rule: 3},

		{name: "rule 4: downgrade via upgrade", ins: func() *Instruction {
			m := good("0.8.0", nil)
			return instruction(ActionUpgrade, "0.8.0", m, sign(m, key), "")
		}, rule: 4, msg: "not newer"},
		{name: "rule 4: same version", ins: func() *Instruction {
			m := good("0.9.1", nil)
			return instruction(ActionUpgrade, "0.9.1", m, sign(m, key), "")
		}, rule: 4, msg: "not newer"},
		{name: "rule 4: pre-release of running is older", ins: func() *Instruction {
			m := good("0.9.1-beta.1", nil)
			return instruction(ActionUpgrade, "0.9.1-beta.1", m, sign(m, key), "")
		}, rule: 4},
		{name: "rule 4: below min_upgrade_from", ins: func() *Instruction {
			m := good("1.0.0", map[string]string{"min_upgrade_from": "0.9.5"})
			return instruction(ActionUpgrade, "1.0.0", m, sign(m, key), "")
		}, rule: 4, msg: "min_upgrade_from"},
		{name: "rule 4: exactly min_upgrade_from", ins: func() *Instruction {
			m := good("1.0.0", map[string]string{"min_upgrade_from": "0.9.1"})
			return instruction(ActionUpgrade, "1.0.0", m, sign(m, key), "")
		}, rule: -1},

		{name: "rule 5: rollback at floor", ins: rb("0.9.0"), manifest: floorManifest("0.9.0"), rule: -1},
		{name: "rule 5: rollback below floor", ins: rb("0.8.0"), manifest: floorManifest("0.9.0"), rule: 5, msg: "rollback_floor"},
		{name: "rule 5: rollback to newer", ins: rb("1.0.0"), manifest: floorManifest("0.9.0"), rule: 5, msg: "not older"},
		{name: "rule 5: rollback to same", ins: rb("0.9.1"), manifest: floorManifest("0.9.0"), rule: 5},
		{name: "rule 5: floor unknown (no kept manifest)", ins: rb("0.9.0"), manifest: func() (*lib.Manifest, error) { return nil, errors.New("missing") }, rule: 5, msg: "unknown"},
		{name: "rule 5: floor unknown (manifest has none)", ins: rb("0.9.0"), manifest: floorManifest(""), rule: 5, msg: "unknown"},
		{name: "rule 5: kept manifest of another version", ins: rb("0.9.0"), running: "0.9.2", manifest: floorManifest("0.9.0"), rule: 5, msg: "kept manifest"},

		{name: "unknown action", ins: func() *Instruction {
			i := instruction("sidegrade", "1.0.0", m100, sign(m100, key), "")
			return i
		}, rule: 0},
		{name: "bad download url", ins: func() *Instruction {
			return instruction(ActionUpgrade, "1.0.0", m100, sign(m100, key), "file:///etc/passwd")
		}, rule: 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			trusted := c.trusted
			if trusted == nil {
				trusted = []ed25519.PublicKey{key.pub}
			}
			running := c.running
			if running == "" {
				running = "0.9.1"
			}
			arch := c.arch
			if arch == "" {
				arch = "amd64"
			}
			v, err := VerifyInstruction(VerifyInput{
				Instruction: c.ins(), Trusted: trusted, Running: running, RunningManifest: c.manifest, OS: "linux", Arch: arch,
			})
			if c.rule == -1 {
				if err != nil {
					t.Fatalf("rejected: %v", err)
				}
				if v.Artifact.Size != int64(len(archive)) || !strings.HasPrefix(v.DownloadURL, base) {
					t.Errorf("verified: %+v", v)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted, want rule %d", c.rule)
			}
			if got := RuleOf(err); got != c.rule {
				t.Errorf("rule %d (%v), want %d", got, err, c.rule)
			}
			if c.msg != "" && !strings.Contains(err.Error(), c.msg) {
				t.Errorf("error %q does not contain %q", err, c.msg)
			}
		})
	}
}

func TestVerifyUsesDownloadURLOverride(t *testing.T) {
	key := newKey(t)
	m := manifestFor(t, "1.0.0", "https://github.example/r", []byte("x"), nil, nil)
	ins := instruction(ActionUpgrade, "1.0.0", m, sign(m, key), "https://ingest.example:4318/v1/openlog/releases/1.0.0/a.tar.gz")
	v, err := VerifyInstruction(VerifyInput{Instruction: ins, Trusted: []ed25519.PublicKey{key.pub}, Running: "0.9.0", OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if v.DownloadURL != ins.DownloadURL {
		t.Errorf("download url %s", v.DownloadURL)
	}
	if string(v.ManifestBytes) != string(m) {
		t.Error("manifest bytes not kept verbatim")
	}
	if _, err := base64.StdEncoding.DecodeString(ins.Manifest); err != nil {
		t.Fatal(err)
	}
}

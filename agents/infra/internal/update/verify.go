package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"

	lib "github.com/onuragtas/openlog/libs/release"
)

// RuleError is a failed verification rule (docs/contracts/releases-updates.md §3, "Agent
// verification rules"). Rule 0 is a malformed instruction.
type RuleError struct {
	Rule int
	Msg  string
}

func (e *RuleError) Error() string { return e.Msg }

func ruleErr(rule int, format string, a ...any) error {
	return &RuleError{Rule: rule, Msg: fmt.Sprintf(format, a...)}
}

// RuleOf returns the rule number of a verification error, or -1.
func RuleOf(err error) int {
	var re *RuleError
	if errors.As(err, &re) {
		return re.Rule
	}
	return -1
}

// ErrNoTrustedKeys is the contract's error text for builds without release keys.
const ErrNoTrustedKeys = "no trusted release keys"

// ErrNotCapable is the contract's error text for installs that cannot update (rule 8).
const ErrNotCapable = "not update capable"

// Verified is an instruction that passed rules 1-5.
type Verified struct {
	Action        string
	Version       lib.Version
	Manifest      *lib.Manifest
	ManifestBytes []byte
	Signature     []byte
	Artifact      lib.Artifact
	DownloadURL   string
}

// VerifyInput holds what the manifest rules are checked against.
type VerifyInput struct {
	Instruction *Instruction
	Trusted     []ed25519.PublicKey
	// Running is the version of the running agent.
	Running string
	// RunningManifest loads the manifest kept next to the running binary (rule 5).
	RunningManifest func() (*lib.Manifest, error)
	OS, Arch        string
}

// VerifyInstruction applies rules 1-5 in order. Rules 6 and 7 (download, extraction, self-test)
// are applied while staging; rule 8 by the manager before anything touches disk or network.
func VerifyInstruction(in VerifyInput) (*Verified, error) {
	ins := in.Instruction
	if ins == nil {
		return nil, ruleErr(0, "empty update instruction")
	}

	// Rule 1: signature valid with a trusted key.
	if len(in.Trusted) == 0 {
		return nil, ruleErr(1, ErrNoTrustedKeys)
	}
	data, err := base64.StdEncoding.DecodeString(ins.Manifest)
	if err != nil || len(data) == 0 {
		return nil, ruleErr(1, "manifest signature invalid: manifest is not valid base64")
	}
	if _, err := lib.Verify(data, []byte(ins.Signature), in.Trusted); err != nil {
		return nil, ruleErr(1, "manifest signature invalid: %v", err)
	}

	// Rule 2: product, schema and version.
	m, err := lib.ParseManifest(data)
	if err != nil {
		return nil, ruleErr(2, "invalid manifest: %v", err)
	}
	if m.Product != lib.Product || m.Schema != lib.SchemaVersion {
		return nil, ruleErr(2, "invalid manifest: product %q schema %d", m.Product, m.Schema)
	}
	target, err := lib.ParseVersion(ins.TargetVersion)
	if err != nil {
		return nil, ruleErr(2, "invalid target_version: %v", err)
	}
	if m.Version != target.String() {
		return nil, ruleErr(2, "manifest version %s does not match target_version %s", m.Version, ins.TargetVersion)
	}

	// Rule 3: an artifact for this platform.
	art, ok := m.Artifact(lib.ComponentInfraAgent, in.OS, in.Arch, lib.FormatTarGz)
	if !ok {
		return nil, ruleErr(3, "manifest %s has no %s %s/%s %s artifact", m.Version, lib.ComponentInfraAgent, in.OS, in.Arch, lib.FormatTarGz)
	}

	running, err := lib.ParseVersion(in.Running)
	if err != nil {
		return nil, ruleErr(4, "running version %q is not SemVer: %v", in.Running, err)
	}
	switch ins.Action {
	case ActionUpgrade:
		// Rule 4: newer, and reachable from the running version.
		if lib.Compare(target, running) <= 0 {
			return nil, ruleErr(4, "upgrade target %s is not newer than running version %s", target, running)
		}
		if s := m.Compatibility.MinUpgradeFrom; s != "" {
			minFrom, _ := lib.ParseVersion(s) // validated by ParseManifest
			if lib.Compare(running, minFrom) < 0 {
				return nil, ruleErr(4, "running version %s is older than min_upgrade_from %s of %s", running, minFrom, target)
			}
		}
	case ActionRollback:
		// Rule 5: older, and not below the running version's rollback_floor.
		if lib.Compare(target, running) >= 0 {
			return nil, ruleErr(5, "rollback target %s is not older than running version %s", target, running)
		}
		if in.RunningManifest == nil {
			return nil, ruleErr(5, "rollback_floor unknown: no manifest for running version %s", running)
		}
		cur, err := in.RunningManifest()
		if err != nil {
			return nil, ruleErr(5, "rollback_floor unknown: %v", err)
		}
		if lib.Compare(cur.ParsedVersion(), running) != 0 {
			return nil, ruleErr(5, "rollback_floor unknown: kept manifest is for %s, running %s", cur.Version, running)
		}
		if cur.Compatibility.RollbackFloor == "" {
			return nil, ruleErr(5, "rollback_floor unknown: manifest of %s has none", running)
		}
		floor, _ := lib.ParseVersion(cur.Compatibility.RollbackFloor)
		if lib.Compare(target, floor) < 0 {
			return nil, ruleErr(5, "rollback target %s is below rollback_floor %s of running version %s", target, floor, running)
		}
	default:
		return nil, ruleErr(0, "unknown update action %q", ins.Action)
	}

	dl := ins.DownloadURL
	if dl == "" {
		dl = art.URL
	}
	u, err := url.Parse(dl)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, ruleErr(0, "invalid download_url %q", dl)
	}
	return &Verified{
		Action: ins.Action, Version: target, Manifest: m, ManifestBytes: data,
		Signature: []byte(ins.Signature), Artifact: art, DownloadURL: dl,
	}, nil
}

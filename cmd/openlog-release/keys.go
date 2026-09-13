package main

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	lib "github.com/onuragtas/openlog/libs/release"
)

// cmdKeygen prints a new key pair in a sourceable KEY=value form.
func cmdKeygen(args []string, stdout io.Writer) error {
	fs := newFlagSet("keygen")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return usagef("unexpected arguments %v", fs.Args())
	}
	pub, seed, err := lib.GenerateKey()
	if err != nil {
		return err
	}
	k, err := lib.ParsePublicKey(pub)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "# openlog release key %s\n", lib.KeyID(k))
	fmt.Fprintln(stdout, "# OPENLOG_RELEASE_PUBLIC_KEYS is public (repository variable, compiled into binaries).")
	fmt.Fprintln(stdout, "# OPENLOG_RELEASE_SIGNING_KEY is SECRET (repository secret only; never commit it).")
	fmt.Fprintf(stdout, "OPENLOG_RELEASE_KEY_ID=%s\n", lib.KeyID(k))
	fmt.Fprintf(stdout, "OPENLOG_RELEASE_PUBLIC_KEYS=%s\n", pub)
	fmt.Fprintf(stdout, "OPENLOG_RELEASE_SIGNING_KEY=%s\n", seed)
	return nil
}

// cmdSign appends (or replaces, for the same key) a signature line in FILE.sig.
func cmdSign(args []string, stdout io.Writer) error {
	fs := newFlagSet("sign")
	keyEnv := fs.String("key-env", "OPENLOG_RELEASE_SIGNING_KEY", "environment variable holding the base64 32-byte Ed25519 seed")
	sigPath := fs.String("sig", "", "signature file (default FILE.sig)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return usagef("want exactly one FILE")
	}
	file := fs.Arg(0)
	if *sigPath == "" {
		*sigPath = file + ".sig"
	}
	seed := os.Getenv(*keyEnv)
	if strings.TrimSpace(seed) == "" {
		return fmt.Errorf("environment variable %s is empty (see docs/operations/releasing.md)", *keyEnv)
	}
	priv, err := lib.PrivateKeyFromSeed(seed)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	line := lib.SignatureLine(data, priv)
	keyID := lib.KeyID(priv.Public().(ed25519.PublicKey))

	existing, err := os.ReadFile(*sigPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	out := appendSignature(existing, line, keyID)
	if err := os.WriteFile(*sigPath, out, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "signed %s with key %s -> %s\n", file, keyID, *sigPath)
	return nil
}

// appendSignature returns the signature file with line added. A previous line of the same key is
// replaced so that re-signing is idempotent; lines of other keys are kept (key rotation).
func appendSignature(existing []byte, line, keyID string) []byte {
	var buf bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(existing))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 3 && f[0] == lib.SignaturePrefix && f[1] == keyID {
			continue
		}
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		buf.WriteString(sc.Text())
		buf.WriteByte('\n')
	}
	buf.WriteString(line)
	buf.WriteByte('\n')
	return buf.Bytes()
}

// loadKeys reads trusted public keys from a key file (one base64 key per line, # comments) or a
// comma-separated list of base64 keys.
func loadKeys(spec string) ([]ed25519.PublicKey, error) {
	var items []string
	if st, err := os.Stat(spec); err == nil && !st.IsDir() {
		data, err := os.ReadFile(spec)
		if err != nil {
			return nil, err
		}
		for _, l := range strings.Split(string(data), "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
				items = append(items, l)
			}
		}
	} else {
		for _, s := range strings.Split(spec, ",") {
			if s = strings.TrimSpace(s); s != "" {
				items = append(items, s)
			}
		}
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("no public keys in %q", spec)
	}
	return lib.ParsePublicKeys(items...)
}

// cmdVerify verifies FILE against FILE.sig and, for manifests and indexes, validates the content.
func cmdVerify(args []string, stdout io.Writer) error {
	fs := newFlagSet("verify")
	keysSpec := fs.String("keys", os.Getenv("OPENLOG_RELEASE_PUBLIC_KEYS"), "trusted keys: key file or comma-separated base64 keys (default $OPENLOG_RELEASE_PUBLIC_KEYS)")
	sigPath := fs.String("sig", "", "signature file (default FILE.sig)")
	checkArtifacts := fs.Bool("check-artifacts", false, "for a manifest: also check size and sha256 of every artifact file next to it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return usagef("want exactly one FILE")
	}
	if *keysSpec == "" {
		return usagef("--keys is required")
	}
	file := fs.Arg(0)
	if *sigPath == "" {
		*sigPath = file + ".sig"
	}
	keys, err := loadKeys(*keysSpec)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(*sigPath)
	if err != nil {
		return err
	}
	keyID, err := lib.Verify(data, sig, keys)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	fmt.Fprintf(stdout, "OK signature %s (key %s)\n", file, keyID)

	var probe struct {
		Artifacts json.RawMessage `json:"artifacts"`
		Channels  json.RawMessage `json:"channels"`
	}
	_ = json.Unmarshal(data, &probe)
	switch {
	case probe.Artifacts != nil:
		m, err := lib.ParseManifest(data)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "OK manifest %s channel=%s artifacts=%d\n", m.Version, m.Channel, len(m.Artifacts))
		if *checkArtifacts {
			dir := filepath.Dir(file)
			for _, a := range m.Artifacts {
				sum, size, err := hashFile(filepath.Join(dir, a.Name))
				if err != nil {
					return err
				}
				if sum != a.SHA256 || size != a.Size {
					return fmt.Errorf("artifact %s: sha256/size mismatch (got %s %d, manifest %s %d)", a.Name, sum, size, a.SHA256, a.Size)
				}
				fmt.Fprintf(stdout, "OK artifact %s %s\n", a.Name, sum)
			}
			if m.HelmChart != nil && m.HelmChart.SHA256 != "" {
				sum, _, err := hashFile(filepath.Join(dir, m.HelmChart.Name))
				if err != nil {
					return err
				}
				if sum != m.HelmChart.SHA256 {
					return fmt.Errorf("helm chart %s: sha256 mismatch", m.HelmChart.Name)
				}
				fmt.Fprintf(stdout, "OK helm chart %s %s\n", m.HelmChart.Name, sum)
			}
		}
	case probe.Channels != nil:
		idx, err := lib.ParseIndex(data)
		if err != nil {
			return err
		}
		stable, _ := idx.Latest(lib.ChannelStable)
		beta, _ := idx.Latest(lib.ChannelBeta)
		fmt.Fprintf(stdout, "OK index stable=%d beta=%d latest_stable=%s latest_beta=%s\n",
			len(idx.Channels[lib.ChannelStable]), len(idx.Channels[lib.ChannelBeta]), stable.Version, beta.Version)
	}
	return nil
}

// Command openlog-release is the release tooling for openlog (docs/contracts/releases-updates.md,
// docs/operations/releasing.md):
//
//	openlog-release keygen
//	openlog-release build-manifest --version V --dist DIR --base-url URL [--channel C] [--compat k=v]… [--image name=ref]… [--migrations db=dir]…
//	openlog-release sign --key-env OPENLOG_RELEASE_SIGNING_KEY FILE
//	openlog-release build-index --out FILE [--base-url ROOT] [--merge index.json] [--entry V=URL]… [MANIFEST…]
//	openlog-release verify --keys FILE|B64,… [--check-artifacts] FILE
//	openlog-release archive --out FILE.tar.gz --prefix DIR SRC_DIR
//
// Signing keys never touch the disk through this tool: keygen prints them, sign reads the seed from
// an environment variable.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/onuragtas/openlog/internal/version"
)

const usage = `usage: openlog-release <command> [flags]

commands:
  keygen           generate an Ed25519 release key pair (printed, never written)
  build-manifest   hash the artifacts of a release directory and write manifest.json
  sign             append a signature line to FILE.sig using a seed from the environment
  build-index      build index.json from manifests and existing entries (newest first)
  verify           verify FILE against FILE.sig and trusted public keys
  archive          write a deterministic tar.gz of a directory
  version          print the version

Run "openlog-release <command> -h" for flags.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmds := map[string]func([]string, io.Writer) error{
		"keygen":         cmdKeygen,
		"build-manifest": cmdBuildManifest,
		"sign":           cmdSign,
		"build-index":    cmdBuildIndex,
		"verify":         cmdVerify,
		"archive":        cmdArchive,
	}
	name := args[0]
	switch name {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	case "version", "-version", "--version":
		fmt.Fprintf(stdout, "openlog-release %s (commit %s, built %s)\n", version.String(), version.Commit, version.Date)
		return 0
	}
	cmd, ok := cmds[name]
	if !ok {
		fmt.Fprintf(stderr, "openlog-release: unknown command %q\n\n%s", name, usage)
		return 2
	}
	if err := cmd(args[1:], stdout); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		var ue usageError
		if errors.As(err, &ue) {
			fmt.Fprintf(stderr, "openlog-release %s: %v\n", name, err)
			return 2
		}
		fmt.Fprintf(stderr, "openlog-release %s: %v\n", name, err)
		return 1
	}
	return 0
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }

// newFlagSet returns a flag set whose parse errors are reported to stderr.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("openlog-release "+name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

// multiFlag collects repeated flag values.
type multiFlag []string

func (m *multiFlag) String() string     { return fmt.Sprint(*m) }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

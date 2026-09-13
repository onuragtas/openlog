# Releasing openlog

How openlog releases are built, signed and published, and how to produce a signed release locally
for tests. Binding rules: [docs/contracts/releases-updates.md](../contracts/releases-updates.md);
rationale: [docs/plan/09-releases-updates.md](../plan/09-releases-updates.md) (D-025…D-029).

Every release has **one product version** (`vX.Y.Z`, pre-releases `vX.Y.Z-beta.N` go to the `beta`
channel) for the backend, the image, the Helm chart and the agents. The release manifest is signed
with Ed25519. Agents and the backend trust only the public keys compiled into them, so **the signing
key is the root of trust for auto-update**: anybody holding it can push code to every agent.

## Tooling

| What | Where |
|---|---|
| `openlog-release` CLI (`keygen`, `build-manifest`, `sign`, `build-index`, `verify`, `archive`) | `cmd/openlog-release`, uses `libs/release` |
| Build targets | root `Makefile` (`release-*`, `shellcheck`, `actionlint`, `helm-lint`, `package-test`, `install-test`) |
| deb/rpm | `packaging/nfpm/infra-agent.yaml`, `packaging/scripts/*.sh` (nfpm runs in the pinned `goreleaser/nfpm` image) |
| Installer | `scripts/install.sh` (attached to every release) |
| CI | `.github/workflows/ci.yml` (incl. a signed release dry run), `.github/workflows/release.yml` |

### Release contents (`dist/v<version>/`)

| File | Manifest |
|---|---|
| `openlog-infra-agent_<v>_linux_{amd64,arm64}.tar.gz` (one top dir: binary, `LICENSE`, `README.md`, `packaging/`) | `artifacts[]` `component=infra-agent`, `format=tar.gz` |
| `openlog-infra-agent_<v>_linux_{amd64,arm64}.{deb,rpm}` | `format=deb` / `rpm` |
| `openlog_<v>_linux_{amd64,arm64}.tar.gz` (all backend binaries) | `component=backend` |
| `openlog-<v>.tgz` (Helm chart, `version` = `appVersion` = `<v>`) | `helm_chart` |
| `ghcr.io/onuragtas/openlog:<v>` (not a file) | `images.openlog` = `…@sha256:<digest>` |
| `manifest.json`, `manifest.json.sig` | signed |
| `index.json`, `index.json.sig` | signed; all releases, newest first |
| `install.sh` | not in the manifest (bootstrap, see [install.sh trust model](#installsh)) |

`build-manifest` picks up artifacts by these file names; unknown files are ignored. It fills
`compatibility` with `oldest_supported_agent = X.(Y-2).0` and `rollback_floor = X.(Y-1).0`; override with
`RELEASE_COMPAT="min_upgrade_from=0.3.0 rollback_floor=0.2.0"` (an empty value clears a bound).
`migrations` is computed from `migrations/postgres` and `schema/clickhouse` (highest number, files whose
first line is `-- openlog:phase contract`).

Version injection (all builds, including `make build` and the image): `-X
github.com/onuragtas/openlog/internal/version.{Version,Commit,Date}`, `-X
github.com/onuragtas/openlog/internal/release.trustedKeys=$OPENLOG_RELEASE_PUBLIC_KEYS`; the agent gets the
same under `github.com/onuragtas/openlog/agents/infra/internal/{version,release}`. Every binary prints
its version with `-version`.

## One-time setup (repository owner)

1. **Generate the release key offline** (a trusted machine, ideally not connected, never CI):

   ```sh
   go build -o /tmp/openlog-release ./cmd/openlog-release
   /tmp/openlog-release keygen > openlog-release-key-1.env   # keep this file offline
   ```

   Output:

   ```
   # openlog release key 3f0c9a…
   OPENLOG_RELEASE_KEY_ID=3f0c9a1b2c3d4e5f
   OPENLOG_RELEASE_PUBLIC_KEYS=<base64 public key>
   OPENLOG_RELEASE_SIGNING_KEY=<base64 seed>      # SECRET
   ```

   Store the seed in a password manager / hardware-backed vault (two people should be able to recover
   it). Losing it means a key rotation; leaking it means an **emergency** rotation (below).

2. **GitHub → Settings → Secrets and variables → Actions:**
   - *Secret* `OPENLOG_RELEASE_SIGNING_KEY` = the seed. Restrict it to the `release` workflow
     (recommended: put it in an environment `release` with required reviewers and tag protection
     `v*`, and add `environment: release` to the `release` job).
   - *Variable* `OPENLOG_RELEASE_PUBLIC_KEYS` = the public key (comma-separated when two keys are active).
3. **Settings → Actions → General → Workflow permissions:** read and write (the release job
   creates releases; the image job pushes to GHCR with `packages: write`).
4. After the first image push, make the GHCR package `openlog` **public** (Packages → openlog →
   Package settings) so that pulls work without credentials.
5. Protect tags `v*` (Settings → Rules → Tag rulesets) so only maintainers can cut releases.

Delete `openlog-release-key-1.env` from any online machine after step 2.

## Cutting a release

```sh
# master is green (ci.yml, incl. "release dry run")
git tag -s v0.4.0 -m "openlog 0.4.0"      # v0.5.0-beta.1 → channel beta, GitHub pre-release
git push origin v0.4.0
```

`release.yml` then:

1. `prepare` – version/channel from the tag; fails early if the key variable/secret are missing.
2. `image` – builds `linux/amd64,linux/arm64` with version + compiled-in keys, pushes
   `ghcr.io/onuragtas/openlog:<v>` (and `:latest` for stable), outputs the digest.
3. `release` – `make web`, collects previous releases that have a `manifest.json` via the GitHub API,
   `make release-local VERSION=<v> RELEASE_IMAGE=ghcr.io/onuragtas/openlog@<digest> …`
   (build → sign → `verify --check-artifacts` → index over all releases → sign → verify), installs
   the amd64 deb/rpm in containers, creates the GitHub release as a draft, uploads everything and
   publishes it (stable: marked *latest*; beta: pre-release). For a beta, `index.json(.sig)` is also
   re-uploaded to the latest stable release, because `releases/latest/download/index.json` (the
   default index URL) serves the latest *stable* release.

Nothing is published if any step fails; a failed draft can be deleted and the tag re-pushed.

### Go agent modules (not yet automated in `release.yml`)

The Go agent (`agents/go`) is a set of Go modules, so it is released by **Go module tags on the same
commit** as the product tag (D-025):

1. `make -C agents/go set-version VERSION=X.Y.Z` (Go libraries cannot use `-ldflags`; this updates
   `agents/go/version.go`).
2. In that commit, rewrite local `replace`d requires of the submodules (`instrumentation/*`, `examples`)
   from `v0.0.0` to `vX.Y.Z` and drop the `replace` directives.
3. Tag the core module first, then the submodules: `agents/go/vX.Y.Z`,
   `agents/go/instrumentation/{grpc,chi,gin,echo}/vX.Y.Z` (optionally `agents/go/examples/vX.Y.Z`).
4. If the product version ever reaches `v2`, Go module paths need a `/v2` suffix (e.g. `agents/go/v2`).

## Verifying a release

```sh
go build -o bin/openlog-release ./cmd/openlog-release
V=0.4.0; B=https://github.com/onuragtas/openlog/releases/download/v$V
mkdir -p /tmp/rel && cd /tmp/rel
for f in manifest.json manifest.json.sig index.json index.json.sig openlog-infra-agent_${V}_linux_amd64.tar.gz; do curl -fsSLO "$B/$f"; done
openlog-release verify --keys "<public key(s), comma-separated, or a key file>" manifest.json
openlog-release verify --keys "<…>" index.json
# every artifact present next to manifest.json is size/sha256-checked; download all for a full check
openlog-release verify --keys "<…>" --check-artifacts manifest.json
```

Output of a good release:

```
OK signature manifest.json (key 3f0c9a1b2c3d4e5f)
OK manifest 0.4.0 channel=stable artifacts=10
OK artifact openlog_0.4.0_linux_amd64.tar.gz 5e1d…
…
OK helm chart openlog-0.4.0.tgz 9a7c…
```

Check the version and keys a binary was built with: `openlog-infra-agent -version`, `openlog-api -version`.

## Key rotation (two active keys)

Binaries accept a manifest if **any** signature line verifies with **any** compiled-in key (at most
2 active keys). Rotation must never leave deployed agents without a key they trust:

1. Generate key 2 offline (`openlog-release keygen`).
2. Set `OPENLOG_RELEASE_PUBLIC_KEYS=<key1>,<key2>`, add secret `OPENLOG_RELEASE_SIGNING_KEY_2=<seed2>`.
   Releases are now signed by both keys (`manifest.json.sig` has two lines) and binaries trust both.
3. Release and wait until the fleet runs a version that trusts key 2 (Fleet UI version distribution;
   agents older than the rotation-start release only trust key 1).
4. Swap: `OPENLOG_RELEASE_SIGNING_KEY=<seed2>`, delete `OPENLOG_RELEASE_SIGNING_KEY_2`. Keep
   `OPENLOG_RELEASE_PUBLIC_KEYS=<key1>,<key2>` for one more release, then set it to `<key2>` alone.
5. Destroy seed 1.

**Compromised key:** remove the secret immediately and rotate as above, but skip waiting where
possible: publish a release signed by *both* keys (the compromised key is still needed to reach
agents that only trust it) that trusts only the new key, then drop the old key. Agents that never
updated past a release trusting only the old key have to be reinstalled (`install.sh` or a
package). Announce the incident; consider `mode=off` in fleet policies until the fleet is migrated.

## Local signed releases (tests)

Used by agent self-update, fleet, updater and e2e tests. **Test keys are throwaway and must never be
used for, or trusted by, a real release.**

```sh
# 1. throwaway key pair in dist/testkeys/ (git-ignored; kept on re-run)
make release-testkeys
#    dist/testkeys/public.key   base64 public key (also a valid trusted-keys file)
#    dist/testkeys/signing.key  base64 seed
#    dist/testkeys/release.env  both, KEY=value

# 2. signed release; binaries trust dist/testkeys/public.key
make release-local VERSION=0.9.0 RELEASE_TESTKEYS=1 RELEASE_BASE_URL=http://127.0.0.1:18090
make release-local VERSION=0.10.0 RELEASE_TESTKEYS=1 RELEASE_BASE_URL=http://127.0.0.1:18090

# 3. serve dist/ (index at http://127.0.0.1:18090/index.json, files at …/v0.10.0/<name>)
make release-serve        # python3 -m http.server 18090 in dist/
```

Equivalent without `RELEASE_TESTKEYS`:

```sh
make release-local VERSION=0.9.0 \
  OPENLOG_RELEASE_SIGNING_KEY="$(cat dist/testkeys/signing.key)" \
  OPENLOG_RELEASE_PUBLIC_KEYS="$(cat dist/testkeys/public.key)" \
  RELEASE_BASE_URL=http://127.0.0.1:18090
```

Notes:

- `RELEASE_BASE_URL` is the **releases root**: artifacts are `<root>/v<version>/<name>`, manifests
  `<root>/v<version>/manifest.json`. Choose the host the *consumer* sees: containers on Docker
  Desktop/OrbStack use `http://host.docker.internal:18090` (on Linux add
  `--add-host host.docker.internal:host-gateway`); compose services can serve `dist/` themselves.
- `dist/index.json` lists every `dist/v*/manifest.json` (all must verify with the current key —
  `make release-clean` after regenerating keys). `dist/v<version>/index.json` is a copy.
- A binary built **without** `OPENLOG_RELEASE_PUBLIC_KEYS` trusts nothing; tests can instead pass the
  key file: backend `OPENLOG_RELEASE_TRUSTED_KEYS_FILE=dist/testkeys/public.key`, agent
  `release.trusted_keys_file`.
- **Validly signed but broken release** (auto-rollback tests): build `VERSION=0.11.0`, replace
  `dist/v0.11.0/openlog-infra-agent_0.11.0_linux_<arch>.tar.gz` with a broken archive (same layout),
  then re-create and re-sign the manifest and index:

  ```sh
  export OPENLOG_RELEASE_SIGNING_KEY="$(cat dist/testkeys/signing.key)"
  rm -f dist/v0.11.0/manifest.json.sig
  bin/openlog-release build-manifest --version 0.11.0 --dist dist/v0.11.0 --base-url http://127.0.0.1:18090/v0.11.0
  bin/openlog-release sign dist/v0.11.0/manifest.json
  make release-index VERSION=0.11.0 RELEASE_TESTKEYS=1 RELEASE_BASE_URL=http://127.0.0.1:18090
  ```

  **Tampered artifact** (must be rejected): change the tarball *without* re-running `build-manifest`.
  **Bad signature** (must be rejected): edit one byte of `manifest.json` after signing.
- Useful variables: `RELEASE_ARCHES=amd64` (faster), `HELM=/path/to/helm` (the chart is skipped with a
  warning when helm is missing), `NFPM=nfpm` (local nfpm instead of Docker), `RELEASE_CHANNEL`,
  `RELEASE_COMPAT`, `RELEASE_IMAGE`, `RELEASE_INDEX_ARGS="--entry 0.8.0=https://…/manifest.json"`.
- The backend tarballs embed the placeholder UI unless `make web` ran before.
- Checks: `make package-test VERSION=0.9.0` (deb in debian:12, rpm in rockylinux:9, arch of the
  Docker host), `packaging/test/install.sh dist 0.9.0 0.10.0-beta.1` (install.sh in debian:12,
  ubuntu:24.04, rockylinux:9, alpine; needs a release built with
  `RELEASE_BASE_URL=http://host.docker.internal:18090` for the index URLs).

## deb / rpm layout

| Path | Owner | Notes |
|---|---|---|
| `/opt/openlog/infra-agent/versions/<v>/{openlog-infra-agent,LICENSE,README.md}` | `openlog-agent` | package files; the agent adds/prunes other versions itself |
| `/opt/openlog/infra-agent/versions/<v>/manifest.json(.sig)` | `openlog-agent` | signed manifest of `<v>` (the agent reads `rollback_floor` from it). In packages it lists only the agent tarballs, because the published `manifest.json` contains the packages' own sha256; version, compatibility, images and `released_at` are identical. `install.sh` tarball installs store the published manifest. |
| `/opt/openlog/infra-agent/current -> versions/<v>` | `openlog-agent` | created by postinstall (temp symlink + `mv -T`) |
| `/usr/bin/openlog-infra-agent -> /opt/openlog/infra-agent/current/openlog-infra-agent` | root | package symlink |
| `/usr/lib/systemd/system/openlog-infra-agent.service` | root | from `agents/infra/packaging/systemd/` |
| `/etc/openlog-infra-agent/config.yaml` | `root:openlog-agent 0640` | conffile (`config|noreplace`), never overwritten |
| `/var/lib/openlog-infra-agent` | `openlog-agent 0750` | state (`update-state.json`, buffer, host id) |

Maintainer scripts (`packaging/scripts`, shared by deb and rpm):

- **preinstall** creates the system user/group `openlog-agent`.
- **postinstall** switches `current` to the packaged version **unless the running version is newer**
  (the agent updated itself past the package); on first install enables the unit and starts it if a
  license key is configured; on upgrade `try-restart`s if `current` changed.
- **preremove** (removal only) stops and disables the service.
- **postremove** removal: deletes `current` and all `versions/` (also self-downloaded ones); config
  and state stay. **purge** (deb): also deletes `/etc/openlog-infra-agent`, `/var/lib/openlog-infra-agent`
  and `/opt/openlog`. The account is kept. rpm has no purge: `rm -rf /etc/openlog-infra-agent /var/lib/openlog-infra-agent`.

A package upgrade removes the previous package's `versions/<old>/` files, so an agent rollback target
installed by a package disappears on the next package upgrade (self-downloaded versions are not
affected).

## install.sh

```sh
curl -fsSL https://github.com/onuragtas/openlog/releases/latest/download/install.sh |
  sudo sh -s -- --license-key KEY --endpoint https://ingest.example.com:4318
```

Flags: `--license-key`, `--endpoint`, `--version` (default: newest on the channel from `index.json`),
`--channel stable|beta`, `--method auto|deb|rpm|tarball` (auto: deb on Debian/Ubuntu, rpm on
RHEL-likes/SUSE/Amazon Linux, tarball elsewhere, e.g. Alpine), `--base-url` (mirror or local release
root; env `OPENLOG_RELEASE_BASE_URL`), `--index-url`, `--no-start`. Environment equivalents:
`OPENLOG_LICENSE_KEY`, `OPENLOG_ENDPOINT`, `OPENLOG_VERSION`, `OPENLOG_CHANNEL`, `OPENLOG_INSTALL_METHOD`.

**Trust model.** The installer itself, `index.json` and `manifest.json` are fetched over **HTTPS**
(the bootstrap trusts TLS and GitHub, like any `curl | sh`). The downloaded package is checked
against the manifest's **size and sha256**; the installer does not verify the Ed25519 signature
(POSIX sh has no Ed25519). Every **later** update is applied by the agent, which verifies the
manifest signature against its compiled-in keys and the artifact hash before switching versions.
Users who want a fully verified bootstrap download the release files and run
`openlog-release verify --check-artifacts` before installing the package.

Re-running is idempotent: same version → only config/service are updated; newer release → upgrade;
an agent that already updated itself past the requested version is kept unless `--version` is given
explicitly. Without systemd (containers, Alpine/OpenRC) the agent is installed and configured but
must be started by the local service manager.

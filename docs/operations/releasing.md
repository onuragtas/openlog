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
| Installers | `scripts/install.sh` (agent) and `scripts/install-server.sh` (Compose server), attached to every release |
| CI | `.github/workflows/ci.yml` (incl. a signed release dry run), `.github/workflows/release.yml` |

### Release contents (`dist/v<version>/`)

| File | Manifest |
|---|---|
| `openlog-infra-agent_<v>_linux_{amd64,arm64}.tar.gz` (one top dir: binary, `LICENSE`, `README.md`, `packaging/`) | `artifacts[]` `component=infra-agent`, `format=tar.gz` |
| `openlog-infra-agent_<v>_linux_{amd64,arm64}.{deb,rpm}` | `format=deb` / `rpm` |
| `openlog_<v>_linux_{amd64,arm64}.tar.gz` (all backend binaries) | `component=backend` |
| `openlog-php-agent_<v>_linux_{amd64,arm64}.{tar.gz,deb,rpm,apk}` (PHP agent: 36 modules, `openlog-php-install`; [php-agent.md](../contracts/php-agent.md) §7.1) | `component=php-agent`, `format=tar.gz` / `deb` / `rpm` / `apk` |
| `openlog-javaagent-<v>.jar` + `.sha256` (Java agent, [below](#java-agent-jar)) | `component=java-agent`, `os`/`arch` `any`, `format=jar` (the `.sha256` is not in the manifest) |
| `openlog-node-<v>.tgz` + `.sha256` (`npm pack` of `openlog-node`, [below](#language-agent-packages-github-release)) | `component=node-agent`, `os`/`arch` `any`, `format=tgz` |
| `openlog_agent-<pep440 v>-py3-none-any.whl` + `.sha256` (Python agent wheel) | `component=python-agent`, `os`/`arch` `any`, `format=whl` |
| `openlog_agent-<pep440 v>.tar.gz` + `.sha256` (Python agent sdist) | not in the manifest |
| `OpenLog.Agent.<v>.nupkg` + `.sha256` (.NET agent) | `component=dotnet-agent`, `os`/`arch` `any`, `format=nupkg` |
| `openlog-<v>.tgz` (Helm chart `deploy/helm/openlog`, `version` = `appVersion` = `<v>`) | `helm_charts.openlog` (and `helm_chart`) |
| `openlog-agent-<v>.tgz` (Helm chart `deploy/helm/openlog-agent`, `version` = `appVersion` = `<v>`) | `helm_charts.openlog-agent` |
| `openlog-compose-<v>.tar.gz` (git-tracked `deploy/compose`, `x-openlog-compose-version` stamped; `make release-compose`) | `component=compose`, `os`/`arch` `any`, `format=tar.gz` |
| `ghcr.io/onuragtas/openlog:<v>` (not a file) | `images.openlog` = `…@sha256:<digest>` |
| `manifest.json`, `manifest.json.sig` | signed |
| `index.json`, `index.json.sig` | signed; all releases, newest first |
| `install.sh` | not in the manifest (bootstrap, see [install.sh trust model](#installsh)) |
| `install-server.sh` | not in the manifest (server bootstrap, see [install-server.sh](#install-serversh)) |

PHP agent artifacts are built by `agents/php/packaging/build-artifacts.sh VERSION DIR` (Docker; one architecture per
run). `release.yml` runs it in the `php-agent-artifacts` jobs on native `ubuntu-24.04` (amd64) and `ubuntu-24.04-arm`
(arm64) runners with all 36 modules (about 30–45 min each, in parallel with the image build), install-tests the
deb/rpm/apk (`agents/php/packaging/test.sh`) and hands the files to the `release` job, which downloads them into
`dist/v<version>/` before `make release-local`. The CI release dry run builds only `8.2-nts-glibc` and `8.3-nts-musl`
with deb and apk (about 8 min). Locally: `make release-php-agent VERSION=… [PHP_TARGETS="8.2-nts-glibc"]
[PHP_PACKAGES=""]` before `make release-local`; without it a local release simply has no PHP agent.

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
   Package settings) so that pulls work without credentials. The same applies to `charts/openlog` and
   `charts/openlog-agent` after the first `helm-oci` run (optional, see [Helm charts](#helm-charts)).
5. Protect tags `v*` (Settings → Rules → Tag rulesets) so only maintainers can cut releases.

6. **npm (Node.js agent `openlog-node`, optional):** the package is unscoped, so no npm organization is needed. On
   npmjs.com create a *granular access token* with read and write permission for packages (for the very first
   publish, before `openlog-node` exists, grant it on all packages; afterwards it can be limited to `openlog-node`;
   2FA-enforcing accounts: the token must be allowed to bypass 2FA). Store it as the repository
   *secret* `NPM_TOKEN`. Without the secret, `release.yml` skips the npm job with a warning and everything else is
   released. Provenance (`npm publish --provenance`) needs the public GitHub repository and the job's `id-token: write`
   permission (already set). Rotate the token before it expires (granular tokens have an expiry date).

7. **PyPI (Python agent `openlog-agent`, optional):** publishing uses PyPI *trusted publishing* (OIDC), so no token is
   stored. On pypi.org (account with 2FA) → *Your projects* → *Publishing* → *Add a new pending publisher* (GitHub):
   PyPI project name `openlog-agent`, owner `onuragtas`, repository `openlog`, workflow name `release.yml`, environment
   name `pypi`. The first successful publish turns the pending publisher into the project's trusted publisher and
   creates the project (add co-maintainers there). In GitHub create the environment `pypi` (Settings → Environments;
   optional: required reviewers, deployment tags `v*`) and set the repository *variable* `PYPI_PUBLISH` = `true`.
   Without the variable, `release.yml` skips the PyPI job with a warning and everything else is released. (The name
   `openlog` is taken on PyPI by an unrelated project, hence `openlog-agent`.)
7. **NuGet (.NET agent `OpenLog.Agent`, optional):** sign in to nuget.org and create an API key (Account → API Keys)
   with the scope *Push new packages and package versions*, glob pattern `OpenLog.Agent` and an expiry of at most
   365 days (the first push creates the package under that account; optionally request the `OpenLog.` ID prefix
   reservation). Store it as the repository *secret* `NUGET_API_KEY`. Without the secret, `release.yml` skips the
   NuGet job with a warning and everything else is released. Rotate the key before it expires.

Delete `openlog-release-key-1.env` from any online machine after step 2.

## Cutting a release

Releases are continuous: every push to `master` whose `ci.yml` run is green is released.

1. `ci.yml` job `release-tag` runs after every other job (none failed or was cancelled; skipped path-filtered jobs
   are fine). It does nothing when the commit is already tagged, when `master` has moved on (the newer push's run
   releases it) or when the commit message contains `[skip release]`.
2. Next version: the highest stable tag `vX.Y.Z` plus a patch bump. `[release minor]` or `[release major]` in the
   commit message bumps the minor or major version instead.
3. When the Go agent modules are not prepared for that version, it runs `scripts/go-agent-release.sh prepare` and
   pushes the commit `release: prepare X.Y.Z (Go agent modules)` to `master` as `github-actions[bot]`. A push with
   `GITHUB_TOKEN` starts no new `ci.yml` run, so this does not loop.
4. It pushes the annotated tag `vX.Y.Z` together with the Go module tags (one atomic push, while `master` still is
   that commit) and starts `release.yml` on it with `workflow_dispatch` (a tag pushed with
   `GITHUB_TOKEN` starts no workflow by itself) and the input `ci_tested=true`.

With `ci_tested=true` (`RUN_TESTS=0`) `release.yml` only builds, signs and publishes: the Java, Node.js, Python and .NET
unit tests, the deb/rpm/apk install test, the PHP deb/apk install test and the Windows MSI install test already passed
in the same commit's `ci.yml` run ("release dry run", "infra-agent (Windows)"). The PHP rpm install test and the
`-version`/`-once` smoke tests of the Windows and macOS release binaries still run. A tag pushed by hand runs every
test. `node-agent-npm` publishes the tarball built by `node-agent-package` (the release asset) instead of packing
again.

Requirements: *Settings → Actions → General → Workflow permissions* allows *Read and write*; branch protection or tag
rulesets on `master` / `v*` must let GitHub Actions push. Nothing else is needed on your side.

Pre-releases and manual releases still work by pushing a tag yourself (e.g. a beta; a manual tag makes the next
automatic release continue from the highest *stable* tag):

```sh
make release-prepare VERSION=0.5.0-beta.1 && git commit -am "release: 0.5.0-beta.1" && git push
git tag -s v0.5.0-beta.1 -m "openlog 0.5.0-beta.1" && git push origin v0.5.0-beta.1   # channel beta, GitHub pre-release
```

Commit with `[skip release]` in the message so the preparation push is not released automatically as well.

`release.yml` then:

1. `prepare` – version/channel from the tag; fails early if the key variable/secret are missing.
2. `go-agent-modules` – fails the release (before anything is built) unless the tagged commit is prepared:
   `scripts/go-agent-release.sh check` and `verify` (see [Go agent modules](#go-agent-modules)).
3. `image` – builds `linux/amd64,linux/arm64` with version + compiled-in keys, pushes
   `ghcr.io/onuragtas/openlog:<v>` (and `:latest` for stable), outputs the digest. In parallel,
   `php-agent-artifacts` builds the PHP agent artifacts and `java-agent-jar` the Java agent jar
   (see [Java agent jar](#java-agent-jar)); both are handed to `release` as workflow artifacts.
4. `release` – `make web`, collects previous releases that have a `manifest.json` via the GitHub API,
   `make release-local VERSION=<v> RELEASE_IMAGE=ghcr.io/onuragtas/openlog@<digest> …`
   (build → sign → `verify --check-artifacts` → index over all releases → sign → verify), installs
   the amd64 deb/rpm in containers, creates the GitHub release as a draft, uploads everything and
   publishes it (stable: marked *latest*; beta: pre-release). For a beta, `index.json(.sig)` is also
   re-uploaded to the latest stable release, because `releases/latest/download/index.json` (the
   default index URL) serves the latest *stable* release.

5. `go-agent-tags` – after the GitHub release is published, pushes the Go module tags on the tagged
   commit (atomic push; see below) unless `release-tag` already pushed them, then asks `proxy.golang.org` for
   them (best effort).

6. `node-agent-npm` – after the GitHub release is published, publishes the `openlog-node-<v>.tgz` built by
   `node-agent-package` (the release asset) as `openlog-node@<v>` to npm (see [Node.js agent package](#nodejs-agent-package)).

- `python-agent-pypi` – after the GitHub release is published, publishes the wheel and sdist built by
  `python-agent-package` (the release assets) as `openlog-agent==<v>` to PyPI with trusted publishing (see
  [Python agent package](#python-agent-package)).

7. `dotnet-agent-nuget` – after the GitHub release is published, pushes the `OpenLog.Agent.<v>.nupkg` built by
   `dotnet-agent-package` (the release asset, with its `.snupkg`) to nuget.org (see [.NET agent package](#net-agent-package)).

The three registry jobs are optional. `node-agent-package`, `python-agent-package` and `dotnet-agent-package` run before
`release` in every release (tests included) and their files are always attached to the GitHub release, so the agents
install without npm, PyPI or nuget.org ([Language agent packages](#language-agent-packages-github-release)).

- `helm-oci` – optional (repository variable `HELM_OCI_PUSH` = `true`, otherwise skipped with a warning): after the
  GitHub release is published, downloads `openlog-<v>.tgz` and `openlog-agent-<v>.tgz` from it, checks their sha256
  against the signed `manifest.json` and pushes them to `oci://ghcr.io/onuragtas/charts` (see
  [Helm charts](#helm-charts)).

Nothing is published if any step fails; a failed draft can be deleted and the tag re-pushed. A failed
`go-agent-tags` job can simply be re-run: tags that already point at the commit are skipped.

### Go agent modules

The Go agent (`agents/go`) is a set of Go modules, so it is released by **Go module tags on the same
commit** as the product tag (D-025). Go libraries are compiled by the user's build (no `-ldflags`), and
a module tag must point at a commit whose files already say the version. So the version bump is a
normal, reviewed commit **before** tagging; CI creates no commits.

`make release-prepare VERSION=X.Y.Z` (`scripts/go-agent-release.sh prepare`):

1. `make -C agents/go set-version` → `const Version = "X.Y.Z"` in `agents/go/version.go`
   (`telemetry.distro.version`);
2. every require of an in-repo module in `agents/go/**/go.mod` (today: `instrumentation/chi` → core,
   `examples` → core + grpc) → `vX.Y.Z` (`go mod edit -require`). The `replace` directives stay: they
   apply only to the main module (development and CI in this repository) and consumers ignore them,
   so `go.sum` needs no change and the tree keeps building offline;
3. runs `check`. Re-running is idempotent. Versions `>= 2.0.0` are refused (module paths would need a
   `/v2` suffix).

Checks:

| Check | Where | Fails when |
|---|---|---|
| `scripts/go-agent-release.sh check X.Y.Z` (`make go-agent-release-check`) | `release.yml` `go-agent-modules` | `version.go` ≠ tag, an in-repo require ≠ `vX.Y.Z`, or a require has no local `replace` |
| `scripts/go-agent-release.sh verify [X.Y.Z]` (`make go-agent-verify`) | `release.yml` `go-agent-modules`, `ci.yml` `go-agent` | a module that requires in-repo modules does not build **without** its `replace` directives |

`verify` writes a `GOPROXY=file://` directory with every `agents/go` module at the version, built from
git-tracked files like the proxy's zip of a tag (`scripts/gomodproxy`, nested modules excluded). Then it
copies each dependent module, drops its `replace` directives, and runs `go build ./...` with
`GOFLAGS=-mod=mod GOPROXY=file://<local proxy>,file://<module cache>/cache/download,off` in a throwaway
`GOMODCACHE`. This is deliberately not literally `GOPROXY=off`: the unreleased in-repo versions exist
only in the local proxy, and third-party modules come read-only from the module cache that
`go mod download` filled first. Nothing is fetched from the network, and the real module cache never
receives the unreleased versions. Without an argument, the version is the one the requires use, so the
ci.yml job also covers master between releases.

Tags (`release.yml` `go-agent-tags`, list from `scripts/go-agent-release.sh tags X.Y.Z`):
`agents/go/vX.Y.Z` and `agents/go/instrumentation/{grpc,chi,gin,echo}/vX.Y.Z` on `$GITHUB_SHA`. `examples`
is not tagged. Continuous releases push them with the product tag (`ci.yml` `release-tag`): pushed later with
`GITHUB_TOKEN`, GitHub refuses a tag whose commit lacks workflow changes that `master` already has ("refusing to
allow a GitHub App to create or update workflow"). For a tag pushed by hand they are pushed only after the release is
published, because the Go module proxy caches a version forever and a module tag must never move. A tag that already exists on another commit fails
the job and is never moved. Pre-releases get tags too (`agents/go/v0.5.0-beta.1`). Go treats them as
pre-release versions, and `@latest` ignores them. Tags pushed with `GITHUB_TOKEN` trigger no workflow. If
tag rulesets cover `agents/**`, allow GitHub Actions to create those tags.

Consumers: `go get github.com/onuragtas/openlog/agents/go@vX.Y.Z` (+ `…/instrumentation/<name>@vX.Y.Z`).

Between releases, `agents/go/version.go` keeps the last prepared version, so `go get …@master`
pseudo-versions report it as `telemetry.distro.version`.

### Language agent packages (GitHub release)

Every release attaches the Node.js, Python and .NET agent packages to the GitHub release, whether or not the registry
jobs below have credentials, so users never depend on npm, PyPI or nuget.org accounts of the project:

| Asset | Built by (`release.yml` job, script) | Manifest | Install without the registry |
|---|---|---|---|
| `openlog-node-<v>.tgz` + `.sha256` | `node-agent-package`: `agents/node/scripts/release-pack.sh` (`npm version`, `npm ci`, `npm test`, `npm pack`) | `node-agent` / `tgz` | `npm install https://github.com/onuragtas/openlog/releases/download/v<v>/openlog-node-<v>.tgz` |
| `openlog_agent-<pep440 v>-py3-none-any.whl` + sdist `openlog_agent-<pep440 v>.tar.gz`, each + `.sha256` | `python-agent-package`: `agents/python/scripts/release-dist.sh` (PEP 440 version, tests, `python -m build`, `twine check --strict`) | `python-agent` / `whl` (sdist: not in the manifest) | `pip install https://github.com/onuragtas/openlog/releases/download/v<v>/openlog_agent-<pep440 v>-py3-none-any.whl` |
| `OpenLog.Agent.<v>.nupkg` + `.sha256` | `dotnet-agent-package`: `agents/dotnet/scripts/release-nupkg.sh` (unit tests, `dotnet pack`) | `dotnet-agent` / `nupkg` | download into a folder, `sha256sum -c`, `dotnet nuget add source <folder> -n openlog-local`, `dotnet add package OpenLog.Agent --version <v>` (no `--source`: it would limit the restore to the folder and lose the OpenTelemetry dependencies) |

The three jobs run in parallel with `image`, before `release`, which downloads their workflow artifacts into
`dist/v<v>/`; `build-manifest` lists the tgz, whl and nupkg (`os`/`arch` `any`), so they are signed and checked by
`verify --check-artifacts` like the Java jar. The `.snupkg` symbols package stays a workflow artifact
(`dotnet-agent-symbols`) for the NuGet push. The registry jobs publish exactly these files (after `sha256sum -c`), so
the registry and the release serve the same bytes.

The Add data page (`web/src/lib/install-commands.ts`) installs from the registry only when `GET /api/v1/onboarding`
reports `agent_packages.<lang>.registry = available` (the API asks npm, PyPI and the nuget.org flat container, 3 s
timeout, cached for an hour; unreachable registries are `unknown`), and from these release assets otherwise.

Locally: `make release-language-agents VERSION=X.Y.Z` (node:22-alpine, python:3.12-slim, dotnet/sdk:8.0; `RUN_TESTS=0`
skips the tests; `NODE_BUILD=local` / `PY_BUILD=local` / `DOTNET_BUILD=local` use host toolchains) before
`make release-local VERSION=X.Y.Z …`. The CI job `release dry run` builds the three packages and fails unless they are in
the dry-run manifest.

### Node.js agent package

The Node.js agent (`agents/node`) is published to npm as `openlog-node` at the product version (D-025, D-062). It was
`@openlog/node` until 0.1.20: the npm name `openlog` (users and organizations share one namespace) is taken, so the
package is unscoped; no npm organization is needed, the `NPM_TOKEN` owner publishes it. The OpenTelemetry scope names
`@openlog/node/runtime` and `@openlog/node/console` are unchanged (stored telemetry keeps matching). Unlike the Go
modules it needs **no preparation commit**: npm publishes built files. `node-agent-package` runs
`agents/node/scripts/release-pack.sh X.Y.Z` (`npm version X.Y.Z --no-git-tag-version`, written into `src/version.ts`
and reported as `telemetry.distro.version`; `npm ci`; `npm test` unless `RUN_TESTS=0`; `npm pack`), and
`node-agent-npm` (after the `release` job) publishes exactly that tarball, the release asset, with
`npm publish --access public --provenance --tag <latest|beta>` (`vX.Y.Z-beta.N` → dist-tag `beta`, so
`npm install openlog-node` keeps resolving the latest stable version). An already published version is skipped
(npm versions are immutable, so re-running the job is safe; a broken version must be deprecated with
`npm deprecate openlog-node@X.Y.Z "<reason>"` and fixed by the next release). Between releases,
`agents/node/package.json` keeps the last released version.

Setup: [one-time setup](#one-time-setup-repository-owner) step 6 (`NPM_TOKEN`). Local check of the package contents:
`make -C agents/node build` then `npm pack --dry-run` in `agents/node` (the CI job does the same).

### Python agent package

The Python agent (`agents/python`, D-073) is published to PyPI as `openlog-agent` at the product version (D-025). No
preparation commit is needed: `release.yml` job `python-agent-pypi` runs after the `release` job and
- converts the tag version to PEP 440 (`X.Y.Z-beta.N` → `X.Y.ZbN`, `-rc.N` → `rcN`; pip installs pre-releases only with
  `--pre`, so `pip install openlog-agent` keeps resolving the latest stable version) and writes it into
  `src/openlog_agent/version.py` (reported as `telemetry.distro.version`);
- installs the agent with the test requirements on Python 3.12, runs the unit and end-to-end tests, builds the pure
  Python wheel and sdist (`python -m build`) and checks them with `twine check --strict`;
- publishes with `pypa/gh-action-pypi-publish` using trusted publishing: the job runs in the GitHub environment `pypi`
  with `id-token: write`, PyPI exchanges the OIDC token for a short-lived upload token, no secret is stored. PyPI
  attestations (PEP 740) are generated by the action.

An already published version is skipped (PyPI versions and file names are immutable, so a re-run is safe; a broken
version is *yanked* on pypi.org and fixed by the next release). Without the repository variable `PYPI_PUBLISH=true` the
job only warns. Between releases, `agents/python/src/openlog_agent/version.py` keeps the last released version.

Setup: [one-time setup](#one-time-setup-repository-owner) step 7 (pending trusted publisher on PyPI, environment `pypi`,
variable `PYPI_PUBLISH`). Local check of the package: `make -C agents/python build` (wheel and sdist in
`agents/python/dist/`); the CI job `python-agent` builds and checks them on every change.

### Java agent jar

The Java agent (`agents/java`, D-072) is released as `openlog-javaagent-X.Y.Z.jar` plus `openlog-javaagent-X.Y.Z.jar.sha256`
(`sha256sum` format) and is listed in the signed `manifest.json` as component `java-agent`, format `jar`, `os`/`arch`
`any` ([releases-updates.md §2](../contracts/releases-updates.md)). No preparation commit is needed. `release.yml` job
`java-agent-jar` runs in parallel with `image` and `php-agent-artifacts`, before `release`:
- `JAVA_BUILD=local agents/java/scripts/release-jar.sh X.Y.Z dist/java` builds with
  `./gradlew -Pversion=X.Y.Z :extension:test :agentJar :agentJarChecksum` (the version becomes
  `telemetry.distro.version` and the `Openlog-Javaagent-Version` jar manifest attribute) and checks the `.sha256`;
- uploads both files as the workflow artifact `java-agent`. The `release` job downloads them into `dist/v<v>/` before
  `make release-local`, so `build-manifest` hashes the jar, it is signed with everything else, `verify --check-artifacts`
  checks it, and both files are published with the GitHub release. The jar is built reproducibly, with fixed timestamps
  and file order.

`agents/java/gradle.properties` keeps `version=0.0.0-dev` between releases. The infra agent's update pipeline ignores the
`java-agent` component (it installs only `infra-agent`); the manifest entry lets users and tooling verify a downloaded
jar against the signed release instead of the loose `.sha256` file. The CI job `release dry run` builds the jar the same
way and fails unless it is in the dry-run manifest. Publishing to Maven Central
(`io.github.onuragtas.openlog:openlog-javaagent`: Sonatype namespace, signing key, `maven-publish`) is future work.

Local release with the jar: `make release-java-agent VERSION=X.Y.Z` (Gradle in `eclipse-temurin:21-jdk`; `JAVA_BUILD=local`
uses the host JDK) before `make release-local VERSION=X.Y.Z …`, like `make release-php-agent`.

### .NET agent package

The .NET agent (`agents/dotnet`) is published to nuget.org as `OpenLog.Agent` at the product version (D-025, D-074).
No preparation commit is needed: `release.yml` (`dotnet-agent-nuget`, after the `release` job) installs the .NET 8
and 9 SDKs, runs the unit tests and `dotnet pack -p:OpenLogVersion=X.Y.Z` (the package version, the assembly
informational version and `telemetry.distro.version`), then `dotnet nuget push --skip-duplicate` (the `.snupkg`
symbols package is pushed with it). `vX.Y.Z-beta.N` becomes a NuGet prerelease, so `dotnet add package OpenLog.Agent`
keeps resolving the latest stable version. NuGet versions are immutable: a re-run skips an existing version, and a
broken version is unlisted or deprecated on nuget.org and fixed by the next release. Between releases,
`agents/dotnet/Directory.Build.props` keeps the last released version.

Setup: [one-time setup](#one-time-setup-repository-owner) step 7 (`NUGET_API_KEY`). Local check of the package:
`make -C agents/dotnet pack VERSION=X.Y.Z` (the CI job `dotnet-agent` packs on every run).

### Helm charts

`make release-helm` (part of `release-local`) packages `deploy/helm/openlog` and `deploy/helm/openlog-agent` with
`helm package --version <v> --app-version <v>` into `dist/v<v>/openlog-<v>.tgz` and `openlog-agent-<v>.tgz`;
`Chart.yaml` in the repository keeps its placeholder version. Without a local `helm` it runs the pinned `HELM_IMAGE`
(`alpine/helm`) container; CI installs helm with `azure/setup-helm` and sets `RELEASE_REQUIRE_HELM=1`.
`build-manifest` records both in the signed manifest (`helm_charts`, sha256; `helm_chart` stays the `openlog`
chart, [releases-updates.md](../contracts/releases-updates.md) §2), `verify --check-artifacts` checks them, and
`release.yml` uploads them with every other file of `dist/v<v>/` to the GitHub release.

OCI registry (optional): set the repository *variable* `HELM_OCI_PUSH` = `true`. The `helm-oci` job then pushes both
charts, taken from the published release and checked against its manifest, to `oci://ghcr.io/onuragtas/charts` with
the job's `GITHUB_TOKEN` (`packages: write`; workflow permissions as in [one-time setup](#one-time-setup-repository-owner)
step 3). New GHCR packages are private: make `charts/openlog` and `charts/openlog-agent` public after the first push.
Without the variable the job only warns. A re-run pushes the same bytes again (OCI tags are overwritten).

```sh
helm install openlog oci://ghcr.io/onuragtas/charts/openlog --version 0.4.0 -n openlog --create-namespace -f my-values.yaml
helm install openlog-agent oci://ghcr.io/onuragtas/charts/openlog-agent --version 0.4.0 -n openlog-agent -f agent-values.yaml
# or from the release asset
helm install openlog https://github.com/onuragtas/openlog/releases/download/v0.4.0/openlog-0.4.0.tgz -n openlog -f my-values.yaml
```

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
OK helm chart openlog-agent-0.4.0.tgz 41be…
```

Check the version and keys a binary was built with: `openlog-infra-agent -version`, `openlog-api -version`.

## Key rotation (two active keys)

Binaries accept a manifest if **any** signature line verifies with **any** compiled-in key (at most
2 active keys). Rotation must never leave deployed agents without a key they trust:

1. Generate key 2 offline (`openlog-release keygen`).
2. Set `OPENLOG_RELEASE_PUBLIC_KEYS=<key1>,<key2>`, add secret `OPENLOG_RELEASE_SIGNING_KEY_2=<seed2>`,
   and add key 2 to `release-public-keys.txt` (the default for source builds of the backend image).
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
- The backend `Dockerfile` defaults to the official keys in `release-public-keys.txt` when the build arg is
  empty. Other binaries (`make build`, agents) built **without** `OPENLOG_RELEASE_PUBLIC_KEYS` trust nothing; tests can instead pass the
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
- Useful variables: `RELEASE_ARCHES=amd64` (faster), `HELM=/path/to/helm` (without helm the charts are packaged in
  the pinned `HELM_IMAGE` container; they are skipped with a warning only when Docker is missing too), `NFPM=nfpm` (local nfpm instead of Docker), `RELEASE_CHANNEL`,
  `RELEASE_COMPAT`, `RELEASE_IMAGE`, `RELEASE_INDEX_ARGS="--entry 0.8.0=https://…/manifest.json"`.
- The backend tarballs embed the placeholder UI unless `make web` ran before.
- Checks: `make package-test VERSION=0.9.0` (deb in debian:12, rpm in rockylinux/rockylinux:9, arch of the
  Docker host), `packaging/test/install.sh dist 0.9.0 0.10.0-beta.1` (install.sh in debian:12,
  ubuntu:24.04, rockylinux/rockylinux:9, alpine; needs a release built with
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

## install-server.sh

```sh
curl -fsSL https://github.com/onuragtas/openlog/releases/latest/download/install-server.sh |
  sudo sh -s -- --email you@example.com
```

Installs the `single` Compose profile in `/opt/openlog-server` with `OPENLOG_IMAGE=ghcr.io/onuragtas/openlog:<v>`
and `openlog-updater` in `auto` mode (flags: README "Quick install"). `make release-local` copies it next to
`install.sh`; like `install.sh` it is not in the manifest.

The compose files come from the release asset `openlog-compose-<v>.tar.gz` (manifest component `compose`, built by
`make release-compose` from the git-tracked `deploy/compose` with `x-openlog-compose-version` stamped): the script reads
the release's `manifest.json` (the index entry's `manifest_url`, else `…/releases/download/v<v>/manifest.json`), downloads
the asset from its `url` and checks the `sha256`. A release without the asset (older than 0.1.22) falls back to
`deploy/compose/` of the tag's source archive (`https://github.com/onuragtas/openlog/archive/refs/tags/v<v>.tar.gz`).
`--bundle-url` points it at a mirror (a `tar.gz` containing `openlog-compose-<v>/` or `deploy/compose/`). Trust: HTTPS
for this bootstrap (the manifest signature is not checked by the shell script); `openlog-updater` verifies the signed
manifest and keeps the files at the installed version (`.bundle-version`, docs/operations/upgrading.md "Compose
files"). `make release-prepare` also stamps `x-openlog-compose-version` in the repository, and `go-agent-release.sh
check` requires it, so a clone of the tag reports its version too. The `build:` section of the compose file is unused
(the release image is pulled, never built).

Port settings in `.env` may be `HOST:PORT` (`OPENLOG_ADMIN_PORT=127.0.0.1:9464`): readiness and port checks connect to
the bound address, URLs use the port only. `packaging/test/install-server-ports.sh` checks these helpers without Docker.

Test: `packaging/test/install-server.sh [VERSION]` runs it in a privileged `docker:28-dind` container against
the published release (fresh install, login, idempotent re-run, upgrade to the latest release, refused
downgrade). It needs internet access and is not part of CI.

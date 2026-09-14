# Contract: Releases, Signed Manifests and Agent Updates (v1)

Plan and rationale: [../plan/09-releases-updates.md](../plan/09-releases-updates.md). This document is binding for the release tooling, agents and backend.

## 1. Versions

- Single SemVer 2.0 product version for every component, git tag `v<version>` (e.g. `v0.4.0`, `v0.5.0-beta.1`).
- Build injects it with `-ldflags "-X github.com/onuragtas/openlog/internal/version.Version=<version> -X …Commit=<sha> -X …Date=<RFC3339>"` (agent: `github.com/onuragtas/openlog/agents/infra/internal/version`). Dev builds report `0.0.0-dev+<commit>`.
- Pre-release versions belong to channel `beta`; plain versions to `stable` (and are also visible in `beta`).
- Comparisons use SemVer precedence. Build metadata is ignored.

## 2. Release manifest

`manifest.json` (UTF-8, no BOM), signed as raw bytes:

```json
{
  "schema": 1,
  "product": "openlog",
  "version": "0.4.0",
  "channel": "stable",
  "released_at": "2026-10-01T12:00:00Z",
  "notes_url": "https://github.com/onuragtas/openlog/releases/tag/v0.4.0",
  "compatibility": {
    "min_backend_for_agent": "0.2.0",
    "oldest_supported_agent": "0.2.0",
    "min_upgrade_from": "0.3.0",
    "rollback_floor": "0.3.0"
  },
  "artifacts": [
    {"component": "infra-agent", "os": "linux", "arch": "amd64", "format": "tar.gz",
     "name": "openlog-infra-agent_0.4.0_linux_amd64.tar.gz",
     "url": "https://github.com/onuragtas/openlog/releases/download/v0.4.0/openlog-infra-agent_0.4.0_linux_amd64.tar.gz",
     "sha256": "…64 hex…", "size": 4123456}
  ],
  "images": {"openlog": "ghcr.io/onuragtas/openlog@sha256:…"},
  "helm_chart": {"name": "openlog-0.4.0.tgz", "url": "…", "sha256": "…"},
  "helm_charts": {"openlog": {"name": "openlog-0.4.0.tgz", "url": "…", "sha256": "…"},
                  "openlog-agent": {"name": "openlog-agent-0.4.0.tgz", "url": "…", "sha256": "…"}},
  "migrations": {"postgres": {"latest": 3, "contract_pending": []}, "clickhouse": {"latest": 7, "contract_pending": []}}
}
```

- `component` values: `infra-agent`, `backend`, `php-agent` (php-agent.md §7.1: `openlog-php-agent_<v>_linux_<arch>.tar.gz`
  with the modules of every PHP ABI and `openlog-php-install`, plus `.deb`/`.rpm`/`.apk`), `java-agent`
  (`openlog-javaagent-<v>.jar`, platform independent: `os` and `arch` are `any`), `node-agent`
  (`openlog-node-<v>.tgz`, `npm pack` of `@openlog/node`), `python-agent` (`openlog_agent-<pep440 v>-py3-none-any.whl`)
  and `dotnet-agent` (`OpenLog.Agent.<v>.nupkg`), all three with `os`/`arch` `any`. `format`: `tar.gz`, `deb`, `rpm`,
  `apk`, `jar`, `tgz`, `whl`, `nupkg`. Consumers ignore components and formats they do not know, so new ones do not
  change `schema`.
- Helm charts: `helm_charts` maps the chart name (`openlog`, `openlog-agent`) to the packaged chart
  `<chart>-<version>.tgz` (chart `version` = `appVersion` = release version); `name`, `url` and `sha256` are required.
  `helm_chart` is kept and always equals `helm_charts.openlog`, for consumers that predate `helm_charts`. Both are
  optional (a release built without helm has neither). `helm_charts` was added without a `schema` change: manifests
  are decoded without rejecting unknown fields, so older binaries verify and use such manifests unchanged (the
  signature covers the raw bytes, including the new field).
- Tarball layout: a single top-level directory `openlog-infra-agent_<v>_linux_<arch>/` containing `openlog-infra-agent`, `LICENSE`, `README.md`, `packaging/`.
- `compatibility.min_upgrade_from`: agents/backends older than this must upgrade through an intermediate version.
- `compatibility.rollback_floor`: the lowest version a rollback may target from this version.

Infra agent artifacts per platform (D-104): `linux` amd64/arm64 `tar.gz`, `deb`, `rpm`; `darwin` amd64/arm64 `tar.gz` (optional `pkg`); `windows` amd64/arm64 `zip` and windows amd64 `msi`. The archives contain the single top-level directory `openlog-infra-agent_<v>_<os>_<arch>/` (binary `openlog-infra-agent`, on Windows `openlog-infra-agent.exe`). Self-updates use `tar.gz` on Linux and macOS and `zip` on Windows (rule 3 looks up that format); `deb`, `rpm`, `msi` and `pkg` are for installers only. Consumers ignore formats they do not know.


### Signature

- `manifest.json.sig`: text file with one or more lines `openlog-sig-v1 <key_id> <base64 ed25519 signature of manifest.json bytes>`.
- `key_id` = first 8 bytes of SHA-256 of the raw 32-byte public key, lowercase hex (16 chars).
- A manifest is valid if **any** line verifies with a trusted key. Verification code: Go module `github.com/onuragtas/openlog/libs/release` (Apache-2.0, used by agents, backend and release tooling).
- **Trusted keys** (public, not secret) are compiled in at build time: `-X <module>/internal/release.trustedKeys=<base64>,<base64>` (backend `github.com/onuragtas/openlog/internal/release`, agent `github.com/onuragtas/openlog/agents/infra/internal/release`). CI takes them from the repository variable `OPENLOG_RELEASE_PUBLIC_KEYS`. At most 2 active keys.
- An additional key file (one base64 key per line) can be configured for tests and self-built distributions: backend `OPENLOG_RELEASE_TRUSTED_KEYS_FILE`, agent `release.trusted_keys_file`. **Binaries built without compiled keys and without a key file cannot apply or advertise updates** (sync still works; update requests are reported as `failed` with `error="no trusted release keys"`).
- The private key never lives in the repo. CI reads it from the secret `OPENLOG_RELEASE_SIGNING_KEY` (base64 32-byte seed). Keys are generated by `openlog-release keygen`; dev/test keys must never be used for real releases.

### Index

`index.json` + `index.json.sig` (same signature format) lists releases per channel, newest first:

```json
{"schema": 1, "product": "openlog", "generated_at": "…",
 "channels": {"stable": [{"version": "0.4.0", "manifest_url": "…/v0.4.0/manifest.json"}],
              "beta":   [{"version": "0.5.0-beta.1", "manifest_url": "…"}]}}
```

Default location: `https://github.com/onuragtas/openlog/releases/latest/download/index.json` (attached to every release, so `latest` always serves the newest). Configurable via `OPENLOG_RELEASE_INDEX_URL`. The index is only a hint. Every manifest is verified on its own.

## 3. Agent ↔ backend: `POST /v1/openlog/agent/sync`

Served by `openlog-ingest` on the OTLP HTTP port, same auth as OTLP (`openlog-license-key` / Bearer). JSON.

### Request

```json
{
  "host_id": "…", "host_name": "web-1",
  "agent": {"name": "openlog-infra-agent", "version": "0.3.0", "commit": "abc123",
            "os": "linux", "arch": "amd64",
            "install_method": "tarball|deb|rpm|container|dev",
            "update_capable": true},
  "update": {"state": "idle|downloading|verifying|staged|restarting|confirming|succeeded|failed|rolled_back",
             "from_version": "0.2.0", "to_version": "0.3.0", "error": "", "changed_at": "…"},
  "config_hash": "sha256:…",
  "integrations_config_revision": "sha256:…"
}
```

`integrations_config_revision`: revision of the remote integration config the agent has applied (`""` = none;
`"disabled"` when `integrations.remote_config: false` in the agent config). Stored on the host (`agent_hosts`, at
most 128 bytes) and shown by `GET /api/v1/integrations/settings?host_id=` as `applied_revision`.

### Response

```json
{
  "poll_interval_seconds": 300,
  "server_version": "0.4.0",
  "update": null,
  "integrations_config": null
}
```

Or, when the policy selects this host for an update:

```json
{
  "poll_interval_seconds": 300,
  "server_version": "0.4.0",
  "update": {
    "action": "upgrade|rollback",
    "target_version": "0.4.0",
    "manifest": "<base64 of manifest.json bytes>",
    "signature": "<contents of manifest.json.sig>",
    "download_url": "https://github.com/…/openlog-infra-agent_0.4.0_linux_amd64.tar.gz",
    "rollout_id": "…",
    "not_before": "2026-10-01T02:00:00Z",
    "deadline": "2026-10-01T04:00:00Z"
  }
}
```

- `download_url` may point to the release source or to a backend mirror (`/v1/openlog/releases/<version>/<name>` on ingest). The agent verifies against the **signed manifest's** sha256 and size regardless of URL.
- `poll_interval_seconds`: server-controlled, clamped by the agent to [60, 3600], ±10% jitter. Ingest answers
  `OPENLOG_FLEET_SYNC_INTERVAL` (300 s), and `OPENLOG_FLEET_ROLLOUT_SYNC_INTERVAL` (60 s) to hosts that an active
  rollout will update in a later wave (decision `not_in_wave`), so the next wave or "Deploy now"
  (`POST /api/v1/fleet/rollouts/{id}/deploy-now`) reaches them within about a minute plus the policy cache TTL.
- Unknown fields are ignored on both sides. `404` or `501` from an old backend → the agent disables sync until restart and logs once.

#### Remote integration config

When the host's effective integration settings (edited under `/api/v1/integrations/settings`, stored in
`integration_settings`) have a revision other than the request's `integrations_config_revision`, the answer carries them:

```json
"integrations_config": {
  "revision": "sha256:4f1c…",
  "items": [
    {"integration": "redis", "enabled": true, "endpoint": "127.0.0.1:6379", "username": "default",
     "password": "<plaintext>", "database": "", "databases": []},
    {"integration": "nginx", "match": {"port": 8080, "container": "", "endpoint": "", "instance": ""},
     "enabled": true, "endpoint": "http://127.0.0.1:8080/nginx_status", "username": "", "password": "",
     "database": "", "databases": []}
  ]
}
```

- `null` (or absent, from older servers) = keep the current config. It is also `null` when the revision is unchanged,
  with `OPENLOG_AUTH_MODE=static`, while PostgreSQL is unavailable and nothing is cached, or when a password cannot be
  decrypted (ingest logs a warning without secrets; `openlog_agent_sync_integrations_config_total{result="error"}`).
- `items` is the complete remote config (it replaces the previous one; `[]` = no remote settings) in application order:
  settings for all hosts first, then settings for this host; within each group settings without `match` first; ties
  by creation time. `match` is omitted when the setting applies to every instance; `port` is omitted when unset.
  Fields an integration does not use are empty.
- Revision = `"sha256:"` + hex sha256 of the JSON `items` array with each password replaced by its stored ciphertext,
  so it changes with every change, including a new password (re-encrypting with a new key changes it too).
- The agent applies the items, persists them with the revision (mode 0600) and reports the revision in every following
  sync. `"disabled"` never matches, so such agents receive the object on every sync and ignore it.
- Passwords are sent in plaintext inside the (TLS) ingest connection to agents authenticated with the organization's
  license key; at rest they are encrypted in PostgreSQL with `OPENLOG_SECRETS_KEY`, which ingest therefore needs too.
- Changes reach an agent within `OPENLOG_FLEET_POLICY_CACHE_TTL` plus one poll interval.

#### PHP agent

Agents report their PHP runtimes and PHP agent installation in the request (`php_agent`, php-agent.md §7.3 item 8;
bounded by ingest: 64 runtimes, clipped strings) and ingest answers the host's fleet settings (null without a policy
store):

```json
"php_agent": {"mode": "auto", "version": "agent", "reload": "graceful", "exclude_bins": [],
  "target_version": "0.4.0", "manifest": "<base64 of manifest.json>", "signature": "…",
  "download_url": "https://…/openlog-php-agent_0.4.0_linux_amd64.tar.gz", "reason": "offer"}
```

- `mode` is the host's override or the policy's; `target_version` is set with `reason=offer` (with manifest, signature
  and a download URL, the backend mirror when enabled) and `reason=up_to_date` (without them); otherwise it is `""` and
  the agent keeps what is installed. Decision reasons: api.md "Fleet" (`php_agent.status`).
- The agent applies these settings instead of `config.yaml` unless `php_agent.remote_config: false`, and verifies the
  manifest like an update instruction (signature, schema, version, a `php-agent` `tar.gz` artifact for its platform,
  rollback floor of the installed PHP agent for downgrades).

### Agent verification rules (all must pass or the update is rejected and reported as `failed`)

1. Signature valid with a trusted key.
2. `manifest.product == "openlog"`, `schema == 1`, `manifest.version == target_version`.
3. An artifact exists for `component=infra-agent`, own `os`/`arch`, `format=tar.gz`.
4. `action=upgrade`: `target_version > current`, and `current >= compatibility.min_upgrade_from`.
5. `action=rollback`: `target_version < current` and `target_version >= rollback_floor` of the **currently running** version's manifest (kept on disk from its own install/update; if unknown → reject).
6. Downloaded size and sha256 match. Archive extraction rejects absolute paths, `..`, symlinks and files outside the expected directory.
7. `<new binary> -self-test -config <config>` exits 0 within 30 s.
8. `update_capable=false` (container, dev) → never apply; report `failed` with `error="not update capable"` if asked. This rule (and the trusted-keys check) is evaluated right after rule 5, **before** any download or disk write.

Additional agent behavior:
- Container detection: `/.dockerenv`, `/run/.containerenv`, container cgroup, or `OPENLOG_AGENT_CONTAINER=1`; `OPENLOG_AGENT_CONTAINER=0` disables detection (for test setups that run the tarball layout inside a container).
- `update_capable` additionally requires `update.enabled`, trusted keys, an existing `current` symlink and either the privileged pre-start step having run for this start (staged mode) or an install root writable by the agent (legacy mode). For deb/rpm the package must be named `openlog-infra-agent`.
- Optional request fields (older backends ignore them): `agent.update_mode` (`staged` | `legacy` | absent), `agent.update_notice` (operator action, e.g. `"unit outdated: …"` when the unit predates the pre-start step), and `reconcile` `{version, at, unit_changed, docker: added|member|opt_out|no_group|error, php_access: added|member|opt_out|error, error}` from the last reconcile.
- `php_access` (optional; absent when the host has no PHP-FPM pool files): `{socket_group, group, group_exists, agent_member, grants: auto|opted_out, pools: [{pool, php_version, user, unit, access: ok|missing|opted_out|unsupported_user}]}` — which PHP-FPM pools may send to `php.sock` (php-agent.md §1), evaluated by the unprivileged agent from `/etc/group`, `/etc/passwd` and the pool files at every sync (at most 512 pools). `missing` pools are granted by the next reconcile (service restart). The backend stores the last report per host (`agent_hosts.php_access`) and returns it as `FleetHost.php_access`.
- Installers (deb/rpm/`install.sh`) place `manifest.json` and `manifest.json.sig` in `versions/<v>/`; without them rollback from that version is refused (floor unknown). A deb/rpm cannot contain the published manifest (it lists the package's own sha256), so packages embed a second manifest for the same version, signed with the same key, listing only the agent tarballs; `version`, `channel`, `released_at`, `compatibility` and `images` are identical to the published one. `install.sh` tarball installs store the published manifest.
- A failed instruction with the same action/version/rollout is not retried for 1 h; a rolled-back version is never retried automatically.
- Downloads send the license key only when the URL host equals the ingest endpoint host.
- Limitation: in staged mode start attempts are counted by `-apply`, so a candidate whose `main` crashes early still rolls back, as long as the candidate's own `-apply` runs (it ran successfully right after the switch). A candidate whose binary cannot start at all restart-loops (`StartLimitIntervalSec=0`) and needs the next release or manual action.

### Update state machine and rollback

State file `<state_dir>/update-state.json`: `{previous, candidate, attempts, staged_at, confirmed}` plus, in staged mode, `{staged, confirmed_version, confirmed_at, rollback_request, rollback_reason}`.

Staged mode (unit with the privileged pre-start step, see below):
- **Stage (agent, unprivileged):** after rules 1-8 download, extract and self-test inside the sandbox; keep `archive.tar.gz`, `manifest.json`, `manifest.json.sig` in `<state_dir>/updates/<v>/`; write state `{staged: v, candidate: v, previous, staged_at}`; exit 0.
- **Apply (root, next start):** re-verify and install (see below), record the candidate `{version, previous, switched_at, attempts: 1}` in the root-owned `apply-status.json`, switch `current`, run the new binary's `-reconcile`.
- **Startup:** `-apply` increments `attempts` of its recorded candidate on every start (not for the one restart caused by a changed unit). If `attempts > 3`, `now - switched_at > 5 min`, or the running candidate set `rollback_request` (its watchdog: no confirmation within 5 min), it switches `current` back to the recorded `previous` (must be a root-owned tree) and runs its `-reconcile`. The agent maps the result of its start (`apply-status.json` with its `$INVOCATION_ID`: `switched`, `rejected`, `rolled_back`, `rollback_failed`) to the reported state.
- **Confirm:** after the first successful OTLP export on the candidate, set `confirmed`, `confirmed_version`, `confirmed_at`, report `succeeded`. The next `-apply` accepts the confirmation only if `confirmed_at` is after its `switched_at`, forgets the candidate and prunes versions except current + previous (+ the deb/rpm package version).

Legacy mode (unit without the pre-start step; only while `versions/` is writable by the agent):
- **Stage:** extract to `<install_root>/versions/<v>/`, self-test, write state `{candidate, previous, attempts: 0}`, atomically replace the `current` symlink (create temp symlink + rename), exit with code 0 (systemd restarts).
- **Startup (first thing in `main`):** if `candidate == running version` and not `confirmed`, increment `attempts`. If `attempts > 3` or `now - staged_at > 5 min`: switch `current` back to `previous`, set state `rolled_back`, exit (restart into previous).
- **Confirm:** after the first successful OTLP export on the candidate, set `confirmed: true`, report `succeeded`, prune versions except current + previous.
- `install_root` default `/opt/openlog/infra-agent`. Legacy installs without the layout report `update_capable=false` until migrated by the installer/package.

### Privileged apply and reconcile

A self-update must be equivalent to installing the new package or re-running `install.sh`: unit changes, group membership, ownership and future install steps must reach self-updated hosts. The agent runs as `openlog-agent` and must not be able to become root, so it cannot install them itself, and root must never execute anything the agent user could have written.

- **Ownership:** `<install_root>`, `versions/`, every version tree and `current` are `root:root`, not writable by others; the unit gives the agent no write access there (`ReadWritePaths=/var/lib/openlog-infra-agent`). `state_dir` is `openlog-agent` 0750; `config.yaml` `root:openlog-agent` 0640.
- **Unit:** `ExecStartPre=-+/opt/openlog/infra-agent/current/openlog-infra-agent -apply -config /etc/openlog-infra-agent/config.yaml` (`+`: root, without `User=` and the sandbox; `-`: never blocks the start), `TimeoutStartSec=180`. The executed binary is the root-owned current version.
- **`-apply`** (always exits 0; no network; no-op outside the versions layout or when not root): (1) secure the layout: install root and `versions/` root-owned 0755; a version tree containing anything not root-owned, writable by others, a symlink or a special file is untrusted: the current one is copied (directories and regular files only, through `os.Root`) into a root-owned tree, others are deleted; untrusted status files are deleted. (2) Candidate check and rollback (above). (3) Staged update, with every file under `state_dir` treated as hostile: version name must be SemVer, action `upgrade`/`rollback`; files are opened through `os.Root(state_dir)` (symlinks cannot escape), must be regular files (no FIFO) within size limits (manifest 1 MiB, signature 64 KiB, archive = signed size); rules 1-5 with the keys compiled into this binary and its own version/manifest; the archive is copied into `versions/.<v>.partial` and size + sha256 are checked on the copy (the digest is not reported); extraction with rule 6 as root; the extracted tree must be trusted; `-self-test` of the candidate as `openlog-agent` (uid/gid from `/etc/passwd`, no supplementary groups; rule 7); rename to `versions/<v>`; switch. A staged update is processed once (`handled_staged_at`). (4) `-reconcile` of the version that starts (a re-exec after a switch or rollback, so the new release's code runs). The configuration and `release.trusted_keys_file` are used only if they and all parent directories are changeable by root only; otherwise defaults and compiled-in keys.
- **`-reconcile`** (run by `-apply`, deb/rpm postinstall and `install.sh`; idempotent; prints `restart-required` for installers): create the account if missing; secure the layout; fix state dir and config ownership/modes (config content is never written); install the unit embedded in the binary (`agents/infra/packaging/systemd/openlog-infra-agent.service`, the file the packages ship) when its content differs — deb/rpm: the packaged `/usr/lib/systemd/system` path (a package upgrade replaces it and its postinstall reconciles again with the current, possibly newer binary; `dpkg --verify`/`rpm -V` may report it modified while a newer self-updated version runs), skipped when `/etc/systemd/system/openlog-infra-agent.service` fully overrides it; tarball: `/etc/systemd/system`; drop-ins are never touched — then `systemctl daemon-reload`; docker group membership (D-040 rules and opt-outs); the PHP socket group (D-103, php-agent.md §1): create the system group `openlog-php` and add `openlog-agent`, then — unless `php_forwarder.grant_pool_users: false` (root-owned config), `/etc/openlog-infra-agent/no-php-access` or `OPENLOG_AGENT_PHP_ACCESS=0` (recorded in that file) — add every existing non-root PHP-FPM pool user and, with Apache/nginx present, `www-data`/`apache`/`nginx`; members are never removed; only the active PHP-FPM (and Apache) services whose users were newly added get `systemctl reload` (never restart); `/usr/bin` symlink for tarball installs. Result: root-owned `<install_root>/reconcile-status.json` (`php_access: {group, agent, grants, added, failed, reloaded, not_active}`), reported as `reconcile`. Installers print what it did (the step logs to stderr). New pools created later are granted at the next `systemctl restart openlog-infra-agent`; there is no timer.
- **Restart for a new unit:** when `-apply`'s reconcile changed the unit, the agent started in that invocation exits 0 once before running, so systemd restarts it under the new definition. Loop guard: not again for the same unit content written again in the next start. New supplementary groups need no restart (the main process is spawned after `-apply`, and systemd resolves `User=`'s groups with `initgroups` for each exec, so `docker`/`openlog-php` memberships added by the pre-start step apply to the agent of the same start; contexts `package`/`install` print `restart-required` instead).
- **Existing installs (bootstrap):** units from before this step have no `ExecStartPre`; the agent detects that `-apply` did not run for its start (`apply-status.json` `invocation_id` ≠ `$INVOCATION_ID`), keeps the legacy binary-only update while `versions/` is writable, and reports `update_mode: legacy` with `update_notice: "unit outdated: …"`. One package upgrade or `install.sh` run installs the new unit and secures the layout. A current version written by a legacy self-update is not trusted by the installers: postinstall switches to the package version, `install.sh` re-installs the running version from the release source.
- **Residual trust:** the bootstrap trusts the binary that is current when root first runs it (a host whose agent user was already compromised before the migration is not recovered by it). Docker group membership remains root-equivalent (D-040).

### macOS and Windows services (D-104)

launchd and the Windows Service Control Manager have no privileged pre-start hook, so the agent itself runs privileged there and performs the same apply step at the start of the service process:

- **Identity and layout:** macOS: LaunchDaemon `/Library/LaunchDaemons/org.openlog.infra-agent.plist` (`KeepAlive`, environment `OPENLOG_SERVICE_MANAGER=launchd`) running `/opt/openlog/infra-agent/current/openlog-infra-agent` as root; install root, config (`/etc/openlog-infra-agent/config.yaml`, root 0600) and state dir (`/var/lib/openlog-infra-agent`, root 0700) as on Linux; log `/var/log/openlog-infra-agent.log` (newsyslog). Windows: service `openlog-infra-agent` as LocalSystem, automatic (delayed) start, ImagePath `"C:\Program Files\openlog\infra-agent\current\openlog-infra-agent.exe" -config "C:\ProgramData\openlog\infra-agent\config.yaml"`, recovery: restart after 5 s/10 s/30 s, also on non-crash failures; `current` is a directory symbolic link; `C:\ProgramData\openlog\infra-agent` (config, `state\`, `logs\`) has a protected ACL granting only SYSTEM and Administrators.
- **Trust checks:** POSIX ownership/mode checks are unchanged on macOS (root:wheel). Windows replaces them with ACL checks: a trusted file or version tree is owned by LocalSystem, Administrators or TrustedInstaller and its DACL grants write, delete or permission rights to nobody else; parent directories may let other principals create entries (C:\ and C:\ProgramData do) but not delete, rename or re-permission them.
- **Start:** the service process (launchd: `OPENLOG_SERVICE_MANAGER=launchd` and euid 0; Windows: started by the SCM) generates an invocation id, sets `INVOCATION_ID` and runs `Apply` with the keys compiled into the running (trusted) binary: candidate check and rollback, staged update with rules 1–7 (the self-test runs with the service identity), `reconcile` of the version that starts. When it switched to another version or rolled back it exits once (Windows: service specific exit code 3, restarted by the recovery actions; launchd: `KeepAlive`); the next start is the new binary's first attempt (`attempts` counted there) and reports the carried-over apply result (`apply-status.json` `carried: true`). Staged mode, confirmation after the first successful export, `ConfirmWindow`, `MaxStartAttempts` and the rollback request are as above.
- **`-reconcile`:** macOS: root-owned layout, state dir and config ownership, the LaunchDaemon (a changed definition prints `restart-required`; installers `bootout`/`bootstrap`; in the apply context it applies at the next bootstrap), `/usr/local/bin/openlog-infra-agent` link, `/etc/newsyslog.d/openlog-infra-agent.conf`. Windows: service creation or update (`restart-required` when the definition of a running service changed), recovery actions, protected ACLs; context `package` (MSI) points `current` at its own version unless a newer self-updated version is current. No accounts or groups are created on either OS.
- **Install methods:** `tarball` (macOS `install.sh`), `zip` (`install.ps1`), `msi` (registry `HKLM\SOFTWARE\openlog\infra-agent` `InstallMethod=msi`). MSI version directories contain no `manifest.json` (it would have to include the MSI's own digest): upgrades work, a backend-ordered **rollback** below an MSI-installed version fails rule 5 until a self-update installed a version with its manifest; automatic rollback of an unconfirmed candidate is unaffected.
- **Installer verification:** `install.sh` and `install.ps1` check size and sha256 against the manifest fetched over HTTPS; when a trusted agent is already installed they additionally run `<current binary> -verify-release manifest.json -artifact <archive>` (Ed25519 signature with the compiled-in keys) before replacing it.


## 4. Fleet update policy (backend)

Stored in PostgreSQL (see [postgres.md](postgres.md), tables `agent_update_policies`, `agent_update_host_overrides`, `agent_rollouts`, `agent_hosts`).

| Field | Type | Default |
|---|---|---|
| `mode` | `off` · `notify` · `auto` | `auto` |
| `channel` | `stable` · `beta` | `stable` |
| `target` | `latest` · `patch` · `pinned` | `latest` |
| `pinned_version` | SemVer or null | null |
| `waves` | int percentages, strictly increasing, last = 100 | `[10, 50, 100]` |
| `wave_soak_minutes` | int | 60 |
| `halt_failure_rate` | 0..1 | 0.05 |
| `maintenance_windows` | list of `{days: ["mon",…], start: "02:00", end: "05:00"}` UTC; empty = always | `[]` |

- **Wave membership:** `bucket = fnv32a(host_id + ":" + rollout_id) % 100`. A host is eligible when `bucket < waves[current_wave]`.
- **Wave advance:** when `wave_soak_minutes` have elapsed since the wave started and, among hosts that attempted this rollout, `failed + rolled_back < halt_failure_rate × attempted` (minimum 3 attempts before the rate is evaluated; with fewer, advance on soak only).
- **Halt:** the failure rate is exceeded → rollout `halted`, no new hosts get the update, hosts already updated stay on it. Admin can resume or order a rollback (rollback rollout to the previous version, same waves).
- **Host overrides:** `hold` (never update) or `pin` to a version.
- The ingest sync handler reads policy and rollout state through the same pod-local cache pattern as license keys (TTL ≤ 30 s). Rollout state transitions (advance, halt) are computed by a single leader: a Postgres advisory lock held by one `openlog-api` pod.

## 5. Backend version endpoints

- Every HTTP response from ingest and api: `X-Openlog-Version: <version>`.
- `GET /api/v1/version` (authenticated, any role) → `{"version", "commit", "date", "latest_available": {"version", "notes_url", "checked_at"} | null, "update_check": "enabled|disabled|failed"}`.
- The backend checks the index at most every 24 h (`OPENLOG_UPDATE_CHECK=enabled|disabled`, `OPENLOG_RELEASE_INDEX_URL`), and verifies signatures. Leader-only (same advisory lock).
  It verifies the index and the manifest of the newest entry of `OPENLOG_UPDATE_CHANNEL` (for `notes_url`) and stores
  the result in PostgreSQL (`system_state`, key `update_check`); a failed check is retried after 6 h and keeps the
  last good result. Each pod reports `latest_available` only when that release is newer than its own version.
- Additive: the response also has `"updater": {…} | null`, the status of `openlog-updater`
  (docs/operations/upgrading.md, openapi `UpdaterStatus`). `/readyz` bodies contain `"version"`; gRPC responses of
  ingest carry `x-openlog-version` header metadata.
- Additive: `"update_requests": {"can_request", "updater_listening", "updater_polled_at", "latest"} | null` (§5.1).

### 5.1 Update requests ("Check now" / "Update now", D-041)

The UI does not talk to `openlog-updater` directly. The request channel is the PostgreSQL table `update_requests`
(migration `0009`, docs/contracts/postgres.md), which both updater engines already reach with the DSN they use for
their status document:

- `POST /api/v1/version/check` (admin/owner, not with `OPENLOG_SIGNUP_ENABLED=true`) inserts `action=check` and runs
  the api release check synchronously (any pod; stored in `update_check`, so the leader's 24 h schedule restarts).
  `POST /api/v1/version/update {"target_version", "ignore_maintenance_window"}` inserts `action=apply`. Both are
  limited to one request per action per 30 s for the whole installation (`429` + `Retry-After`; a transaction-level
  advisory lock serializes pods); a second `apply` while one is `pending`/`running` → `409`.
- **Compose updater:** polls every `OPENLOG_UPDATER_REQUEST_POLL` (10 s): expires `pending` rows older than 15 min
  (`expired`), claims the oldest `pending` row (`UPDATE … WHERE id = (SELECT … FOR UPDATE SKIP LOCKED)` → `running`),
  handles it and stores `done`/`failed` + `message`. Every 30 s it writes `system_state` key `updater_poll`
  `{engine, mode, poll_seconds, polled_at}`; the api reports `updater_listening` when `polled_at` is at most
  max(90 s, 3 polls) old. On start it marks rows left `running` as `failed` (interrupted).
- **Kubernetes CronJob** (`-k8s -once`): handles all `pending` rows first (a handled request includes the check),
  so requests apply at the next scheduled run; it never writes `updater_poll`.
- `check` = one regular run (mode rules unchanged). `apply` = install now, also in `notify` mode: only if the
  release the updater selects is still `target_version` (else `failed`, "check again"); outside
  `OPENLOG_UPDATER_MAINTENANCE_WINDOW` only with `ignore_maintenance_window`; `OPENLOG_UPDATER_MODE=off` → `failed`.
  A version in `failed_versions` may be retried by an explicit request. The update itself is the normal
  backup → pull → migrate → recreate → health → rollback flow; audit `updater.update_started` carries
  `request_id` and `requested_by`. The api audits `update.check_requested` / `update.apply_requested`.
- The UI polls `GET /api/v1/version` every 2 s while a request is open or `updater.state = updating`, keeps the
  last answer while the api container is recreated ("server is restarting…") and reloads on the new
  `X-Openlog-Version`.

## 6. Migrations for rolling updates

- Every PostgreSQL and ClickHouse migration file declares a phase in its first line: `-- openlog:phase expand` or `-- openlog:phase contract`.
- `openlog-migrate` (default) applies all `expand` migrations and only those `contract` migrations whose `-- openlog:requires-all-at-least <version>` is satisfied by every running component version recorded in the `component_heartbeats` table (pods report version + last_seen). Unknown → skip and log.
- CI job `mixed-version`: deploys release `N`, runs migrate for `N+1`, runs `N` and `N+1` services together, executes the e2e suite.

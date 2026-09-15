# Contract: Java agent distribution and updates (v1)

Decisions: D-072 (OpenTelemetry Java agent distribution), D-123 (updates through the infra agent). Agent:
[`agents/java/README.md`](../../agents/java/README.md). Infra agent code: `agents/infra/internal/javaagent`; backend:
`internal/fleet` (policy section, decision), API: [api.md](api.md) "Fleet".

## 1. Release artifact

Every product release publishes `openlog-javaagent-<v>.jar` and `.sha256` and lists the jar in the signed
`manifest.json` (releases-updates.md §2): `{"component": "java-agent", "os": "any", "arch": "any", "format": "jar"}`.
The jar manifest carries `Premain-Class` and `Openlog-Javaagent-Version: <v>`.

## 2. Distribution and updates through the infra agent (D-123)

The infra agent keeps a stable jar path current, using the release pipeline and trust rules of its own updates
(releases-updates.md §3) and of the PHP agent installation (php-agent.md §7.3). The application is configured once:

```sh
JAVA_TOOL_OPTIONS=-javaagent:/opt/openlog/openlog-javaagent.jar   # e.g. in the PM2 ecosystem file or the systemd unit
```

### 2.1 Configuration

`java_agent` in `config.yaml`:

| Key | Default | Meaning |
|---|---|---|
| `mode` | `manual` | `off`: remove the fleet installation (never a user's own jar) · `manual`: JVM inventory only, never changes files · `auto`: install and upgrade |
| `version` | `agent` | `agent` (the infra agent's own version) or a SemVer version |
| `remote_config` | `true` | the fleet's `mode` and resolved target (sync `java_agent`) replace the local values (persisted in `<state_dir>/java-agent/remote.json`) |
| `health_check_after` | `2m` | time between a switch and its verification (at least `10s`) |
| `install_root` | `/opt/openlog/java-agent` (Windows `C:\Program Files\openlog\java-agent`) | `versions/<v>/openlog-javaagent.jar`, `current` → `versions/<v>` |
| `link_path` | `/opt/openlog/openlog-javaagent.jar` (Windows `C:\Program Files\openlog\openlog-javaagent.jar`) | the stable path used in `-javaagent:`; must end in `.jar` and lie outside `install_root` |

The privileged step uses `install_root` and `link_path` only from a configuration file that only root can change
(otherwise the defaults) and refuses installs when that file says `mode: off` and `remote_config: false`.

### 2.2 Layout and link switch

- `<install_root>/versions/<v>/{openlog-javaagent.jar (0644), manifest.json, manifest.json.sig}`, directories 0755,
  everything root-owned (Windows: owned by SYSTEM/Administrators, nobody else may write); the marker
  `.managed-by-openlog-infra-agent` in the install root; `current` → `versions/<v>` (symlink via temporary link +
  rename; Windows: directory symbolic link).
- **A jar is never changed in place.** Running JVMs keep the file they opened (they read the jar lazily; changing its
  content under them crashes them). A new version is a new file; only the name is switched.
- `link_path`, Linux and macOS: a symbolic link to the absolute `versions/<v>/openlog-javaagent.jar`, created as a
  temporary link in the same directory and renamed over the old link. The directory must be root-owned and not
  writable by others.
- `link_path`, Windows: creating symbolic links is not assumed, so it is a **copy**: written to
  `<link_path>.openlog-new`, then renamed over the old copy (`MoveFileEx(MOVEFILE_REPLACE_EXISTING)`); its sha256 is
  recorded to recognize it as managed. Windows JVMs open jars without `FILE_SHARE_DELETE`, so while any JVM that
  started with the old copy runs, the rename fails: the new version is installed and `current` switched, the link
  stays `pending`, the report says `restart_pending`, and the agent retries every 30 s (in the privileged service
  process, no restart). Stopping those applications lets the switch happen; starting them again before the retry
  loads the old copy once more. For seamless updates on Windows point `-javaagent` at
  `C:\Program Files\openlog\java-agent\current\openlog-javaagent.jar` (the directory link can be switched while jars
  are open).
- **Unmanaged `link_path`**: a regular file (or a link outside `install_root`) that the infra agent did not create —
  e.g. a jar installed by hand with `install -m 0644 … /opt/openlog/openlog-javaagent.jar` — is never replaced or
  removed. The installation into `install_root` still happens; the report says `unmanaged` with guidance: point
  `-javaagent` at `<install_root>/current/openlog-javaagent.jar`, or move the file away (`sudo mv
  /opt/openlog/openlog-javaagent.jar /opt/openlog/openlog-javaagent.jar.manual`; running JVMs keep their open copy) so
  that the next evaluation creates the link. Restart applications only after the link exists: a JVM whose
  `-javaagent` file is missing does not start.
- Kept versions: current, the previous one, every version a running managed JVM still uses, and (Windows) the version
  a pending copy holds; older ones are removed.

### 2.3 Flow

1. **Inventory** (every 5 min and after each change; a change triggers a sync): install state (`current`, marker, link
   state) and the JVMs (§2.4).
2. **Capability**: Linux: the unit's privileged pre-start step ran for this start (`apply-status.json` of the same
   invocation), the binary runs from the versions layout and release keys exist. macOS (LaunchDaemon as root) and
   Windows (service as LocalSystem): the service process is privileged (D-104) and applies requests itself. Not in
   containers or dev builds; otherwise `capable=false` with a `reason`.
3. **Staging** (`mode=auto`, target ≠ current, not rolled back before, a failed target not within 1 h, no self-update
   in progress): the signed manifest comes from the fleet (`java_agent.manifest`) or, locally with `version: agent`,
   from the manifest next to the infra agent binary. Verification: signature by a trusted key, product/schema, manifest
   version = target, a `java-agent` `jar` artifact (`any`/`any`, at most 256 MiB), and for a downgrade target ≥
   `rollback_floor` of the installed version's manifest. Download (size and sha256 of the manifest; license key only
   for the ingest host), the jar must have `Premain-Class` and, when it names one, `Openlog-Javaagent-Version` = target.
   Result: `<state_dir>/java-agent/staged/<v>/{openlog-javaagent.jar, manifest.json, manifest.json.sig}` and
   `request.json` `{id, action: install, version, in_use, at}` (all untrusted).
4. **Hand-over**: Linux: the agent exits once; `-apply` of the next start (after the agent's own update step and the
   PHP agent step) handles the request. macOS/Windows: applied in the running service process.
5. **Privileged install** (once per request id; everything under `state_dir` read through `os.Root` with size limits;
   verification again with the keys compiled into the root-owned binary): the jar is copied into
   `versions/.<v>.partial/` (size and sha256 checked on the copy), checked as a Java agent of `<v>`, stored with the
   manifest and signature, must be a root-owned tree, renamed to `versions/<v>`; marker written; `current` switched;
   `link_path` switched (§2.2); pruning. An existing trusted `versions/<v>` with the signed digest is reused (rollback to
   a kept version). Result: root-owned `<infra install root>/java-agent-status.json` `{request_id, at, action,
   operation, result: applied|failed|rolled_back|uninstalled|rejected, target, version, previous, error, link_state,
   link_version, link_sha256, switches[], link_switches[]}` (`switches`: the last 4 switches of `current` and
   `link_path` with time, used to detect loaded versions); `rejected` = a verification or ownership check failed,
   nothing changed. An install root without the marker is never touched.
6. **Verification** (`health_check_after` after `applied`): `current` points at the version, its jar is the signed
   release (sha256 of the kept manifest) and a Java agent, and a managed link resolves to it. On failure the agent
   requests `rollback`: `current` and `link_path` go back to the recorded previous version (without one the installed
   version stays and the operation is `failed`: removing the jar would stop JVMs from starting). A rolled back version
   is never installed again automatically.
   Not detected automatically: a JVM that fails *with* the new jar (the infra agent does not supervise applications and
   cannot tell an agent failure from an application exit). The fleet rolls back by pinning the policy's
   `java_agent.version` to the previous version (a downgrade within `rollback_floor`; the kept version directory is
   reused), or the operator sets `mode: manual` and points `-javaagent` at a version directory.
7. **Removal**: `mode: off` with the marker requests `uninstall` — refused while a running managed JVM uses the jar
   (reported as `error` with the versions in use, re-evaluated every inventory), then the managed `link_path` and the
   install root are removed. Applications still configured with `-javaagent:<link_path>` will not start afterwards.
8. **Never**: restart, signal or attach to a JVM; change a jar in place; touch a user's jar.

### 2.4 JVM discovery and loaded version

- Linux (`/proc`): processes whose executable or `argv[0]` is `java`, or whose command line contains `-javaagent:`;
  `JAVA_TOOL_OPTIONS`, `JDK_JAVA_OPTIONS` and `_JAVA_OPTIONS` from `/proc/<pid>/environ`; open `.jar` files from
  `/proc/<pid>/fd`; start time from `stat`; working directory; container from the cgroup. `environ`, `fd` and `cwd` of
  other users' processes need `CAP_SYS_PTRACE` (granted by the unit); without it those fields are empty.
- macOS and Windows: `java`/`javaw` processes with command line, start time and (best effort) environment and working
  directory; open files are not read.
- A JVM is reported when one of its `-javaagent:` paths (command line first, then the variables) is `link_path`, inside
  `install_root` or named `openlog-javaagent*.jar`; relative paths resolve against the working directory. At most 64
  JVMs. Command lines are masked like the process inventory.
- `managed`: the path is a managed `link_path` or inside `install_root`. Container processes are never managed (their
  paths belong to the container).
- `loaded_version`, in order: (1) Linux: an open `versions/<v>/openlog-javaagent.jar` (also `(deleted)`), or for an
  unmanaged path the open file's manifest; (2) a path naming `versions/<v>`; (3) managed `link_path`/`current`: the
  switch history — the version the path pointed at when the process started; (4) unmanaged: the jar's
  `Openlog-Javaagent-Version` when the file did not change after the process started. Otherwise `""` (unknown).
- `restart_pending`: managed, not pinned to a version directory, and the loaded version differs from `current` — or,
  when unknown, the process started before the path was switched to `current`.

### 2.5 Report

`java_agent` of the sync request: `mode`, `source` (`local`|`remote`), `capable`, `reason`, `managed`,
`current_version`, `target_version`, `status` (`installed` | `staged` (download or hand-over in progress) |
`restart_pending` (JVMs run an older version, or the Windows link is locked) | `unmanaged` (`link_path` is a user's
file) | `error` (last operation failed, removal blocked by running JVMs, link not writable) | `not_found`), `detail`,
`link_path`, `link_state` (`missing`|`managed`|`unmanaged`|`pending`|`error`), `jvms[] {pid, name, command,
agent_path, loaded_version, managed, restart_pending, started_at, container}`, `update {operation:
install|upgrade|rollback|switch|uninstall, version, state: downloading|restarting|confirming|applied|failed|rolled_back|uninstalled,
error, changed_at}`. Ingest bounds it (64 JVMs, clipped strings) and stores it in `agent_hosts.java_agent`.

### 2.6 Fleet policy (backend)

The update policy's `java_agent` section `{mode (default manual), version (default agent), changed_at}` and per-host
mode overrides (`agent_java_host_overrides`, `PUT/DELETE /api/v1/fleet/hosts/{host_id}/java-agent`). Sync answers
`java_agent {mode, version, target_version, manifest, signature, download_url, reason}`. A host in `auto` gets the
target and its signed manifest when its report is capable, the target is a verified release with a `java-agent` jar,
it did not roll that version back, it is in the current wave (`fnv32a(host_id + ":java-agent:" + target) % 100` below
the policy wave reached after `wave_soak_minutes` per wave since the later of `changed_at` and the release time; a
per-host `auto` override skips waves) and a maintenance window is open. Decision reasons: `offer`, `up_to_date`,
`mode_off`, `manual`, `not_reported`, `not_capable`, `invalid_version`, `no_catalog`, `target_unavailable`,
`no_artifact`, `already_failed`, `not_in_wave`, `outside_window`.

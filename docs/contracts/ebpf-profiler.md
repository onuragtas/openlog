# Contract: Whole-host CPU profiling (eBPF, v1)

Decisions: D-148. Component: `agents/ebpf`. Related: [profiles.md](profiles.md) (the profiles signal this
produces), [semantic-conventions.md](semantic-conventions.md) §2 (process attributes).

**This is a separate component, not a mode of the infra agent.** It is installed on purpose, it runs with
privileges the infra agent deliberately does not have, and an operator who never installs it keeps exactly
the security posture openlog documents today.

## 1. What it does, and what it does not

It samples the CPU of the **whole machine** — every process, instrumented or not — and exports the result as
OTLP profiles to the endpoint the language agents already use. A Go service with the SDK profiles itself; a
PHP-FPM pool, a database, a cron job and a kernel thread do not, and this is how they get a flame graph.

It is **not** eBPF auto-instrumentation. It produces no spans, creates no service edges and does not read
request payloads. `docs/features.md` still lists auto-instrumentation as not built, and this contract does
not change that.

Also not here, deliberately: off-CPU profiling, memory and allocation profiling, and any per-request tracing.
Each needs its own design and none is claimed by installing this.

## 2. Why it is a separate component

The infra agent's Linux sandbox forbids this work, and the sandbox is a promise rather than an accident:

- `packaging/systemd/openlog-infra-agent.service` runs as `User=openlog-agent` with
  `AmbientCapabilities=CAP_DAC_READ_SEARCH CAP_SYS_PTRACE` and nothing else, plus
  `SystemCallFilter=~@privileged @resources @mount @reboot @swap @raw-io @module @debug @cpu-emulation`,
  `MemoryDenyWriteExecute=yes` and `ProtectKernelTunables/Modules/Logs=yes`. `bpf(2)` and
  `perf_event_open(2)` are filtered by that list.
- `docs/operations/kubernetes.md` states the DaemonSet posture as a guarantee: "**It is not `privileged`**",
  with `RuntimeDefault` seccomp — which blocks `bpf(2)` — and every capability dropped but two.

Relaxing either would weaken the posture of every installation, including the ones that never wanted
profiling. So the privileges live in a component of its own, and the infra agent is untouched.

**Installed by default** (D-149), which is a change from how this started: the component is standard
equipment rather than something to ask for, on the grounds that a host nobody profiled is a host whose
slow process has no explanation. What does not change is that it is separate: its own package, its own
account and its own unit, so `--no-ebpf-profiler` leaves a machine exactly as it was, and the capabilities
are named out loud at install time rather than inherited quietly by the agent.

## 3. Privileges and kernel requirements

| | |
|---|---|
| Capabilities | `CAP_BPF` and `CAP_PERFMON` (kernels ≥ 5.8). `CAP_SYS_ADMIN` is accepted where those do not exist, and is not requested when they do |
| `perf_event_paranoid` | ≤ 2 is enough with `CAP_PERFMON`; the component reads the value and says which setting is blocking it rather than failing opaquely |
| Kernel | ≥ 5.8. No BTF and no kernel headers are needed: the program is built from instructions at run time, not compiled from C, so there is no CO-RE relocation to satisfy |
| Mounts | `/sys/fs/bpf` is **not** required — no pinning. `/proc` is read for process identity |

**No clang, and no cgo.** The BPF program is assembled with `cilium/ebpf/asm` (pure Go; its only dependency
is `golang.org/x/sys`, which the infra agent already carries). `bpf2go` would put clang into the build of a
repository that ships six os/arch targets with `CGO_ENABLED=0`, to generate a program of a few dozen
instructions. The instructions are written out instead, and the cost of that choice is that the program must
stay small — which is also the reason it is honest to keep it small.

When a requirement is missing the component logs what is missing and exits non-zero at start-up. It never
degrades into sampling nothing while looking healthy.

## 4. How a sample becomes a service

This is the part that makes the signal usable, and it is decided by the storage model rather than by taste.
`schema/clickhouse/0095_profiles.sql` shards on `cityHash64(tenant_id, service_name)` and orders by
`(tenant_id, service_name, profile_type, timestamp)`, because "a flame graph is always one service over one
window". The read API agrees: `GET /api/v1/profiles/flame` **requires** `service`, and `host` is only a
narrowing filter. There is no read path that takes a host and returns a profile.

So samples are **attributed per process** rather than sent as one undifferentiated host profile:

| The process is | `service.name` becomes |
|---|---|
| a service the infra agent discovered | its **discovery rule id** — `redis`, `postgresql`, `nginx` — the same value its process metrics carry as `openlog.discovery.id` |
| anything else | the binary's own name (`sshd`, `containerd`), as `process.executable.name` does |
| a kernel thread | `kernel`, once, rather than one pseudo-service per thread |

**How the discovered names get here.** The mapping lives in the infra agent's memory and is rebuilt every
round, so it cannot simply be read. The infra agent therefore publishes it to its runtime directory, the way
it already publishes `host-id` there for the language agents (semantic-conventions §1): an optional file
written atomically, absent when the agent is not running, and never required.

```
/run/openlog-infra-agent/services
exe<TAB>/usr/bin/redis-server<TAB>redis
container<TAB>3f2a1b9c…<TAB>redis
```

Keyed by **executable path and container id, never by pid**. Pids churn between publishes, so a pid-keyed
file would be wrong within seconds of being written; an executable path and a container id are stable for as
long as the thing they name exists. The profiler resolves a sample's pid to those through `/proc` itself.

A line whose kind it does not recognise is skipped rather than refused, so an infra agent that learns to
publish something new does not break a profiler that has not learned to read it yet.

When the file is absent — the infra agent is not installed, or is older — every process falls to its binary
name. That degrades the names on one screen; it does not degrade the profiling.

One OTLP profile is produced per resulting name, each carrying `host.id` and the process attributes of
[semantic-conventions.md](semantic-conventions.md) §2. Sending a single profile with no `service.name` was
rejected: the Kafka partition key is `<tenant_id>/<service.name>`, so every host in the fleet would land on
one partition, and the flame graph screen would have nothing to select.

The consequence worth stating plainly: on this screen "service" means *a service or a binary*. That is a
widening of what the word meant when only instrumented processes could profile themselves.

## 5. The payload

Identical to what the Go agent sends ([profiles.md](profiles.md)), because it is the same signal:

```
POST <endpoint>/v1/profiles
openlog-license-key: olk_…
content-type: application/x-protobuf        (gzip optional)
```

`profile_type` is `cpu`, `unit` is `nanoseconds`. A sampled stack is converted to a value by multiplying the
sample count by the sampling period, so the number means the same thing it does for the Go agent: CPU
nanoseconds on that stack.

Retries follow the ingest contract (429, 502, 503, 504 with `Retry-After`) and are **bounded**: a profile
measures a window that has already passed, so retrying past the next window costs the next profile rather
than saving the last one.

An interval in which nothing ran produces no request at all.

## 6. Limits

| | |
|---|---|
| Sampling frequency | 99 Hz per CPU by default. 99 rather than 100 so sampling does not fall into lockstep with timer-driven work |
| Profile interval | 60 s, matching the other producers |
| Stack depth | 127 frames, the BPF stack map's own limit; deeper stacks are truncated at the root and marked |
| Distinct stacks per interval | 20 000, beyond which the smallest are dropped — the flame graph cannot draw them either |
| Services per interval | 256, so a machine that forks a new binary per request cannot turn one host into thousands of series |
| Resource budget | the sampler is bounded to a few percent of one core; exceeding it lowers the frequency and says so |

## 7. Symbolication

Native stacks are resolved to function names where the binary carries them (ELF symbol table, and the Go
`pclntab` for Go binaries). A stripped binary yields addresses, and the frame is reported as
`<binary>+0x<offset>` rather than being dropped — knowing which binary burned the CPU is still an answer.

Interpreted runtimes (Python, PHP, Ruby, the JVM) show their **interpreter's** frames, not the application's.
That is a real limitation and not a bug to be filed: it needs per-runtime unwinding, which is its own
contract. A language agent's own profiler is the better answer where one exists.

## 8. Errors

`401` unknown or missing license key · `413` body too large · `429` rate limited, with `Retry-After` ·
`503` retry later. Bodies are `{"error": {"code", "message"}}`.

Never retried: `401` and `413`. Nothing about the next attempt would differ.

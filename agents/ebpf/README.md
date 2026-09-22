# openlog eBPF profiler

Whole-host CPU profiling: every process on the machine, instrumented or not, sampled and exported as OTLP
profiles. It implements [ebpf-profiler.md](../../docs/contracts/ebpf-profiler.md) — that document is the
contract, this is its implementation.

**A separate component, not part of the infra agent.** The infra agent's sandbox forbids this work on
purpose: its systemd unit filters `bpf(2)` and `perf_event_open(2)`, carries only `CAP_DAC_READ_SEARCH` and
`CAP_SYS_PTRACE`, and the Kubernetes DaemonSet posture is published as a guarantee that it is not
privileged. An operator who never installs this keeps that posture exactly (D-148).

**Linux only**, and Linux-only in function rather than in compilation: every package here still builds and
vets on darwin and windows, with the sampler behind a build tag. A component that fails to compile where it
will never run makes the whole repository harder to work in.

## Status

Not yet runnable. What exists and is tested:

- `internal/otlpprofiles` — aggregated stacks to an OTLP profiles payload, with the shared dictionary.
- `internal/attribute` — which service a sample is stored under: the infra agent's published discovery map
  where there is one, the binary's name where there is not, `kernel` for kernel threads.
- `internal/export` — posting to `/v1/profiles` with the license key, gzip, and retries bounded so a dead
  ingest delays the next profile instead of replacing it.
- `internal/aggregate` — one interval's raw samples grouped per service, with every limit the contract
  sets: 127 frames (the cut is marked), 20 000 stacks and 256 services, dropping the quietest rather than
  the busiest.
- `internal/config` — settings from the environment, refusing what it cannot honour rather than guessing:
  no license key, a sampling frequency outside 1..1000, an interval under a second.
- `internal/profiler` — the interval loop that ties those together: sample, group, convert, send. Every
  kernel-facing call is behind the `Sampler` interface, which is what keeps the loop testable on a machine
  that cannot run eBPF at all.
- `internal/version` and the command itself — `-version`, `-self-test` to say whether this machine can be
  sampled, and `-once` to print what would be stored before pointing it at an ingest.

- `internal/sampler` (the program, not the plumbing) — the BPF program is assembled by hand with
  `cilium/ebpf/asm` and its maps are declared beside it. None of that needs a kernel to construct, so it
  lives in an untagged file and the tests assert its shape here: both stacks interned, only known maps
  referenced, the key encoding matching the map spec byte for byte.

- `internal/sampler` (the Linux plumbing) — one perf event per CPU at the configured frequency, the
  program attached with `PERF_EVENT_IOC_SET_BPF`, and the maps drained and cleared each interval. The
  arithmetic over what the kernel wrote is untagged and tested here: summing a per-CPU counter across
  CPUs, and trimming the zero padding a fixed-width stack array leaves after the last frame.

The binary builds for linux/amd64, linux/arm64, darwin and windows. Off Linux, `sampler.New` refuses
rather than returning a sampler that produces nothing.

**None of the kernel-facing code has ever run.** It compiles for both Linux architectures and its pure
parts are tested, but the program has not been through a verifier and the perf attach has not been
attempted, because this repository has no privileged Linux CI job and a development Mac cannot load BPF.
The first run on a real host is where `New` either works or says exactly which capability or
`perf_event_paranoid` setting is in the way.

That shapes how the whole component is written: everything that does not touch the kernel sits behind an
interface and is tested where tests can run, and the kernel-facing part is kept small enough to read.
Where a claim here has not been executed, it says so rather than implying a green test.

- `internal/symbol` — naming an address: which file `/proc/<pid>/maps` has at that address, where in the
  file it lands (via the PT_LOAD headers, since symbols carry virtual addresses and maps give offsets),
  and what the symbol table calls it. A stripped binary answers `app+0x1010`, a vanished process answers
  the bare address; nothing is invented. Tested against an ELF built byte by byte in the test, because
  this repository carries no binary fixtures.

A fresh symbolizer is built for every window. Its caches are keyed by pid, and pids are reused — a cache
that outlived its window would name a new program's addresses after the symbols of whatever used to hold
that pid, which is wrong in a way that looks entirely plausible.

## No clang, no cgo

The BPF program is assembled with `cilium/ebpf/asm` rather than generated from C by `bpf2go`. This
repository ships six os/arch targets with `CGO_ENABLED=0`, and putting clang into that build to produce a
few dozen instructions is the wrong trade. The cost is that the program has to stay small.

## Development

```sh
go test ./...
go vet ./...
```

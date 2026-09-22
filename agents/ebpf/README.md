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

The binary builds for linux/amd64, linux/arm64, darwin and windows. `internal/sampler` refuses on every
platform today, with two different errors because they are two different situations: on Linux the sampler
has not landed yet, anywhere else the kernel interfaces do not exist. Neither returns a sampler that
produces nothing.

Still to come: the perf-event sampler and its BPF program, and symbolication.

**The sampler cannot be verified on a development Mac**, and this repository has no privileged Linux CI job.
So everything that does not touch the kernel is built behind an interface and tested where tests can run,
and the kernel-facing part is kept small enough to read. Where a claim here has not been executed, it says
so rather than implying a green test.

## No clang, no cgo

The BPF program is assembled with `cilium/ebpf/asm` rather than generated from C by `bpf2go`. This
repository ships six os/arch targets with `CGO_ENABLED=0`, and putting clang into that build to produce a
few dozen instructions is the wrong trade. The cost is that the program has to stay small.

## Development

```sh
go test ./...
go vet ./...
```

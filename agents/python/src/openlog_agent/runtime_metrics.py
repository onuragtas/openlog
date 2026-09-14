"""Python runtime and process metrics.

Names follow the OpenTelemetry semantic conventions (the Go agent's policy: semconv names where they exist,
``openlog.`` prefix otherwise):

  cpython.gc.collections {cpython.gc.generation}            Counter  {collection}
  cpython.gc.collected_objects {cpython.gc.generation}      Counter  {object}
  cpython.gc.uncollectable_objects {cpython.gc.generation}  Counter  {object}
  openlog.cpython.gc.time {cpython.gc.generation}           Counter  s   (total time in collections)
  openlog.cpython.gc.pause.max {cpython.gc.generation}      Gauge    s   (longest collection since the last export)
  process.cpu.time {cpu.mode}                               Counter  s
  process.memory.usage                                      UpDownCounter By  (RSS; Linux, or psutil when installed)
  process.memory.virtual                                    UpDownCounter By  (Linux, or psutil)
  process.thread.count                                      UpDownCounter {thread}
  process.open_file_descriptor.count                        UpDownCounter {file_descriptor}  (Linux)
  process.context_switches {process.context_switch.type}    Counter  {context_switch}  (Linux)

Values are read on collection. GC timing uses a ``gc.callbacks`` hook that only updates plain numbers: recording
into an SDK instrument from inside a garbage collection could re-enter SDK locks.
"""

from __future__ import annotations

import gc
import os
import sys
import threading
import time
from typing import Callable, Dict, Iterable, List, Optional

from opentelemetry.metrics import CallbackOptions, MeterProvider, Observation

from .version import __version__

RUNTIME_SCOPE = "openlog_agent.runtime"

_PAGE_SIZE = os.sysconf("SC_PAGE_SIZE") if hasattr(os, "sysconf") else 4096


class _ProcSnapshot:
    """/proc/self/status and statm, read at most once per collection (every instrument callback shares it)."""

    def __init__(self, proc: str) -> None:
        self._proc = proc
        self._at = 0.0
        self.status: Dict[str, str] = {}
        self.statm: List[int] = []

    def refresh(self) -> None:
        now = time.monotonic()
        if now - self._at < 1.0:
            return
        self._at = now
        status: Dict[str, str] = {}
        try:
            with open(os.path.join(self._proc, "status"), encoding="ascii", errors="replace") as f:
                for line in f:
                    k, _, v = line.partition(":")
                    status[k] = v.strip()
        except OSError:
            pass
        self.status = status
        try:
            with open(os.path.join(self._proc, "statm"), encoding="ascii") as f:
                self.statm = [int(x) for x in f.read().split()]
        except (OSError, ValueError):
            self.statm = []


class GCTimer:
    """Accumulates collection time per generation from gc.callbacks (no locks, no allocation-heavy work)."""

    def __init__(self) -> None:
        self.total = [0.0, 0.0, 0.0]
        self.max = [0.0, 0.0, 0.0]
        self._start = 0.0

    def __call__(self, phase: str, info: Dict[str, int]) -> None:
        if phase == "start":
            self._start = time.perf_counter()
            return
        gen = info.get("generation", 0)
        if not 0 <= gen <= 2 or not self._start:
            return
        d = time.perf_counter() - self._start
        self.total[gen] += d
        if d > self.max[gen]:
            self.max[gen] = d


class RuntimeMetrics:
    def __init__(self, stop: Callable[[], None]) -> None:
        self._stop = stop

    def stop(self) -> None:
        self._stop()


def _psutil_process():
    try:
        import psutil  # type: ignore  # pylint: disable=import-outside-toplevel

        return psutil.Process()
    except Exception:  # pylint: disable=broad-except
        return None


def start_runtime_metrics(meter_provider: MeterProvider, proc: str = "/proc/self") -> RuntimeMetrics:
    meter = meter_provider.get_meter(RUNTIME_SCOPE, __version__)
    linux = sys.platform.startswith("linux") and os.path.isdir(proc)
    snap = _ProcSnapshot(proc)
    ps = None if linux else _psutil_process()
    timer = GCTimer()
    gc.callbacks.append(timer)
    ps_pid = [os.getpid()]

    def gen_attrs(g: int) -> Dict[str, int]:
        return {"cpython.gc.generation": g}

    def gc_stat(key: str) -> Callable[[CallbackOptions], Iterable[Observation]]:
        def cb(_: CallbackOptions) -> Iterable[Observation]:
            return [Observation(s.get(key, 0), gen_attrs(g)) for g, s in enumerate(gc.get_stats())]

        return cb

    meter.create_observable_counter(
        "cpython.gc.collections",
        [gc_stat("collections")],
        unit="{collection}",
        description="The number of times a generation was collected since interpreter start.",
    )
    meter.create_observable_counter(
        "cpython.gc.collected_objects",
        [gc_stat("collected")],
        unit="{object}",
        description="The total number of objects collected inside a generation since interpreter start.",
    )
    meter.create_observable_counter(
        "cpython.gc.uncollectable_objects",
        [gc_stat("uncollectable")],
        unit="{object}",
        description="The total number of objects which were found to be uncollectable inside a generation since interpreter start.",
    )

    def gc_time(_: CallbackOptions) -> Iterable[Observation]:
        return [Observation(timer.total[g], gen_attrs(g)) for g in range(3)]

    def gc_pause(_: CallbackOptions) -> Iterable[Observation]:
        out = [Observation(timer.max[g], gen_attrs(g)) for g in range(3)]
        timer.max = [0.0, 0.0, 0.0]
        return out

    meter.create_observable_counter(
        "openlog.cpython.gc.time", [gc_time], unit="s", description="Total time spent in garbage collections."
    )
    meter.create_observable_gauge(
        "openlog.cpython.gc.pause.max",
        [gc_pause],
        unit="s",
        description="Longest garbage collection since the previous collection of this metric.",
    )

    def cpu(_: CallbackOptions) -> Iterable[Observation]:
        t = os.times()
        return [Observation(t.user, {"cpu.mode": "user"}), Observation(t.system, {"cpu.mode": "system"})]

    meter.create_observable_counter(
        "process.cpu.time", [cpu], unit="s", description="Total CPU seconds broken down by different CPU modes."
    )

    def psutil_ok() -> bool:
        nonlocal ps
        if ps is not None and ps_pid[0] != os.getpid():
            ps = _psutil_process()
            ps_pid[0] = os.getpid()
        return ps is not None

    def memory(_: CallbackOptions) -> Iterable[Observation]:
        if linux:
            snap.refresh()
            return [Observation(snap.statm[1] * _PAGE_SIZE)] if len(snap.statm) > 1 else []
        if psutil_ok():
            return [Observation(ps.memory_info().rss)]
        return []

    def virtual(_: CallbackOptions) -> Iterable[Observation]:
        if linux:
            snap.refresh()
            return [Observation(snap.statm[0] * _PAGE_SIZE)] if snap.statm else []
        if psutil_ok():
            return [Observation(ps.memory_info().vms)]
        return []

    def threads(_: CallbackOptions) -> Iterable[Observation]:
        if linux:
            snap.refresh()
            v = snap.status.get("Threads")
            if v and v.isdigit():
                return [Observation(int(v))]
        return [Observation(threading.active_count())]

    meter.create_observable_up_down_counter(
        "process.memory.usage", [memory], unit="By", description="The amount of physical memory in use."
    )
    meter.create_observable_up_down_counter(
        "process.memory.virtual", [virtual], unit="By", description="The amount of committed virtual memory."
    )
    meter.create_observable_up_down_counter(
        "process.thread.count", [threads], unit="{thread}", description="Process threads count."
    )

    if linux:
        fd_dir = os.path.join(proc, "fd")

        def fds(_: CallbackOptions) -> Iterable[Observation]:
            try:
                return [Observation(len(os.listdir(fd_dir)))]
            except OSError:
                return []

        def switches(_: CallbackOptions) -> Iterable[Observation]:
            snap.refresh()
            out = []
            for key, kind in (("voluntary_ctxt_switches", "voluntary"), ("nonvoluntary_ctxt_switches", "involuntary")):
                v = snap.status.get(key)
                if v and v.isdigit():
                    out.append(Observation(int(v), {"process.context_switch.type": kind}))
            return out

        meter.create_observable_up_down_counter(
            "process.open_file_descriptor.count",
            [fds],
            unit="{file_descriptor}",
            description="Number of file descriptors in use by the process.",
        )
        meter.create_observable_counter(
            "process.context_switches",
            [switches],
            unit="{context_switch}",
            description="Number of times the process has been context switched.",
        )

    def stop() -> None:
        try:
            gc.callbacks.remove(timer)
        except ValueError:
            pass

    return RuntimeMetrics(stop)


def gc_timer_installed() -> Optional[GCTimer]:
    """The installed GC timer, if any (tests)."""
    for cb in gc.callbacks:
        if isinstance(cb, GCTimer):
            return cb
    return None

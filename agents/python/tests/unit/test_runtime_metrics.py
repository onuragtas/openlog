import gc
import sys

from opentelemetry.sdk.metrics import MeterProvider
from opentelemetry.sdk.metrics.export import InMemoryMetricReader

from openlog_agent.runtime_metrics import RUNTIME_SCOPE, gc_timer_installed, start_runtime_metrics


def collect(reader):
    out = {}
    data = reader.get_metrics_data()
    for rm in data.resource_metrics:
        for sm in rm.scope_metrics:
            assert sm.scope.name == RUNTIME_SCOPE
            for m in sm.metrics:
                out[m.name] = m
    return out


def test_runtime_metrics():
    reader = InMemoryMetricReader()
    provider = MeterProvider(metric_readers=[reader], shutdown_on_exit=False)
    rm = start_runtime_metrics(provider)
    try:
        assert gc_timer_installed() is not None
        garbage = []
        for _ in range(3):
            a = []
            a.append(a)
            garbage.append(a)
        del garbage, a
        gc.collect()
        metrics = collect(reader)
        names = set(metrics)
        expected = {
            "cpython.gc.collections",
            "cpython.gc.collected_objects",
            "cpython.gc.uncollectable_objects",
            "openlog.cpython.gc.time",
            "openlog.cpython.gc.pause.max",
            "process.cpu.time",
            "process.thread.count",
        }
        if sys.platform.startswith("linux"):
            expected |= {
                "process.memory.usage",
                "process.memory.virtual",
                "process.open_file_descriptor.count",
                "process.context_switches",
            }
        assert expected <= names, expected - names
        gens = {
            p.attributes["cpython.gc.generation"]: p.value for p in metrics["cpython.gc.collections"].data.data_points
        }
        assert set(gens) == {0, 1, 2} and gens[2] >= 1
        gc_time = {
            p.attributes["cpython.gc.generation"]: p.value for p in metrics["openlog.cpython.gc.time"].data.data_points
        }
        assert gc_time[2] > 0
        assert metrics["cpython.gc.collections"].data.is_monotonic
        modes = {p.attributes["cpu.mode"] for p in metrics["process.cpu.time"].data.data_points}
        assert modes == {"user", "system"}
        if sys.platform.startswith("linux"):
            rss = metrics["process.memory.usage"].data.data_points[0].value
            assert rss > 1 << 20
    finally:
        rm.stop()
        provider.shutdown()
    assert gc_timer_installed() is None

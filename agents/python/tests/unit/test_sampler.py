import json
import os
from pathlib import Path

import pytest
from opentelemetry import context as context_api
from opentelemetry import trace
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter
from opentelemetry.sdk.trace.id_generator import IdGenerator
from opentelemetry.trace import SpanKind, TraceState
from opentelemetry.trace.propagation.tracecontext import TraceContextTextMapPropagator

from openlog_agent.sampler import (
    SAMPLING_RATIO_KEY,
    OpenlogTracerProvider,
    create_sampler,
    encode_threshold,
    ot_probability,
    parse_ot,
    parse_threshold,
    with_ot,
)

# Cross-language fixtures generated from the Go agent's sampler (agents/node/scripts/gen-go-fixtures.sh, D-061).
FIXTURES_PATH = Path(
    os.environ.get(
        "OPENLOG_GO_SAMPLER_FIXTURES",
        Path(__file__).resolve().parents[3] / "node" / "test" / "interop" / "go-sampler-fixtures.json",
    )
)
FIXTURES = json.loads(FIXTURES_PATH.read_text())["cases"]

propagator = TraceContextTextMapPropagator()


class FixedIds(IdGenerator):
    def __init__(self, f):
        self.f = f
        self.n = 0

    def generate_trace_id(self):
        return int(self.f["traceId"], 16)

    def generate_span_id(self):
        self.n += 1
        return int(self.f["spanId"] if self.n == 1 else self.f["childSpanId"], 16)


def headers(ctx):
    carrier = {}
    propagator.inject(carrier, context=ctx)
    return carrier.get("traceparent", ""), carrier.get("tracestate", "")


def test_threshold_encoding():
    for ratio, th in [
        (0.5, "8"),
        (0.25, "c"),
        (0.125, "e"),
        (0.75, "4"),
        (0.1, "e6666666666666"),
        (0.001, "ffbe76c8b43958"),
    ]:
        keep = round(ratio * 2**56)
        assert encode_threshold((1 << 56) - keep) == th, ratio
        p = ot_probability([("th", th)])
        assert p is not None and abs(p - ratio) < 1e-12
    assert encode_threshold(0) == "0"
    assert ot_probability([("p", "2")]) == 0.25
    assert ot_probability([("p", "63")]) == 0
    assert ot_probability([("p", "64")]) is None
    assert parse_threshold("xyz") is None
    assert parse_threshold("123456789abcdef") is None
    assert parse_ot("th:c;rv:00000000000001;bad;:x") == [("th", "c"), ("rv", "00000000000001")]


def test_with_ot_keeps_w3c_limits():
    members = [(f"v{i}", "x") for i in range(32)]
    ts = with_ot(TraceState(members), [("th", "c")])
    out = list(ts.items())
    assert len(out) == 32
    assert out[0] == ("ot", "th:c")
    assert out[31] == ("v30", "x")  # right-most member dropped
    long = [("th", "c"), ("x", "a" * 200), ("y", "b" * 100)]
    assert with_ot(None, long).get("ot") == "th:c;x:" + "a" * 200
    assert with_ot(TraceState([("ot", "th:c"), ("k", "v")]), []).to_header() == "k=v"
    bad = TraceState([("k", "v")])
    assert with_ot(bad, [("x", "a,b")]) is bad


@pytest.mark.parametrize("f", FIXTURES, ids=[f["name"] for f in FIXTURES])
def test_go_fixture(f):
    exporter = InMemorySpanExporter()
    rnd = f.get("randomness") or "00000000000000"
    provider = OpenlogTracerProvider(
        sampler=create_sampler(f["ratio"], f["writeRV"], lambda: (int(rnd, 16), rnd)),
        id_generator=FixedIds(f),
        shutdown_on_exit=False,
    )
    provider.add_span_processor(SimpleSpanProcessor(exporter))
    tracer = provider.get_tracer("fixtures")
    ctx = context_api.Context()
    if f["incoming"]:
        ctx = propagator.extract(f["incoming"], context=ctx)
    span = tracer.start_span("span", context=ctx, kind=SpanKind.SERVER)
    sctx = trace.set_span_in_context(span, ctx)
    child = tracer.start_span("child", context=sctx, kind=SpanKind.CLIENT)
    cctx = trace.set_span_in_context(child, sctx)
    child.end()
    span.end()
    finished = {s.context.span_id: s for s in exporter.get_finished_spans()}

    for s, c, want, which in ((span, sctx, f["span"], "span"), (child, cctx, f["child"], "child")):
        tp, tstate = headers(c)
        assert tp == want["traceparent"], which
        assert tstate == want["tracestate"], which
        assert bool(s.get_span_context().trace_flags & 1) == want["sampled"], which
        rec = finished.get(s.get_span_context().span_id)
        ratio = rec.attributes.get(SAMPLING_RATIO_KEY) if rec is not None else None
        assert ratio == want["samplingRatio"], which
        if rec is not None:
            # exported span context carries the same flags as the propagated one
            assert rec.context.trace_flags == s.get_span_context().trace_flags


def test_ratio_quarter_weights_to_total():
    exporter = InMemorySpanExporter()
    provider = OpenlogTracerProvider(sampler=create_sampler(0.25), shutdown_on_exit=False)
    provider.add_span_processor(SimpleSpanProcessor(exporter))
    tracer = provider.get_tracer("t")
    n = 20000
    for _ in range(n):
        tracer.start_span("root").end()
    spans = exporter.get_finished_spans()
    weighted = sum(1 / s.attributes[SAMPLING_RATIO_KEY] for s in spans)
    assert abs(len(spans) / n - 0.25) < 0.02
    assert abs(weighted - n) / n < 0.08
    for s in spans[:10]:
        assert s.context.trace_state.get("ot") == "th:c"
        assert s.context.trace_flags == 3


def test_start_attributes_are_kept():
    # SDK samplers return the span's start attributes; dropping them lost rpc.*/http.* attributes set at start
    exporter = InMemorySpanExporter()
    provider = OpenlogTracerProvider(sampler=create_sampler(0.5), shutdown_on_exit=False)
    provider.add_span_processor(SimpleSpanProcessor(exporter))
    tracer = provider.get_tracer("t")
    for _ in range(64):
        tracer.start_span("root", attributes={"rpc.system": "grpc"}).end()
    roots = exporter.get_finished_spans()
    assert roots and all(
        s.attributes["rpc.system"] == "grpc" and s.attributes[SAMPLING_RATIO_KEY] == 0.5 for s in roots
    )
    exporter.clear()
    for tracestate in ("ot=th:c", "vendor=x"):
        carrier = {"traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01", "tracestate": tracestate}
        ctx = propagator.extract(carrier)
        tracer.start_span("server", context=ctx, kind=SpanKind.SERVER, attributes={"rpc.service": "shop.Catalog"}).end()
    a, b = exporter.get_finished_spans()
    assert a.attributes["rpc.service"] == "shop.Catalog" and a.attributes[SAMPLING_RATIO_KEY] == 0.25
    assert b.attributes["rpc.service"] == "shop.Catalog" and SAMPLING_RATIO_KEY not in b.attributes


def test_start_as_current_span_sets_random_flag():
    provider = OpenlogTracerProvider(sampler=create_sampler(1), shutdown_on_exit=False)
    tracer = provider.get_tracer("t")
    with tracer.start_as_current_span("outer") as outer:
        assert trace.get_current_span() is outer
        assert outer.get_span_context().trace_flags == 3
        with tracer.start_as_current_span("inner") as inner:
            assert inner.get_span_context().trace_id == outer.get_span_context().trace_id
            assert inner.get_span_context().trace_flags == 3

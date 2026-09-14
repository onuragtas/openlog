"""Consistent probability sampling with the OpenTelemetry tracestate ``ot`` entry.

https://opentelemetry.io/docs/specs/otel/trace/tracestate-probability-sampling/ — a port of the Go agent's
sampler.go (and the Node.js agent's sampler.ts). Cross-language fixtures produced by the Go agent:
agents/node/test/interop/go-sampler-fixtures.json (tests/unit/test_sampler.py).
"""

from __future__ import annotations

import random
import re
from typing import Callable, List, Optional, Sequence, Tuple

from opentelemetry import trace as trace_api
from opentelemetry.context import Context
from opentelemetry.sdk.trace import Tracer as SdkTracer
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.sampling import (
    ALWAYS_OFF,
    ALWAYS_ON,
    Decision,
    ParentBased,
    Sampler,
    SamplingResult,
)
from opentelemetry.trace import SpanContext, TraceFlags, TraceState

#: Span attribute carrying the head sampling probability (only set when p < 1); the APM backend weights by 1/p.
SAMPLING_RATIO_KEY = "sampling.ratio"

OT_KEY = "ot"
MAX_OT_VALUE_LEN = 256
MAX_MEMBERS = 32
MAX_THRESHOLD = 1 << 56  # exclusive; T = 2^56 would mean p = 0
RANDOMNESS_MASK = MAX_THRESHOLD - 1
TRACE_FLAG_RANDOM = 0x02

OTField = Tuple[str, str]
#: Returns 56 random bits and their ot=rv encoding (14 hex digits).
RandomnessSource = Callable[[], Tuple[int, str]]

_rng = random.Random()


def default_randomness() -> Tuple[int, str]:
    r = _rng.getrandbits(56)
    return r, format(r, "014x")


def parse_ot(s: Optional[str]) -> List[OTField]:
    """Parses the ``ot`` value (``k1:v1;k2:v2``)."""
    if not s:
        return []
    out: List[OTField] = []
    for part in s.split(";"):
        i = part.find(":")
        if i > 0:
            out.append((part[:i], part[i + 1 :]))
    return out


def _ot_get(o: Sequence[OTField], k: str) -> Optional[str]:
    for fk, fv in o:
        if fk == k:
            return fv
    return None


def _ot_without(o: Sequence[OTField], k: str) -> List[OTField]:
    return [f for f in o if f[0] != k]


def _ot_with(o: Sequence[OTField], k: str, v: str) -> List[OTField]:
    return [(k, v)] + _ot_without(o, k)


def ot_string(o: Sequence[OTField]) -> str:
    return ";".join(f"{k}:{v}" for k, v in o)


_RV = re.compile(r"^[0-9a-fA-F]{14}$")
_TH = re.compile(r"^[0-9a-fA-F]{1,14}$")
_P = re.compile(r"^[+-]?\d+$")


def ot_randomness(o: Sequence[OTField]) -> Optional[int]:
    """Explicit 56-bit randomness ``ot=rv:<14 hex digits>``."""
    v = _ot_get(o, "rv")
    if v is None or not _RV.match(v):
        return None
    return int(v, 16)


def encode_threshold(t: int) -> str:
    """Encodes T as up to 14 hex digits without trailing zeros (``0`` for T=0)."""
    return format(t, "014x").rstrip("0") or "0"


def parse_threshold(s: str) -> Optional[int]:
    """Decodes ``th:<hex>`` (1..14 hex digits, right-padded with zeros)."""
    if not _TH.match(s):
        return None
    return int(s.ljust(14, "0"), 16)


def ot_probability(o: Sequence[OTField]) -> Optional[float]:
    """p from ``th:<hex>`` (p = 1 − T/2^56) or legacy ``p:<n>`` (p = 2^−n)."""
    th = _ot_get(o, "th")
    if th is not None:
        t = parse_threshold(th)
        if t is not None:
            return 1 - t / MAX_THRESHOLD
    p = _ot_get(o, "p")
    if p is not None and _P.match(p):
        n = int(p)
        if 0 <= n <= 63:
            return 0.0 if n == 63 else 2.0**-n
    return None


# W3C tracestate value: printable ASCII except ',' and '=', not ending with a space, at most 256 characters.
_VALID_VALUE = re.compile(r"^[\x20-\x2b\x2d-\x3c\x3e-\x7e]{0,255}[\x21-\x2b\x2d-\x3c\x3e-\x7e]$")


def with_ot(ts: Optional[TraceState], o: Sequence[OTField]) -> Optional[TraceState]:
    """Stores ``o`` as the ``ot`` member of ``ts`` (moved to the front), keeping W3C limits.

    The value is at most 256 characters (sub-keys other than th and rv are dropped, last first) and the list keeps
    at most 32 members (the right-most member is dropped). An empty ``o`` removes the member. On an invalid value
    ``ts`` is returned unchanged.
    """
    fields = list(o)
    while len(ot_string(fields)) > MAX_OT_VALUE_LEN:
        i = len(fields) - 1
        while i >= 0 and fields[i][0] in ("th", "rv"):
            i -= 1
        if i < 0:
            return ts
        del fields[i]
    if not fields:
        if ts is None or ts.get(OT_KEY) is None:
            return ts
        return ts.delete(OT_KEY)
    value = ot_string(fields)
    if not _VALID_VALUE.match(value):
        return ts
    others = [(k, v) for k, v in ts.items() if k != OT_KEY] if ts is not None else []
    return TraceState([(OT_KEY, value)] + others[: MAX_MEMBERS - 1])


def _parent_trace_state(ctx: Optional[Context]) -> Optional[TraceState]:
    sc = trace_api.get_current_span(ctx).get_span_context()
    return sc.trace_state if sc.is_valid else None


def _with_attribute(attributes, key: str, value: float) -> dict:
    # The Python SDK uses SamplingResult.attributes as the span's start attributes (not merged with the attributes
    # given to start_span), so a sampler that records a span must return them.
    out = dict(attributes) if attributes else {}
    out[key] = value
    return out


def _keep_count(ratio: float) -> int:
    # ratio·2^56 is exact in float64 (power-of-two scaling); round half up like Go's math.Round for positive values.
    x = ratio * MAX_THRESHOLD
    k = int(x)
    if x - k >= 0.5:
        k += 1
    return k


class ConsistentRatioRootSampler(Sampler):
    """Root sampler for 0 < ratio < 1 (threshold comparison against the trace id or ot=rv randomness)."""

    def __init__(self, ratio: float, write_rv: bool, randomness: RandomnessSource) -> None:
        self.ratio = ratio
        self._write_rv = write_rv
        self._randomness = randomness
        keep = max(_keep_count(ratio), 1)
        self.threshold = MAX_THRESHOLD - keep
        self.th = encode_threshold(self.threshold)

    def should_sample(self, parent_context, trace_id, name, kind=None, attributes=None, links=None, trace_state=None):
        ts = _parent_trace_state(parent_context)
        ot = parse_ot(ts.get(OT_KEY) if ts is not None else None)
        r = ot_randomness(ot)
        if r is None:
            if self._write_rv:
                r, rv = self._randomness()
                ot = _ot_without(ot, "rv") + [("rv", rv)]
            else:
                r = trace_id & RANDOMNESS_MASK
        if r < self.threshold:
            return SamplingResult(Decision.DROP, None, with_ot(ts, _ot_without(ot, "th")))
        return SamplingResult(
            Decision.RECORD_AND_SAMPLE,
            _with_attribute(attributes, SAMPLING_RATIO_KEY, self.ratio),
            with_ot(ts, _ot_with(ot, "th", self.th)),
        )

    def get_description(self) -> str:
        return f"OpenlogConsistentRatio{{{self.ratio}}}"


class _RVRootSampler(Sampler):
    """Adds ot=rv to roots decided by another sampler (ratio 0 or 1)."""

    def __init__(self, inner: Sampler, randomness: RandomnessSource) -> None:
        self._inner = inner
        self._randomness = randomness

    def should_sample(self, parent_context, trace_id, name, kind=None, attributes=None, links=None, trace_state=None):
        res = self._inner.should_sample(parent_context, trace_id, name, kind, attributes, links)
        ts = res.trace_state if res.trace_state is not None else _parent_trace_state(parent_context)
        ot = parse_ot(ts.get(OT_KEY) if ts is not None else None)
        if ot_randomness(ot) is not None:
            return res
        _, rv = self._randomness()
        return SamplingResult(res.decision, res.attributes, with_ot(ts, _ot_without(ot, "rv") + [("rv", rv)]))

    def get_description(self) -> str:
        return f"OpenlogRV{{{self._inner.get_description()}}}"


class RemoteParentSampledSampler(Sampler):
    """Samples every span whose remote parent is sampled and records the upstream sampling probability on it."""

    def should_sample(self, parent_context, trace_id, name, kind=None, attributes=None, links=None, trace_state=None):
        ts = _parent_trace_state(parent_context)
        p = ot_probability(parse_ot(ts.get(OT_KEY) if ts is not None else None))
        if p is not None and 0 < p < 1:
            return SamplingResult(Decision.RECORD_AND_SAMPLE, _with_attribute(attributes, SAMPLING_RATIO_KEY, p), ts)
        return SamplingResult(Decision.RECORD_AND_SAMPLE, attributes, ts)

    def get_description(self) -> str:
        return "OpenlogRemoteParentSampled"


def create_sampler(ratio: float, write_rv: bool = False, randomness: RandomnessSource = default_randomness) -> Sampler:
    """Parent-based sampler with the Go agent's semantics.

    - new traces are sampled with probability ``ratio`` by comparing the 56-bit randomness (tracestate ot=rv, else
      the lower 56 bits of the trace id) against T = (1 − ratio)·2^56; sampled roots carry sampling.ratio and
      ot=th:<T>;
    - children of a sampled remote parent are sampled and get sampling.ratio = p when tracestate carries p < 1;
    - other children follow their parent. With ``write_rv``, roots without ot=rv write explicit randomness.
    """
    keep_all = ratio != ratio or ratio >= 1  # NaN or >= 1
    if not keep_all and ratio > 0 and _keep_count(ratio) >= MAX_THRESHOLD:
        keep_all = True
    root: Sampler
    if keep_all:
        root = ALWAYS_ON
    elif ratio <= 0:
        root = ALWAYS_OFF
    else:
        root = ConsistentRatioRootSampler(ratio, write_rv, randomness)
    if write_rv and not isinstance(root, ConsistentRatioRootSampler):
        root = _RVRootSampler(root, randomness)
    return ParentBased(root, remote_parent_sampled=RemoteParentSampledSampler())


class RandomFlagTracer(SdkTracer):
    """SDK tracer that sets the W3C Trace Context Level 2 random flag (traceparent flags 0x02).

    Like the Go agent's randomTracerProvider: root spans get it (the SDK's trace ids are random), children inherit it
    from their parent, so a remote W3C Level 1 parent is continued unchanged. SDK 1.42+ sets the flag itself for
    random id generators; older SDKs (the Python 3.9 line) derive trace flags from the sampling decision only, so the
    flag is added to the new span context right after the SDK created it (before it is propagated or exported).
    """

    def start_span(self, name, context=None, *args, **kwargs):  # type: ignore[override]
        parent = trace_api.get_current_span(context).get_span_context()
        span = super().start_span(name, context, *args, **kwargs)
        add_random_flag(span, parent)
        return span


def add_random_flag(span: trace_api.Span, parent: Optional[SpanContext]) -> None:
    sc = span.get_span_context()
    if not sc.is_valid or sc.trace_flags & TRACE_FLAG_RANDOM:
        return
    if parent is not None and parent.is_valid and not parent.trace_flags & TRACE_FLAG_RANDOM:
        return
    try:
        span._context = SpanContext(  # type: ignore[attr-defined]  # pylint: disable=protected-access
            sc.trace_id,
            sc.span_id,
            is_remote=sc.is_remote,
            trace_flags=TraceFlags(sc.trace_flags | TRACE_FLAG_RANDOM),
            trace_state=sc.trace_state,
        )
    except AttributeError:
        pass


class OpenlogTracerProvider(TracerProvider):
    """TracerProvider whose tracers set the W3C random flag (see RandomFlagTracer)."""

    def get_tracer(self, *args, **kwargs):  # type: ignore[override]
        tracer = super().get_tracer(*args, **kwargs)
        if type(tracer) is SdkTracer:  # pylint: disable=unidiomatic-typecheck
            tracer.__class__ = RandomFlagTracer
        return tracer

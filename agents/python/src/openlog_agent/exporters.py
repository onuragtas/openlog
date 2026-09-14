"""OTLP exporters and the span post-processing done on export."""

from __future__ import annotations

import re
from typing import Any, Dict, Optional, Sequence, Tuple

from opentelemetry.sdk.trace import ReadableSpan
from opentelemetry.sdk.trace.export import SpanExporter, SpanExportResult
from opentelemetry.trace import SpanKind

from .config import PROTOCOL_GRPC, Config, ConfigError
from .sanitize import sanitize_key_value, sanitize_sql, truncate_query_text

QUERY_TEXT_KEYS = ("db.query.text", "db.statement")
KV_SYSTEMS = frozenset(("redis", "valkey", "memcached"))
# Document/search stores whose instrumentations already mask values or use JSON bodies.
NON_SQL_SYSTEMS = frozenset(
    (
        "mongodb",
        "elasticsearch",
        "opensearch",
        "aws.dynamodb",
        "dynamodb",
        "couchdb",
        "couchbase",
        "azure.cosmosdb",
        "cosmosdb",
        "cassandra",
    )
)
_KV_SANITIZED = re.compile(r"^[A-Z][A-Z._-]*( \?)*( …)?$")


def process_query_text(text: str, system: str, mode: str) -> Optional[str]:
    """Applies OPENLOG_DB_QUERY_TEXT to one statement; None removes the attribute."""
    if mode == "off":
        return None
    out = text
    if mode == "sanitized":
        if system in KV_SYSTEMS:
            if not _KV_SANITIZED.match(text):
                tokens = text.split()
                out = sanitize_key_value(tokens[0] if tokens else "", tokens[1:])
        elif system not in NON_SQL_SYSTEMS:
            out = sanitize_sql(text, system)
    return truncate_query_text(out)


def transform_span(span: ReadableSpan, mode: str) -> ReadableSpan:
    """Rewrites a finished span before export (on the exporter thread, not the request path).

    - ``db.query.text`` / ``db.statement``: OPENLOG_DB_QUERY_TEXT (``sanitized`` normalizes SQL like the Go agent and
      key/value commands to ``CMD ? ?``; ``raw`` keeps the text; ``off`` removes it); capped at 4096 bytes.
    - ``http.route`` of SERVER spans without a leading ``/`` (Django's ``users/<int:id>/``) gets one, and the span name
      ``<METHOD> <route>`` follows, so transactions are named like other frameworks' (apm.md §2.1).

    ReadableSpan objects handed to exporters are per-export copies; only their attribute mapping reference and name
    are replaced, the live span is not touched.
    """
    attrs = span.attributes
    if not attrs:
        return span
    changed: Optional[Dict[str, Any]] = None
    name = span.name
    for key in QUERY_TEXT_KEYS:
        text = attrs.get(key)
        if not isinstance(text, str):
            continue
        system = str(attrs.get("db.system.name") or attrs.get("db.system") or "").lower()
        out = process_query_text(text, system, mode)
        if out != text:
            if changed is None:
                changed = dict(attrs)
            if out is None:
                del changed[key]
            else:
                changed[key] = out
    if span.kind == SpanKind.SERVER:
        route = attrs.get("http.route")
        if isinstance(route, str) and route and not route.startswith("/") and route != "*":
            if changed is None:
                changed = dict(attrs)
            fixed = "/" + route
            changed["http.route"] = fixed
            method = attrs.get("http.request.method") or attrs.get("http.method")
            if isinstance(method, str) and name in (f"{method} {route}", f"{method} ^{route}"):
                name = f"{method} {fixed}"
            elif name == route:
                name = fixed
    if changed is None and name == span.name:
        return span
    if changed is not None:
        span._attributes = changed  # pylint: disable=protected-access
    span._name = name  # pylint: disable=protected-access
    return span


class OpenlogSpanExporter(SpanExporter):
    """Wraps the OTLP span exporter with transform_span."""

    def __init__(self, delegate: SpanExporter, db_query_text: str) -> None:
        self._delegate = delegate
        self._mode = db_query_text

    def export(self, spans: Sequence[ReadableSpan]) -> SpanExportResult:
        return self._delegate.export([transform_span(s, self._mode) for s in spans])

    def shutdown(self) -> None:
        self._delegate.shutdown()

    def force_flush(self, timeout_millis: int = 30000) -> bool:
        return self._delegate.force_flush(timeout_millis)


def create_exporters(cfg: Config) -> Tuple[Any, Any, Any]:
    """OTLP span, metric and log exporters.

    http/protobuf posts to <endpoint>/v1/{traces,metrics,logs}; grpc uses the endpoint as target (http:// =
    plaintext) and needs the ``grpc`` extra. The OpenTelemetry exporters retry transient failures (429/502/503/504,
    gRPC UNAVAILABLE/RESOURCE_EXHAUSTED) with exponential backoff; failures reach the rate-limited diagnostics.
    """
    headers = dict(cfg.headers)
    if cfg.protocol == PROTOCOL_GRPC:
        try:
            import grpc  # pylint: disable=import-outside-toplevel
            from opentelemetry.exporter.otlp.proto.grpc._log_exporter import (  # pylint: disable=import-outside-toplevel
                OTLPLogExporter as GrpcLogExporter,
            )
            from opentelemetry.exporter.otlp.proto.grpc.metric_exporter import (  # pylint: disable=import-outside-toplevel
                OTLPMetricExporter as GrpcMetricExporter,
            )
            from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import (  # pylint: disable=import-outside-toplevel
                OTLPSpanExporter as GrpcSpanExporter,
            )
        except ImportError as exc:
            raise ConfigError(
                f"openlog: OPENLOG_PROTOCOL=grpc needs the grpc extra (pip install 'openlog-agent[grpc]'): {exc}"
            ) from exc
        insecure = cfg.endpoint.startswith("http://")
        compression = grpc.Compression.Gzip if cfg.compression == "gzip" else grpc.Compression.NoCompression
        common: Dict[str, Any] = {
            "endpoint": cfg.endpoint,
            "insecure": insecure,
            "headers": headers,
            "timeout": cfg.export_timeout,
            "compression": compression,
        }
        return GrpcSpanExporter(**common), GrpcMetricExporter(**common), GrpcLogExporter(**common)

    # pylint: disable=import-outside-toplevel
    from opentelemetry.exporter.otlp.proto.http import Compression
    from opentelemetry.exporter.otlp.proto.http._log_exporter import OTLPLogExporter
    from opentelemetry.exporter.otlp.proto.http.metric_exporter import OTLPMetricExporter
    from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter

    comp = Compression.Gzip if cfg.compression == "gzip" else Compression.NoCompression
    return (
        OTLPSpanExporter(
            endpoint=f"{cfg.endpoint}/v1/traces", headers=headers, timeout=cfg.export_timeout, compression=comp
        ),
        OTLPMetricExporter(
            endpoint=f"{cfg.endpoint}/v1/metrics", headers=headers, timeout=cfg.export_timeout, compression=comp
        ),
        OTLPLogExporter(
            endpoint=f"{cfg.endpoint}/v1/logs", headers=headers, timeout=cfg.export_timeout, compression=comp
        ),
    )

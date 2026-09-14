"""Log ↔ trace correlation (apm.md §11).

OTLP log export of stdlib ``logging`` records is done by the OpenTelemetry logging instrumentation's handler on the
root logger (trace and span ids are part of each exported record). This module adds the ids to the application's own
log output: every ``LogRecord`` gets ``trace_id``, ``span_id`` and ``trace_flags`` (empty strings outside a span), so
formats such as ``%(trace_id)s`` and JSON formatters work, and :func:`structlog_processor` does the same for structlog.
"""

from __future__ import annotations

import logging
from typing import Any, Callable, MutableMapping

from opentelemetry import trace

CORRELATION_FIELDS = ("trace_id", "span_id", "trace_flags")


def _ids() -> tuple:
    sc = trace.get_current_span().get_span_context()
    if not sc.is_valid:
        return "", "", ""
    return format(sc.trace_id, "032x"), format(sc.span_id, "016x"), format(sc.trace_flags, "02x")


def install_record_factory() -> Callable[[], None]:
    """Wraps the logging record factory; returns a function that restores the previous one."""
    previous = logging.getLogRecordFactory()

    def factory(*args: Any, **kwargs: Any) -> logging.LogRecord:
        record = previous(*args, **kwargs)
        record.trace_id, record.span_id, record.trace_flags = _ids()
        return record

    factory.__openlog__ = True  # type: ignore[attr-defined]
    logging.setLogRecordFactory(factory)

    def restore() -> None:
        if logging.getLogRecordFactory() is factory:
            logging.setLogRecordFactory(previous)

    return restore


def structlog_processor(_logger: Any, _method: str, event_dict: MutableMapping[str, Any]) -> MutableMapping[str, Any]:
    """structlog processor adding ``trace_id``, ``span_id`` and ``trace_flags`` of the active span.

    structlog.configure(processors=[openlog_agent.structlog_processor, ..., structlog.processors.JSONRenderer()])

    When structlog renders into stdlib logging (``structlog.stdlib.LoggerFactory``), the records are also exported as
    OTLP logs with the trace context.
    """
    trace_id, span_id, flags = _ids()
    if trace_id:
        event_dict.setdefault("trace_id", trace_id)
        event_dict.setdefault("span_id", span_id)
        event_dict.setdefault("trace_flags", flags)
    return event_dict

"""The agent's own diagnostics: logfmt-like lines on stderr, like the Go agent's slog text handler."""

from __future__ import annotations

import json
import logging
import sys
import threading
import time
from datetime import datetime, timezone
from typing import Callable, Dict, Optional

Write = Callable[[str], None]

LEVELS = ("debug", "info", "warn", "error", "off")
_ORDER = {"debug": 0, "info": 1, "warn": 2, "error": 3, "off": 100}
_STDLIB = {"debug": logging.DEBUG, "info": logging.INFO, "warn": logging.WARNING, "error": logging.ERROR, "off": 100}


def parse_log_level(v: str) -> Optional[str]:
    s = v.strip().lower()
    if s in ("debug", "verbose", "all"):
        return "debug"
    if s == "info":
        return "info"
    if s in ("warn", "warning"):
        return "warn"
    if s == "error":
        return "error"
    if s in ("off", "none"):
        return "off"
    return None


def _stderr(line: str) -> None:
    try:
        sys.stderr.write(line + "\n")
        sys.stderr.flush()
    except Exception:  # pylint: disable=broad-except  # closed stderr
        pass


def _fmt(v: object) -> str:
    if isinstance(v, BaseException):
        return json.dumps(str(v))
    if isinstance(v, str):
        return json.dumps(v) if any(c in v for c in ' \t\n"=') or v == "" else v
    try:
        return json.dumps(v)
    except (TypeError, ValueError):
        return json.dumps(str(v))


class Diag:
    def __init__(self, level: str = "warn", write: Optional[Write] = None) -> None:
        self.level = level
        self._write = write or _stderr

    def enabled(self, level: str) -> bool:
        return self.level != "off" and _ORDER[level] >= _ORDER[self.level]

    def log(self, level: str, msg: str, **fields: object) -> None:
        if not self.enabled(level):
            return
        ts = datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")
        line = f"time={ts} level={level.upper()} msg={_fmt(msg)} component=openlog-python-agent"
        for k, v in fields.items():
            if v is not None:
                line += f" {k}={_fmt(v)}"
        self._write(line)

    def debug(self, msg: str, **fields: object) -> None:
        self.log("debug", msg, **fields)

    def info(self, msg: str, **fields: object) -> None:
        self.log("info", msg, **fields)

    def warn(self, msg: str, **fields: object) -> None:
        self.log("warn", msg, **fields)

    def error(self, msg: str, **fields: object) -> None:
        self.log("error", msg, **fields)


class RateLimitedHandler(logging.Handler):
    """Routes OpenTelemetry SDK/exporter log records to the agent diagnostics.

    The same message template is written at most once per ``interval`` seconds (the number of suppressed repeats is
    added to the next line), so an ingest outage does not flood stderr. Installed on the ``opentelemetry`` logger with
    ``propagate = False``: SDK export errors must not reach the application's root handlers, where the OTLP log
    handler would try to export them again.
    """

    def __init__(self, diag: Diag, interval: float = 60.0) -> None:
        super().__init__(level=_STDLIB[diag.level] if diag.level in _STDLIB else logging.WARNING)
        self._diag = diag
        self._interval = interval
        self._seen: Dict[str, list] = {}
        self._mu = threading.Lock()

    def emit(self, record: logging.LogRecord) -> None:
        level = "debug"
        if record.levelno >= logging.ERROR:
            level = "error"
        elif record.levelno >= logging.WARNING:
            level = "warn"
        elif record.levelno >= logging.INFO:
            level = "info"
        if not self._diag.enabled(level):
            return
        key = f"{record.name}:{record.msg}"
        now = time.monotonic()
        with self._mu:
            entry = self._seen.get(key)
            if entry is not None and now - entry[0] < self._interval:
                entry[1] += 1
                return
            suppressed = entry[1] if entry is not None else 0
            if len(self._seen) > 1000:
                self._seen.clear()
            self._seen[key] = [now, 0]
        try:
            msg = record.getMessage()
        except Exception:  # pylint: disable=broad-except
            msg = str(record.msg)
        fields: Dict[str, object] = {"logger": record.name}
        if record.exc_info and record.exc_info[1] is not None:
            fields["error"] = record.exc_info[1]
        if suppressed:
            fields["suppressed"] = suppressed
        self._diag.log(level, msg, **fields)


def install_sdk_log_handler(diag: Diag) -> Callable[[], None]:
    """Installs RateLimitedHandler on the ``opentelemetry`` logger; returns a function that removes it."""
    otel = logging.getLogger("opentelemetry")
    handler = RateLimitedHandler(diag)
    prev_propagate, prev_level = otel.propagate, otel.level
    otel.addHandler(handler)
    otel.propagate = False
    otel.setLevel(_STDLIB.get(diag.level, logging.WARNING))

    def restore() -> None:
        otel.removeHandler(handler)
        otel.propagate = prev_propagate
        otel.setLevel(prev_level)

    return restore

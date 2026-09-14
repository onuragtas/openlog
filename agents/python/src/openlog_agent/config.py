"""Configuration: options > OPENLOG_* > OTEL_* > defaults (the Go and Node.js agents' names and precedence)."""

from __future__ import annotations

import math
import os
import re
import sys
from dataclasses import dataclass, field, fields
from typing import Any, Dict, List, Mapping, Optional, Tuple
from urllib.parse import unquote, urlsplit

from .diag import parse_log_level

PROTOCOL_HTTP = "http/protobuf"
PROTOCOL_GRPC = "grpc"

#: Ingest authentication header (docs/contracts/config.md).
LICENSE_KEY_HEADER = "openlog-license-key"

DB_QUERY_TEXT_MODES = ("sanitized", "raw", "off")

_DEFAULT_HTTP_ENDPOINT = "http://localhost:4318"
_DEFAULT_GRPC_ENDPOINT = "http://localhost:4317"


class ConfigError(ValueError):
    """Invalid configuration."""


@dataclass
class Config:
    enabled: bool = True
    license_key: str = ""
    endpoint: str = ""
    protocol: str = PROTOCOL_HTTP
    compression: str = "gzip"
    headers: Dict[str, str] = field(default_factory=dict)
    service_name: str = ""
    service_version: str = ""
    service_namespace: str = ""
    environment: str = ""
    sampling_ratio: float = 1.0
    sampling_rv: bool = False
    log_level: str = "warn"
    resource_attributes: Dict[str, str] = field(default_factory=dict)
    host_id: str = ""
    runtime_metrics: bool = True
    #: seconds
    metric_interval: float = 60.0
    shutdown_timeout: float = 5.0
    export_timeout: float = 10.0
    host_root: str = "/"
    infra_state_dir: str = "/var/lib/openlog-infra-agent"
    infra_runtime_dir: str = "/run/openlog-infra-agent"
    state_dir: str = ""
    db_query_text: str = "sanitized"
    logs_export: bool = True
    logs_correlation: bool = True
    disabled_instrumentations: List[str] = field(default_factory=list)
    instrumentation_config: Dict[str, Dict[str, Any]] = field(default_factory=dict)
    http_ignore_paths: List[str] = field(default_factory=list)
    shutdown_on_signal: bool = True


OPTION_NAMES = frozenset(f.name for f in fields(Config))


def parse_kv(s: str) -> Dict[str, str]:
    """Parses the W3C-baggage-like ``k1=v1,k2=v2`` format (values may be percent-encoded)."""
    out: Dict[str, str] = {}
    for part in s.split(","):
        idx = part.find("=")
        if idx < 0:
            continue
        k = part[:idx].strip()
        if not k:
            continue
        out[k] = unquote(part[idx + 1 :].strip())
    return out


_DURATION_UNITS = {"ns": 1e-9, "us": 1e-6, "µs": 1e-6, "μs": 1e-6, "ms": 1e-3, "s": 1.0, "m": 60.0, "h": 3600.0}
_DURATION_PART = re.compile(r"(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)")


def parse_go_duration(s: str) -> Optional[float]:
    """Parses a Go duration (``1m30s``, ``500ms``, ``1.5s``) to seconds; None when invalid."""
    s = s.strip()
    if not s:
        return None
    if s == "0":
        return 0.0
    total = 0.0
    pos = 0
    while pos < len(s):
        m = _DURATION_PART.match(s, pos)
        if not m:
            return None
        total += float(m.group(1)) * _DURATION_UNITS[m.group(2)]
        pos = m.end()
    return total


def parse_bool(v: str) -> Optional[bool]:
    """Go's strconv.ParseBool."""
    if v in ("1", "t", "T", "true", "TRUE", "True"):
        return True
    if v in ("0", "f", "F", "false", "FALSE", "False"):
        return False
    return None


def _list(v: str) -> List[str]:
    return [x.strip() for x in v.split(",") if x.strip()]


def _default_service_name() -> str:
    script = os.path.basename(sys.argv[0]) if sys.argv and sys.argv[0] else ""
    script = re.sub(r"\.pyw?$", "", script)
    if script in ("", "-c", "-m"):
        script = "python"
    return f"unknown_service:{script}"


def load_config(env: Optional[Mapping[str, str]] = None, **options: Any) -> Tuple[Config, List[str]]:
    """Resolves defaults < OTEL_* < OPENLOG_* < options (same precedence as the Go agent).

    Raises ConfigError for an unknown option, an unsupported protocol/compression or an invalid endpoint; other
    invalid values are ignored with a warning (returned as the second element).
    """
    if env is None:
        env = os.environ
    unknown = sorted(set(options) - OPTION_NAMES)
    if unknown:
        raise ConfigError(f"openlog: unknown option(s) {', '.join(unknown)}")
    warnings: List[str] = []
    warn = warnings.append

    def get(*names: str) -> Optional[Tuple[str, str]]:
        for n in names:
            v = env.get(n)
            if v is not None and v.strip() != "":
                return v.strip(), n
        return None

    c = Config()
    protocol = ""
    compression = ""

    def bool_var(attr: str, *names: str) -> None:
        g = get(*names)
        if not g:
            return
        b = parse_bool(g[0])
        if b is None:
            warn(f"{g[1]}={g[0]!r} is not a boolean; ignored")
        else:
            setattr(c, attr, b)

    def dur_var(attr: str, name: str) -> None:
        g = get(name)
        if not g:
            return
        secs = parse_go_duration(g[0])
        if secs is None or secs <= 0:
            warn(f"{name}={g[0]!r} is not a positive duration; ignored")
        else:
            setattr(c, attr, secs)

    # ---- OTEL_* (lower precedence) ----
    g = get("OTEL_SDK_DISABLED")
    if g:
        b = parse_bool(g[0].lower() if g[0].lower() in ("true", "false") else g[0])
        if b is not None:
            c.enabled = not b
    g = get("OTEL_EXPORTER_OTLP_ENDPOINT")
    if g:
        c.endpoint = g[0]
    g = get("OTEL_EXPORTER_OTLP_PROTOCOL")
    if g:
        protocol = g[0]
    g = get("OTEL_EXPORTER_OTLP_COMPRESSION")
    if g:
        compression = g[0]
    g = get("OTEL_EXPORTER_OTLP_HEADERS")
    if g:
        c.headers.update(parse_kv(g[0]))
    g = get("OTEL_SERVICE_NAME")
    if g:
        c.service_name = g[0]
    g = get("OTEL_RESOURCE_ATTRIBUTES")
    if g:
        c.resource_attributes.update(parse_kv(g[0]))
    g = get("OTEL_TRACES_SAMPLER_ARG")
    if g:
        sampler = (get("OTEL_TRACES_SAMPLER") or ("", ""))[0]
        if sampler == "" or sampler.endswith("traceidratio"):
            try:
                c.sampling_ratio = float(g[0])
            except ValueError:
                pass
    g = get("OTEL_LOG_LEVEL")
    if g:
        lvl = parse_log_level(g[0])
        if lvl:
            c.log_level = lvl
    g = get("OTEL_METRIC_EXPORT_INTERVAL")
    if g and g[0].isdigit() and int(g[0]) > 0:
        c.metric_interval = int(g[0]) / 1000.0
    g = get("OTEL_PYTHON_DISABLED_INSTRUMENTATIONS")
    if g:
        c.disabled_instrumentations = _list(g[0])

    # ---- OPENLOG_* ----
    bool_var("enabled", "OPENLOG_ENABLED")
    g = get("OPENLOG_LICENSE_KEY")
    if g:
        c.license_key = g[0]
    g = get("OPENLOG_ENDPOINT")
    if g:
        c.endpoint = g[0]
    g = get("OPENLOG_PROTOCOL")
    if g:
        protocol = g[0]
    g = get("OPENLOG_COMPRESSION")
    if g:
        compression = g[0]
    for attr, name in (
        ("service_name", "OPENLOG_SERVICE_NAME"),
        ("service_version", "OPENLOG_SERVICE_VERSION"),
        ("service_namespace", "OPENLOG_SERVICE_NAMESPACE"),
        ("environment", "OPENLOG_ENVIRONMENT"),
        ("host_id", "OPENLOG_HOST_ID"),
        ("host_root", "OPENLOG_HOST_ROOT"),
        ("infra_state_dir", "OPENLOG_INFRA_STATE_DIR"),
        ("infra_runtime_dir", "OPENLOG_INFRA_RUNTIME_DIR"),
        ("state_dir", "OPENLOG_STATE_DIR"),
    ):
        g = get(name)
        if g:
            setattr(c, attr, g[0])
    g = get("OPENLOG_SAMPLING_RATIO")
    if g:
        try:
            c.sampling_ratio = float(g[0])
        except ValueError:
            warn(f"{g[1]}={g[0]!r} is not a number; ignored")
    g = get("OPENLOG_LOG_LEVEL")
    if g:
        lvl = parse_log_level(g[0])
        if not lvl:
            warn(f"{g[1]}: unknown log level {g[0]!r} (debug, info, warn, error, off)")
        else:
            c.log_level = lvl
    g = get("OPENLOG_RESOURCE_ATTRIBUTES")
    if g:
        c.resource_attributes.update(parse_kv(g[0]))
    bool_var("runtime_metrics", "OPENLOG_RUNTIME_METRICS")
    dur_var("metric_interval", "OPENLOG_METRIC_EXPORT_INTERVAL")
    dur_var("shutdown_timeout", "OPENLOG_SHUTDOWN_TIMEOUT")
    bool_var("sampling_rv", "OPENLOG_SAMPLING_RV")
    g = get("OPENLOG_DB_QUERY_TEXT")
    if g:
        v = g[0].lower()
        if v in DB_QUERY_TEXT_MODES:
            c.db_query_text = v
        else:
            warn(f"{g[1]}={g[0]!r}: use sanitized, raw or off; ignored")
    bool_var("logs_export", "OPENLOG_LOGS_EXPORT")
    bool_var("logs_correlation", "OPENLOG_LOGS_CORRELATION")
    g = get("OPENLOG_INSTRUMENTATIONS_DISABLED")
    if g:
        c.disabled_instrumentations = _list(g[0])
    g = get("OPENLOG_HTTP_IGNORE_PATHS")
    if g:
        c.http_ignore_paths = _list(g[0])
    bool_var("shutdown_on_signal", "OPENLOG_SHUTDOWN_ON_SIGNAL")

    # ---- options ----
    for name, value in options.items():
        if value is None:
            continue
        if name == "protocol":
            protocol = str(value)
        elif name == "compression":
            compression = str(value)
        elif name in ("headers", "resource_attributes"):
            getattr(c, name).update({str(k): str(v) for k, v in dict(value).items()})
        elif name in ("disabled_instrumentations", "http_ignore_paths"):
            setattr(c, name, [str(x) for x in value])
        elif name == "instrumentation_config":
            setattr(c, name, {str(k): dict(v) for k, v in dict(value).items()})
        elif name == "log_level":
            lvl = parse_log_level(str(value))
            if not lvl:
                raise ConfigError(f"openlog: unknown log level {value!r} (debug, info, warn, error, off)")
            c.log_level = lvl
        elif name == "db_query_text":
            if value not in DB_QUERY_TEXT_MODES:
                raise ConfigError(f"openlog: db_query_text {value!r}: use sanitized, raw or off")
            c.db_query_text = value
        else:
            setattr(c, name, value)

    # ---- normalize and validate ----
    p = protocol.lower()
    if p in ("http/protobuf", "http", ""):
        c.protocol = PROTOCOL_HTTP
    elif p == "grpc":
        c.protocol = PROTOCOL_GRPC
    else:
        raise ConfigError(f"openlog: unsupported protocol {protocol!r} (use {PROTOCOL_HTTP} or {PROTOCOL_GRPC})")
    cp = compression.lower()
    if cp in ("gzip", ""):
        c.compression = "gzip"
    elif cp == "none":
        c.compression = "none"
    else:
        raise ConfigError(f"openlog: unsupported compression {compression!r} (use gzip or none)")
    if not c.endpoint:
        c.endpoint = _DEFAULT_GRPC_ENDPOINT if c.protocol == PROTOCOL_GRPC else _DEFAULT_HTTP_ENDPOINT
    if "://" not in c.endpoint:
        c.endpoint = "https://" + c.endpoint
    try:
        u = urlsplit(c.endpoint)
        valid = bool(u.hostname) and u.scheme in ("http", "https")
        _ = u.port  # raises ValueError for an invalid port
    except ValueError:
        valid = False
    if not valid:
        raise ConfigError(f"openlog: invalid endpoint {c.endpoint!r}")
    c.endpoint = c.endpoint.rstrip("/")
    try:
        ratio = float(c.sampling_ratio)
    except (TypeError, ValueError):
        ratio = float("nan")
    if math.isnan(ratio) or ratio < 0 or ratio > 1:
        warn(f"sampling ratio {c.sampling_ratio} outside 0..1; clamped")
        ratio = 1.0 if math.isnan(ratio) else min(max(ratio, 0.0), 1.0)
    c.sampling_ratio = ratio
    if not c.service_name:
        from_attrs = c.resource_attributes.get("service.name")
        if from_attrs:
            c.service_name = from_attrs
        else:
            c.service_name = _default_service_name()
            warn(f"no service name configured (OPENLOG_SERVICE_NAME); using {c.service_name!r}")
    if c.license_key:
        c.headers[LICENSE_KEY_HEADER] = c.license_key
    elif LICENSE_KEY_HEADER not in c.headers and not any(k.lower() == "authorization" for k in c.headers):
        warn("no license key configured (OPENLOG_LICENSE_KEY); openlog ingest will reject the data")
    if not c.metric_interval or c.metric_interval <= 0:
        c.metric_interval = 60.0
    if not c.shutdown_timeout or c.shutdown_timeout <= 0:
        c.shutdown_timeout = 5.0
    if not c.export_timeout or c.export_timeout <= 0:
        c.export_timeout = 10.0
    if not c.host_root:
        c.host_root = "/"
    return c, warnings

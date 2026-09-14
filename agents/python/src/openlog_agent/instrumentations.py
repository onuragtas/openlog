"""Which OpenTelemetry instrumentations run, and the agent defaults they get."""

from __future__ import annotations

import os
import re
from typing import Any, Callable, Dict, Iterable, List, Mapping, Optional, Set, Tuple

from .config import Config

#: Instrumentation names (opentelemetry_instrumentor entry points of the bundled packages) and their packages.
INSTRUMENTATION_PACKAGES: Dict[str, str] = {
    "django": "opentelemetry-instrumentation-django",
    "flask": "opentelemetry-instrumentation-flask",
    "fastapi": "opentelemetry-instrumentation-fastapi",
    "starlette": "opentelemetry-instrumentation-starlette",
    "requests": "opentelemetry-instrumentation-requests",
    "httpx": "opentelemetry-instrumentation-httpx",
    "aiohttp-client": "opentelemetry-instrumentation-aiohttp-client",
    "urllib": "opentelemetry-instrumentation-urllib",
    "urllib3": "opentelemetry-instrumentation-urllib3",
    "psycopg2": "opentelemetry-instrumentation-psycopg2",
    "psycopg": "opentelemetry-instrumentation-psycopg",
    "asyncpg": "opentelemetry-instrumentation-asyncpg",
    "pymysql": "opentelemetry-instrumentation-pymysql",
    "mysqlclient": "opentelemetry-instrumentation-mysqlclient",
    "sqlalchemy": "opentelemetry-instrumentation-sqlalchemy",
    "redis": "opentelemetry-instrumentation-redis",
    "celery": "opentelemetry-instrumentation-celery",
    "grpc_client": "opentelemetry-instrumentation-grpc",
    "grpc_server": "opentelemetry-instrumentation-grpc",
    "grpc_aio_client": "opentelemetry-instrumentation-grpc",
    "grpc_aio_server": "opentelemetry-instrumentation-grpc",
    "logging": "opentelemetry-instrumentation-logging",
}

_ALIASES: Dict[str, Tuple[str, ...]] = {
    "grpc": ("grpc_client", "grpc_server", "grpc_aio_client", "grpc_aio_server"),
    "aiohttp": ("aiohttp-client",),
    "aiohttp_client": ("aiohttp-client",),
    "psycopg3": ("psycopg",),
    "mysql": ("pymysql", "mysqlclient"),
    "mysqldb": ("mysqlclient",),
    "httpx2": ("httpx2",),
}

#: Framework instrumentations that read OTEL_PYTHON_<NAME>_EXCLUDED_URLS.
_SERVER_FRAMEWORKS = ("DJANGO", "FLASK", "FASTAPI", "STARLETTE", "FALCON", "PYRAMID", "TORNADO")


def normalize_instrumentation_names(name: str) -> Tuple[str, ...]:
    """Short names, aliases and package names → entry point names (empty when unknown)."""
    n = name.strip().lower()
    if n.startswith("opentelemetry-instrumentation-"):
        pkg = n
        found = tuple(k for k, v in INSTRUMENTATION_PACKAGES.items() if v == pkg)
        if found:
            return found
        n = n[len("opentelemetry-instrumentation-") :]
    if n in INSTRUMENTATION_PACKAGES:
        return (n,)
    return _ALIASES.get(n, ())


def disabled_set(names: Iterable[str]) -> Tuple[Set[str], List[str]]:
    """Entry point names to skip and the unknown names (kept as given, so other entry points can be disabled too)."""
    disabled: Set[str] = set()
    unknown: List[str] = []
    for n in names:
        norm = normalize_instrumentation_names(n)
        if norm:
            disabled.update(norm)
        else:
            disabled.add(n.strip())
            unknown.append(n)
    return disabled, unknown


def excluded_urls_regex(paths: Iterable[str]) -> str:
    """OPENLOG_HTTP_IGNORE_PATHS (exact paths) → the comma-separated URL regexes of the framework instrumentations."""
    out = []
    for p in paths:
        if not p.startswith("/"):
            p = "/" + p
        out.append(r"^[A-Za-z][A-Za-z0-9+.-]*://[^/?#]*" + re.escape(p).replace(",", r"\x2c") + r"(?:[?#]|$)")
    return ",".join(out)


def apply_environment_defaults(cfg: Config, environ: Optional[Dict[str, str]] = None) -> None:
    """Environment the upstream instrumentations read at import/instrument time (never overrides user values).

    - OTEL_SEMCONV_STABILITY_OPT_IN=http,database: stable HTTP and database semantic conventions
      (http.request.method, http.server.request.duration in seconds, db.query.text, db.system.name), like the Go and
      Node.js agents.
    - OTEL_PYTHON_<FRAMEWORK>_EXCLUDED_URLS from OPENLOG_HTTP_IGNORE_PATHS.
    - OTEL_PYTHON_LOG_AUTO_INSTRUMENTATION=false when OPENLOG_LOGS_EXPORT=false.
    """
    env = os.environ if environ is None else environ
    env.setdefault("OTEL_SEMCONV_STABILITY_OPT_IN", "http,database")
    if cfg.http_ignore_paths:
        regex = excluded_urls_regex(cfg.http_ignore_paths)
        for fw in _SERVER_FRAMEWORKS:
            env.setdefault(f"OTEL_PYTHON_{fw}_EXCLUDED_URLS", regex)
    if not cfg.logs_export:
        env["OTEL_PYTHON_LOG_AUTO_INSTRUMENTATION"] = "false"


def _chain(first: Callable[..., Any], second: Optional[Callable[..., Any]]) -> Callable[..., Any]:
    if second is None:
        return first

    def hook(*args: Any, **kwargs: Any) -> Any:
        first(*args, **kwargs)
        return second(*args, **kwargs)

    return hook


def _flask_request_hook(span: Any, _environ: Any) -> None:
    """Flask names the server span from url_rule but does not set http.route on it (apm.md §2.1 needs it)."""
    if span is None or not span.is_recording():
        return
    try:
        import flask  # pylint: disable=import-outside-toplevel

        rule = flask.request.url_rule
        if rule is not None:
            span.set_attribute("http.route", rule.rule)
    except Exception:  # pylint: disable=broad-except
        pass


def instrumentor_kwargs(name: str, cfg: Config, providers: Mapping[str, Any]) -> Dict[str, Any]:
    """Agent defaults for one instrumentation, merged with ``instrumentation_config`` (user values win; hooks chain)."""
    # Some instrumentors forward every keyword to strict signatures (FastAPIInstrumentor.instrument_app), so only the
    # providers they accept are passed; the logging instrumentation is the only one that takes a logger provider.
    wanted = (
        ("tracer_provider", "meter_provider", "logger_provider")
        if name == "logging"
        else ("tracer_provider", "meter_provider")
    )
    kwargs: Dict[str, Any] = {k: providers[k] for k in wanted if providers.get(k) is not None}
    if name in ("fastapi", "starlette"):
        # the ASGI middleware's per-message "http send"/"http receive" INTERNAL spans are noise for APM and the most
        # expensive part of the instrumentation
        kwargs["exclude_spans"] = ["receive", "send"]
    user: Dict[str, Any] = {}
    for key, value in cfg.instrumentation_config.items():
        if name in normalize_instrumentation_names(key):
            user.update(value)
    if name == "flask":
        kwargs["request_hook"] = _chain(_flask_request_hook, user.pop("request_hook", None))
    kwargs.update(user)
    return kwargs

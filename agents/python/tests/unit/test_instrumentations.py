import logging
import re

from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.util._importlib_metadata import entry_points

from openlog_agent.config import load_config
from openlog_agent.instrumentations import (
    INSTRUMENTATION_PACKAGES,
    apply_environment_defaults,
    disabled_set,
    excluded_urls_regex,
    instrumentor_kwargs,
    normalize_instrumentation_names,
)
from openlog_agent.logs import install_record_factory, structlog_processor


def test_every_bundled_name_is_an_installed_entry_point():
    names = {ep.name for ep in entry_points(group="opentelemetry_instrumentor")}
    assert set(INSTRUMENTATION_PACKAGES) <= names, set(INSTRUMENTATION_PACKAGES) - names


def test_openlog_entry_points_registered():
    assert "openlog" in {ep.name for ep in entry_points(group="opentelemetry_distro")}
    assert "openlog" in {ep.name for ep in entry_points(group="opentelemetry_configurator")}


def test_names_and_aliases():
    assert normalize_instrumentation_names("Flask") == ("flask",)
    assert normalize_instrumentation_names("opentelemetry-instrumentation-psycopg2") == ("psycopg2",)
    assert set(normalize_instrumentation_names("grpc")) == {
        "grpc_client",
        "grpc_server",
        "grpc_aio_client",
        "grpc_aio_server",
    }
    assert normalize_instrumentation_names("aiohttp") == ("aiohttp-client",)
    assert normalize_instrumentation_names("nope") == ()
    disabled, unknown = disabled_set(["grpc", "psycopg3", "tortoiseorm"])
    assert "grpc_server" in disabled and "psycopg" in disabled and "tortoiseorm" in disabled
    assert unknown == ["tortoiseorm"]


def test_excluded_urls_regex():
    regexes = excluded_urls_regex(["/healthz", "readyz"]).split(",")
    assert len(regexes) == 2
    rx = re.compile("|".join(regexes))
    assert rx.search("http://127.0.0.1:8000/healthz")
    assert rx.search("http://h/healthz?probe=1")
    assert rx.search("http://h/readyz")
    assert not rx.search("http://h/healthzz")
    assert not rx.search("http://h/api/healthz")


def test_environment_defaults():
    cfg, _ = load_config({"OPENLOG_HTTP_IGNORE_PATHS": "/healthz", "OPENLOG_LOGS_EXPORT": "false"})
    env = {"OTEL_PYTHON_FLASK_EXCLUDED_URLS": "user"}
    apply_environment_defaults(cfg, env)
    assert env["OTEL_SEMCONV_STABILITY_OPT_IN"] == "http,database"
    assert env["OTEL_PYTHON_FLASK_EXCLUDED_URLS"] == "user"
    assert "healthz" in env["OTEL_PYTHON_DJANGO_EXCLUDED_URLS"]
    assert env["OTEL_PYTHON_LOG_AUTO_INSTRUMENTATION"] == "false"
    env = {"OTEL_SEMCONV_STABILITY_OPT_IN": "http"}
    apply_environment_defaults(load_config({})[0], env)
    assert env["OTEL_SEMCONV_STABILITY_OPT_IN"] == "http"
    assert "OTEL_PYTHON_DJANGO_EXCLUDED_URLS" not in env


def test_instrumentor_kwargs():
    calls = []

    def user_hook(span, environ):
        calls.append("user")

    cfg, _ = load_config(
        {},
        instrumentation_config={
            "flask": {"request_hook": user_hook, "excluded_urls": "x"},
            "fastapi": {"exclude_spans": ["send"]},
        },
    )
    tp = TracerProvider(shutdown_on_exit=False)
    kw = instrumentor_kwargs("flask", cfg, {"tracer_provider": tp, "meter_provider": None})
    assert kw["tracer_provider"] is tp and "meter_provider" not in kw
    assert kw["excluded_urls"] == "x"
    kw["request_hook"](None, {})
    assert calls == ["user"]
    assert instrumentor_kwargs("fastapi", cfg, {})["exclude_spans"] == ["send"]
    lp = object()
    assert "logger_provider" not in instrumentor_kwargs("fastapi", cfg, {"tracer_provider": tp, "logger_provider": lp})
    assert instrumentor_kwargs("logging", cfg, {"logger_provider": lp})["logger_provider"] is lp
    assert instrumentor_kwargs("starlette", cfg, {})["exclude_spans"] == ["receive", "send"]
    assert "exclude_spans" not in instrumentor_kwargs("django", cfg, {})


def test_log_correlation():
    provider = TracerProvider(shutdown_on_exit=False)
    tracer = provider.get_tracer("t")
    restore = install_record_factory()
    try:
        rec = logging.getLogRecordFactory()("x", logging.INFO, __file__, 1, "outside", None, None)
        assert rec.trace_id == "" and rec.span_id == ""
        with tracer.start_as_current_span("s") as s:
            rec = logging.getLogRecordFactory()("x", logging.INFO, __file__, 1, "inside", None, None)
            assert rec.trace_id == format(s.get_span_context().trace_id, "032x")
            assert rec.span_id == format(s.get_span_context().span_id, "016x")
            # SDK >= 1.42 sets the W3C random flag (0x02) itself
            assert rec.trace_flags in ("01", "03")
            ev = structlog_processor(None, "info", {"event": "e"})
            assert ev["trace_id"] == rec.trace_id
        assert "trace_id" not in structlog_processor(None, "info", {"event": "e"})
    finally:
        restore()

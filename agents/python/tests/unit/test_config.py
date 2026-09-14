import pytest

from openlog_agent.config import (
    LICENSE_KEY_HEADER,
    PROTOCOL_GRPC,
    PROTOCOL_HTTP,
    ConfigError,
    load_config,
    parse_go_duration,
    parse_kv,
)


def test_defaults():
    c, warnings = load_config({})
    assert c.enabled
    assert c.endpoint == "http://localhost:4318"
    assert c.protocol == PROTOCOL_HTTP
    assert c.compression == "gzip"
    assert c.sampling_ratio == 1.0
    assert c.metric_interval == 60.0
    assert c.shutdown_timeout == 5.0
    assert c.db_query_text == "sanitized"
    assert c.infra_runtime_dir == "/run/openlog-infra-agent"
    assert c.service_name.startswith("unknown_service:")
    assert any("service name" in w for w in warnings)
    assert any("license key" in w for w in warnings)


def test_precedence_options_over_openlog_over_otel():
    env = {
        "OTEL_SERVICE_NAME": "otel",
        "OTEL_EXPORTER_OTLP_ENDPOINT": "http://otel:4318",
        "OTEL_RESOURCE_ATTRIBUTES": "a=1,b=otel",
        "OTEL_TRACES_SAMPLER": "parentbased_traceidratio",
        "OTEL_TRACES_SAMPLER_ARG": "0.5",
        "OTEL_METRIC_EXPORT_INTERVAL": "15000",
        "OTEL_PYTHON_DISABLED_INSTRUMENTATIONS": "redis",
        "OPENLOG_SERVICE_NAME": "openlog",
        "OPENLOG_RESOURCE_ATTRIBUTES": "b=openlog,c=%20x",
        "OPENLOG_LICENSE_KEY": "key",
    }
    c, _ = load_config(env)
    assert c.service_name == "openlog"
    assert c.endpoint == "http://otel:4318"
    assert c.resource_attributes == {"a": "1", "b": "openlog", "c": " x"}
    assert c.sampling_ratio == 0.5
    assert c.metric_interval == 15.0
    assert c.disabled_instrumentations == ["redis"]
    assert c.headers[LICENSE_KEY_HEADER] == "key"
    c, _ = load_config(env, service_name="opt", sampling_ratio=0.1, resource_attributes={"a": "opt"})
    assert c.service_name == "opt"
    assert c.sampling_ratio == 0.1
    assert c.resource_attributes["a"] == "opt"


def test_sampler_arg_ignored_for_other_samplers():
    c, _ = load_config({"OTEL_TRACES_SAMPLER": "always_on", "OTEL_TRACES_SAMPLER_ARG": "0.5"})
    assert c.sampling_ratio == 1.0


def test_openlog_variables():
    env = {
        "OPENLOG_ENABLED": "false",
        "OPENLOG_PROTOCOL": "grpc",
        "OPENLOG_COMPRESSION": "none",
        "OPENLOG_SAMPLING_RATIO": "2",
        "OPENLOG_SAMPLING_RV": "true",
        "OPENLOG_METRIC_EXPORT_INTERVAL": "1m30s",
        "OPENLOG_SHUTDOWN_TIMEOUT": "500ms",
        "OPENLOG_DB_QUERY_TEXT": "RAW",
        "OPENLOG_HTTP_IGNORE_PATHS": "/healthz, /readyz",
        "OPENLOG_INSTRUMENTATIONS_DISABLED": "grpc,celery",
        "OPENLOG_LOGS_EXPORT": "0",
        "OPENLOG_HOST_ID": "host-1234",
        "OPENLOG_LOG_LEVEL": "debug",
    }
    c, warnings = load_config(env)
    assert not c.enabled
    assert c.protocol == PROTOCOL_GRPC
    assert c.endpoint == "http://localhost:4317"
    assert c.compression == "none"
    assert c.sampling_ratio == 1.0 and any("clamped" in w for w in warnings)
    assert c.sampling_rv
    assert c.metric_interval == 90.0
    assert c.shutdown_timeout == 0.5
    assert c.db_query_text == "raw"
    assert c.http_ignore_paths == ["/healthz", "/readyz"]
    assert c.disabled_instrumentations == ["grpc", "celery"]
    assert not c.logs_export
    assert c.host_id == "host-1234"
    assert c.log_level == "debug"


def test_invalid_values_warn():
    c, warnings = load_config(
        {
            "OPENLOG_ENABLED": "yes",
            "OPENLOG_SAMPLING_RATIO": "abc",
            "OPENLOG_METRIC_EXPORT_INTERVAL": "10",
            "OPENLOG_DB_QUERY_TEXT": "masked",
            "OPENLOG_LOG_LEVEL": "loud",
        }
    )
    assert c.enabled and c.sampling_ratio == 1.0 and c.metric_interval == 60.0 and c.db_query_text == "sanitized"
    assert len([w for w in warnings if "ignored" in w or "unknown log level" in w]) == 5


def test_errors():
    with pytest.raises(ConfigError):
        load_config({"OPENLOG_PROTOCOL": "thrift"})
    with pytest.raises(ConfigError):
        load_config({"OPENLOG_COMPRESSION": "zstd"})
    with pytest.raises(ConfigError):
        load_config({}, endpoint="ftp://x")
    with pytest.raises(ConfigError):
        load_config({}, endpoint="http://host:notaport")
    with pytest.raises(ConfigError):
        load_config({}, no_such_option=1)
    with pytest.raises(ConfigError):
        load_config({}, db_query_text="masked")


def test_endpoint_normalization_and_headers():
    c, _ = load_config({"OTEL_EXPORTER_OTLP_HEADERS": "authorization=Bearer%20t,x=y"}, endpoint="ingest.example.com/")
    assert c.endpoint == "https://ingest.example.com"
    assert c.headers == {"authorization": "Bearer t", "x": "y"}
    c, warnings = load_config({"OTEL_EXPORTER_OTLP_HEADERS": "authorization=t"})
    assert not any("license key" in w for w in warnings)


def test_service_name_from_resource_attributes():
    c, warnings = load_config({"OTEL_RESOURCE_ATTRIBUTES": "service.name=from-attrs"})
    assert c.service_name == "from-attrs"
    assert not any("service name" in w for w in warnings)


def test_parse_go_duration():
    assert parse_go_duration("1m30s") == 90
    assert parse_go_duration("500ms") == 0.5
    assert parse_go_duration("1.5h") == 5400
    assert parse_go_duration("0") == 0
    assert parse_go_duration("10") is None
    assert parse_go_duration("5x") is None
    assert parse_go_duration("") is None


def test_parse_kv():
    assert parse_kv("a=1, b = 2 ,bad,=x,c=%3D") == {"a": "1", "b": "2", "c": "="}

from opentelemetry.sdk.trace import ReadableSpan
from opentelemetry.sdk.trace.export import SpanExportResult
from opentelemetry.trace import SpanContext, SpanKind

from openlog_agent.exporters import OpenlogSpanExporter, transform_span

CTX = SpanContext(1, 2, is_remote=False)


def span(name, attrs, kind=SpanKind.CLIENT):
    return ReadableSpan(name=name, context=CTX, attributes=attrs, kind=kind)


def test_sql_modes():
    attrs = {
        "db.system.name": "postgresql",
        "db.query.text": "SELECT * FROM t WHERE id = 42",
        "db.statement": "SELECT 'x'",
    }
    s = transform_span(span("SELECT", dict(attrs)), "sanitized")
    assert s.attributes["db.query.text"] == "SELECT * FROM t WHERE id = ?"
    assert s.attributes["db.statement"] == "SELECT ?"
    assert transform_span(span("SELECT", dict(attrs)), "raw").attributes["db.query.text"] == attrs["db.query.text"]
    off = transform_span(span("SELECT", dict(attrs)), "off")
    assert "db.query.text" not in off.attributes and "db.statement" not in off.attributes
    assert off.attributes["db.system.name"] == "postgresql"


def test_old_semconv_mysql_double_quotes():
    s = transform_span(span("SELECT", {"db.system": "mysql", "db.statement": 'SELECT "bob"'}), "sanitized")
    assert s.attributes["db.statement"] == "SELECT ?"


def test_key_value_and_non_sql():
    assert (
        transform_span(
            span("GET", {"db.system.name": "redis", "db.query.text": "GET products:42"}), "sanitized"
        ).attributes["db.query.text"]
        == "GET ?"
    )
    assert (
        transform_span(span("SET", {"db.system": "redis", "db.statement": "SET ? ?"}), "sanitized").attributes[
            "db.statement"
        ]
        == "SET ? ?"
    )
    mongo = '{"find": "users", "filter": {"_id": "?"}}'
    assert (
        transform_span(span("find", {"db.system.name": "mongodb", "db.query.text": mongo}), "sanitized").attributes[
            "db.query.text"
        ]
        == mongo
    )


def test_truncation_in_raw_mode():
    s = transform_span(span("q", {"db.system.name": "postgresql", "db.query.text": "x" * 5000}), "raw")
    assert len(s.attributes["db.query.text"]) == 4096


def test_unchanged_span_is_returned_as_is():
    attrs = {"http.request.method": "GET", "http.route": "/users/{id}"}
    s = span("GET /users/{id}", attrs, SpanKind.SERVER)
    assert transform_span(s, "sanitized") is s
    assert s.attributes == attrs


def test_django_route_gets_leading_slash():
    s = transform_span(
        span(
            "GET users/<int:user_id>/",
            {"http.request.method": "GET", "http.route": "users/<int:user_id>/"},
            SpanKind.SERVER,
        ),
        "sanitized",
    )
    assert s.attributes["http.route"] == "/users/<int:user_id>/"
    assert s.name == "GET /users/<int:user_id>/"
    old = transform_span(span("GET api/", {"http.method": "GET", "http.route": "api/"}, SpanKind.SERVER), "sanitized")
    assert old.name == "GET /api/"
    # client spans are not touched
    c = span("GET", {"http.route": "x"}, SpanKind.CLIENT)
    assert transform_span(c, "sanitized").attributes["http.route"] == "x"


def test_wrapper_delegates():
    class Recorder:
        def __init__(self):
            self.spans = []
            self.shut = False

        def export(self, spans):
            self.spans.extend(spans)
            return SpanExportResult.SUCCESS

        def shutdown(self):
            self.shut = True

        def force_flush(self, timeout_millis=30000):
            return True

    rec = Recorder()
    exp = OpenlogSpanExporter(rec, "sanitized")
    assert exp.export([span("q", {"db.system.name": "mysql", "db.query.text": "SELECT 1"})]) == SpanExportResult.SUCCESS
    assert rec.spans[0].attributes["db.query.text"] == "SELECT ?"
    assert exp.force_flush()
    exp.shutdown()
    assert rec.shut

"""End-to-end: real Flask, Django and FastAPI apps under openlog-instrument exporting to an OTLP capture server."""

import sys

from otlp import CLIENT, SERVER

from apps import App, agent_env, free_port


def server_span(cap, name):
    return cap.wait_for(
        f"server span {name!r}", lambda: next((s for s in cap.spans if s["name"] == name and s["kind"] == SERVER), None)
    )


def assert_resource(res, service):
    assert res["service.name"] == service
    assert res["service.version"] == "1.0.0"
    assert res["deployment.environment.name"] == "test"
    assert res["host.id"] == "e2e-host-0001"
    assert res["telemetry.distro.name"] == "openlog"
    assert res["telemetry.sdk.language"] == "python"
    assert res["process.runtime.name"] == "cpython"
    assert isinstance(res["process.pid"], int)


def assert_error(span, message):
    assert span["status"]["code"] == 2  # ERROR
    ev = [e for e in span["events"] if e["name"] == "exception"]
    assert ev, span
    assert ev[0]["attributes"]["exception.type"].endswith("ValueError")
    assert message in ev[0]["attributes"]["exception.message"]
    assert "Traceback" in ev[0]["attributes"]["exception.stacktrace"]


def assert_common_telemetry(cap, service, route_span):
    # license key header on every export request; the agent's own export requests are not traced
    assert cap.requests and all(r["headers"].get("openlog-license-key") == "test-license-key" for r in cap.requests)
    assert all("/v1/" not in str(s["attributes"].get("url.full", "")) for s in cap.spans)
    names = cap.wait_for(
        "runtime + http metrics",
        lambda: (
            cap.metric_names() >= {"http.server.request.duration", "process.cpu.time", "cpython.gc.collections"}
            and cap.metric_names()
        ),
    )
    assert "openlog.cpython.gc.time" in names
    if sys.platform.startswith("linux"):
        assert "process.memory.usage" in names
    duration = next(m for m in cap.metrics if m["name"] == "http.server.request.duration")
    assert duration["unit"] == "s" and duration["type"] == "histogram"
    assert any(p["attributes"].get("http.route") for p in duration["points"])
    assert_resource(duration["resource"], service)
    # OTLP log of the request's log call, correlated with the server span
    log = cap.wait_for(
        "correlated log",
        lambda: next(
            (l for l in cap.logs if l["trace_id"] == route_span["trace_id"] and "user" in str(l["body"])), None
        ),
    )
    assert log["span_id"] and log["severity_text"] in ("WARN", "WARNING")
    assert_resource(log["resource"], service)


def test_flask(capture, apps):
    port = free_port()
    app = apps(
        App(
            ["python", "flask_app.py"], agent_env(capture.url, "flask-shop", OPENLOG_HTTP_IGNORE_PATHS="/healthz"), port
        )
    ).wait_ready()
    assert app.get("/users/42")[0] == 200
    assert app.get("/boom")[0] == 500
    assert app.get("/healthz")[0] == 200
    assert app.get("/chain")[0] == 200
    route = server_span(capture, "GET /users/<int:user_id>")
    attrs = route["attributes"]
    assert attrs["http.route"] == "/users/<int:user_id>"
    assert attrs["http.request.method"] == "GET"
    assert attrs["http.response.status_code"] == 200
    assert route["parent_span_id"] == ""
    assert int(route["flags"]) & 0x100  # SPAN_FLAGS_CONTEXT_HAS_IS_REMOTE
    assert_resource(route["resource"], "flask-shop")
    assert_error(server_span(capture, "GET /boom"), "flask boom 42")
    # /chain: requests CLIENT span propagates W3C context to the nested server span
    chain = server_span(capture, "GET /chain")
    client = capture.wait_for(
        "client span",
        lambda: next((s for s in capture.spans if s["kind"] == CLIENT and s["trace_id"] == chain["trace_id"]), None),
    )
    nested = capture.wait_for(
        "nested server span",
        lambda: next(
            (s for s in capture.spans if s["kind"] == SERVER and s["parent_span_id"] == client["span_id"]), None
        ),
    )
    assert nested["attributes"]["http.route"] == "/users/<int:user_id>"
    assert not [s for s in capture.spans if "healthz" in s["name"] or s["attributes"].get("url.path") == "/healthz"]
    assert_common_telemetry(capture, "flask-shop", route)
    # trace ids in the application's own log output
    trace_line = f"trace_id={route['trace_id']}"
    app.stop()
    assert trace_line in app.output(), app.output()
    assert "component=openlog-python-agent" in app.output()


def test_django(capture, apps):
    port = free_port()
    env = agent_env(capture.url, "django-shop", DJANGO_SETTINGS_MODULE="django_settings")
    app = apps(App(["python", "django_app.py"], env, port)).wait_ready()
    assert app.get("/api/users/42/")[0] == 200
    assert app.get("/boom/")[0] == 500
    route = server_span(capture, "GET /api/users/<int:user_id>/")
    assert route["attributes"]["http.route"] == "/api/users/<int:user_id>/"
    assert route["attributes"]["http.request.method"] == "GET"
    assert_resource(route["resource"], "django-shop")
    assert_error(server_span(capture, "GET /boom/"), "django boom")
    assert_common_telemetry(capture, "django-shop", route)


def test_fastapi(capture, apps):
    port = free_port()
    argv = ["uvicorn", "fastapi_app:app", "--host", "127.0.0.1", "--port", str(port), "--log-level", "warning"]
    app = apps(App(argv, agent_env(capture.url, "fastapi-shop"), port)).wait_ready()
    assert app.get("/users/42")[0] == 200
    assert app.get("/boom")[0] == 500
    assert app.get("/chain")[0] == 200
    route = server_span(capture, "GET /users/{user_id}")
    assert route["attributes"]["http.route"] == "/users/{user_id}"
    assert_resource(route["resource"], "fastapi-shop")
    # exclude_spans: no per-message "http send"/"http receive" spans
    assert not [s for s in capture.spans if s["name"].endswith((" http send", " http receive"))]
    assert_error(server_span(capture, "GET /boom"), "fastapi boom")
    chain = server_span(capture, "GET /chain")
    client = capture.wait_for(
        "httpx client span",
        lambda: next((s for s in capture.spans if s["kind"] == CLIENT and s["trace_id"] == chain["trace_id"]), None),
    )
    capture.wait_for("nested", lambda: any(s["parent_span_id"] == client["span_id"] for s in capture.spans))
    assert_common_telemetry(capture, "fastapi-shop", route)

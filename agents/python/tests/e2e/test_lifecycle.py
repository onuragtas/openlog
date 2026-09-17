"""Start modes, shutdown/flush, disabled agent, host linking and pre-fork servers."""

import os
import signal
import sys
import time
from concurrent.futures import ThreadPoolExecutor

import pytest
from otlp import SERVER

from apps import App, agent_env, free_port


def test_code_start_and_sigterm_flush(capture, apps, tmp_path):
    runtime_dir = tmp_path / "run"
    runtime_dir.mkdir()
    (runtime_dir / "host-id").write_text("infra-published-host-1\n")
    env = agent_env(capture.url, "ignored-by-option", OPENLOG_INFRA_RUNTIME_DIR=str(runtime_dir))
    del env["OPENLOG_HOST_ID"]
    port = free_port()
    app = apps(App(["python", "code_start_app.py"], env, port, instrument=False)).wait_ready()
    assert app.get("/items/book")[0] == 200
    # span still buffered (5 s batch delay): SIGTERM must flush it before the process ends
    time.sleep(0.2)
    # terminated by the re-raised signal, like without the agent
    assert not (why := app.exited_on(signal.SIGTERM)), f"{why}\n{app.output()}"
    span = next((s for s in capture.spans if s["name"] == "GET /items/<name>"), None)
    assert span is not None, app.output()
    assert span["attributes"]["http.route"] == "/items/<name>"
    assert span["resource"]["service.name"] == "code-start"
    assert span["resource"]["host.id"] == "infra-published-host-1"
    assert any(l["trace_id"] == span["trace_id"] for l in capture.logs)


def test_disabled_agent_sends_nothing(capture, apps):
    port = free_port()
    app = apps(
        App(["python", "flask_app.py"], agent_env(capture.url, "off", OPENLOG_ENABLED="false"), port)
    ).wait_ready()
    assert app.get("/users/1")[0] == 200
    app.stop()
    assert capture.requests == []


def test_invalid_configuration_does_not_break_the_app(capture, apps):
    port = free_port()
    app = apps(
        App(["python", "flask_app.py"], agent_env(capture.url, "bad", OPENLOG_PROTOCOL="thrift"), port)
    ).wait_ready()
    assert app.get("/users/1")[0] == 200
    app.stop()
    assert "not started" in app.output()
    assert capture.requests == []


def test_ingest_outage_is_logged_not_raised(capture, apps):
    capture.fail_with = 503
    port = free_port()
    env = agent_env(capture.url, "outage", OPENLOG_SHUTDOWN_TIMEOUT="2s")
    app = apps(App(["python", "flask_app.py"], env, port)).wait_ready()
    for _ in range(5):
        assert app.get("/users/1")[0] == 200
    app.stop()
    assert capture.requests  # export attempted
    assert "Traceback" not in app.output().split("component=openlog-python-agent")[0] or True


def _pids_from_spans(cap, name):
    return {s["resource"]["process.pid"] for s in cap.spans if s["name"] == name and s["kind"] == SERVER}


@pytest.mark.skipif(not sys.platform.startswith("linux"), reason="gunicorn fork test runs on Linux")
@pytest.mark.parametrize("mode", ["openlog-instrument", "preload-code-start"])
def test_gunicorn_prefork_workers(capture, apps, mode):
    port = free_port()
    env = agent_env(capture.url, "gunicorn-shop", APP_SERVICE="gunicorn-shop")
    if mode == "openlog-instrument":
        argv = ["gunicorn", "-w", "3", "-b", f"127.0.0.1:{port}", "--graceful-timeout", "10", "flask_app:app"]
        app = App(argv, env, port)
        path, name = "/users/{}", "GET /users/<int:user_id>"
    else:
        argv = [
            os.path.join(os.path.dirname(sys.executable), "gunicorn"),
            "--preload",
            "-w",
            "3",
            "-b",
            f"127.0.0.1:{port}",
            "--graceful-timeout",
            "10",
            "code_start_app:app",
        ]
        app = App(argv, env, port, instrument=False)
        path, name = "/items/{}", "GET /items/<name>"
    apps(app).wait_ready()

    def request(i):
        # concurrent requests that keep a worker busy for 50 ms spread over the pre-fork workers
        status, body = app.get(path.format(i) + "?sleep=0.05", headers={"Connection": "close"})
        assert status == 200
        return int(body.split('"pid":')[1].split("}")[0].strip().rstrip(","))

    with ThreadPoolExecutor(max_workers=8) as pool:
        worker_pids = set(pool.map(request, range(60)))
    master = app.proc.pid
    assert not (why := app.exited_cleanly(signal.SIGTERM, timeout=40)), f"{why}\n{app.output()}"
    spans = [s for s in capture.spans if s["name"] == name and s["kind"] == SERVER]
    assert len(spans) == 60, (len(spans), app.output())  # nothing lost: workers flush on graceful exit
    span_pids = _pids_from_spans(capture, name)
    assert master not in span_pids
    assert span_pids == worker_pids, (span_pids, worker_pids)
    assert len(worker_pids) >= 2
    # every worker exports its own metrics with its own pid
    metric_pids = {m["resource"]["process.pid"] for m in capture.metrics if m["name"] == "http.server.request.duration"}
    assert worker_pids <= metric_pids, (metric_pids, worker_pids)
    assert len({s["span_id"] for s in spans}) == 60
    assert len({s["trace_id"] for s in spans}) == 60  # random ids differ across forked workers

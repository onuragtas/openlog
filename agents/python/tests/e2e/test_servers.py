"""uWSGI pre-fork workers and gevent/eventlet monkey-patched servers."""

import importlib.util
import signal
import sys
from concurrent.futures import ThreadPoolExecutor

import pytest
from otlp import CLIENT, SERVER

from apps import BIN, App, agent_env, free_port

linux_only = pytest.mark.skipif(not sys.platform.startswith("linux"), reason="pre-fork server tests run on Linux")


def _pid(body):
    return int(body.split('"pid":')[1].split("}")[0].split(",")[0].strip())


def _load(app, path, n, workers):
    def request(i):
        # concurrent requests that keep a worker busy for 50 ms spread over the pre-fork workers
        status, body = app.get(path.format(i) + "?sleep=0.05", headers={"Connection": "close"})
        assert status == 200, body
        return _pid(body)

    with ThreadPoolExecutor(max_workers=workers) as pool:
        return set(pool.map(request, range(n)))


def _assert_chains_linked(cap, chain_name, nested_name, count):
    chains = [s for s in cap.spans if s["name"] == chain_name and s["kind"] == SERVER]
    assert len(chains) == count, [(s["name"], s["kind"]) for s in cap.spans]
    for chain in chains:
        client = [s for s in cap.spans if s["kind"] == CLIENT and s["parent_span_id"] == chain["span_id"]]
        assert len(client) == 1, chain
        nested = [s for s in cap.spans if s["kind"] == SERVER and s["parent_span_id"] == client[0]["span_id"]]
        assert len(nested) == 1 and nested[0]["name"] == nested_name
        assert chain["trace_id"] == client[0]["trace_id"] == nested[0]["trace_id"]


# --- uWSGI -----------------------------------------------------------------------------------------------------------

UWSGI_MODES = {
    # openlog-instrument; the application is imported in the master before fork
    "prefork": (["--wsgi-file", "flask_app.py", "--callable", "app"], True),
    # the application is imported in each worker after fork
    "lazy-apps": (["--lazy-apps", "--wsgi-file", "flask_app.py", "--callable", "app"], True),
    # worker threads
    "threads": (["--threads", "4", "--wsgi-file", "flask_app.py", "--callable", "app"], True),
    "lazy-apps-threads": (["--lazy-apps", "--threads", "4", "--wsgi-file", "flask_app.py", "--callable", "app"], True),
    # the application imports uwsgidecorators, which replaces uwsgi.post_fork_hook with its @postfork chain
    "uwsgidecorators": (["--wsgi-file", "uwsgi_decorators_app.py", "--callable", "app"], True),
    # openlog_agent.start() in the application module (imported in the master)
    "code-start": (["--wsgi-file", "code_start_app.py", "--callable", "app"], False),
}


@linux_only
@pytest.mark.skipif(not (BIN / "uwsgi").exists(), reason="uwsgi not installed")
@pytest.mark.parametrize("mode", list(UWSGI_MODES))
def test_uwsgi_workers(capture, apps, mode):
    port = free_port()
    args, instrument = UWSGI_MODES[mode]
    argv = [
        str(BIN / "uwsgi"),
        "--http-socket",
        f"127.0.0.1:{port}",
        "--master",
        "--processes",
        "3",
        "--die-on-term",
        "--disable-logging",
        *args,
    ]
    app = apps(App(argv, agent_env(capture.url, "uwsgi-shop", APP_SERVICE="uwsgi-shop"), port, instrument=instrument))
    app.wait_ready()
    if mode == "code-start":
        path, name = "/items/{}", "GET /items/<name>"
    else:
        path, name = "/users/{}", "GET /users/<int:user_id>"
    worker_pids = _load(app, path, 60, 8)
    if mode != "code-start":
        with ThreadPoolExecutor(max_workers=2) as pool:  # a chain holds one worker while it calls another
            assert set(pool.map(lambda _: app.get("/chain", headers={"Connection": "close"})[0], range(10))) == {200}
    master = app.proc.pid
    # no wait: the spans are still buffered in the workers and must be flushed when they exit
    rc = app.stop(signal.SIGINT, timeout=40)
    assert rc == 0, app.output()
    spans = [s for s in capture.spans if s["name"] == name and s["kind"] == SERVER and "?" not in s["name"]]
    nested = 10 if mode != "code-start" else 0
    assert len(spans) == 60 + nested, (len(spans), app.output())
    span_pids = {s["resource"]["process.pid"] for s in spans}
    assert master not in span_pids
    assert worker_pids <= span_pids, (span_pids, worker_pids)
    assert len(worker_pids) >= 2
    assert len({s["span_id"] for s in spans}) == len(spans)
    assert len({s["trace_id"] for s in spans if not s["parent_span_id"]}) == 60
    metric_pids = {m["resource"]["process.pid"] for m in capture.metrics if m["name"] == "http.server.request.duration"}
    assert worker_pids <= metric_pids, (metric_pids, worker_pids)
    if mode != "code-start":
        _assert_chains_linked(capture, "GET /chain", name, 10)
    out = app.output()
    assert "starts in each worker after fork" in out, out
    assert out.count("msg=started") >= 3, out  # one agent per worker, none in the master
    assert "Fatal Python error" not in out and "Traceback" not in out, out


# --- gevent / eventlet -----------------------------------------------------------------------------------------------


def _installed(name):
    return importlib.util.find_spec(name) is not None


@pytest.mark.parametrize("green", ["gevent", "eventlet"])
@pytest.mark.parametrize("start", ["instrument", "code"])
def test_monkey_patched_server(capture, apps, green, start):
    if not _installed(green):
        pytest.skip(f"{green} not installed")
    port = free_port()
    env = agent_env(capture.url, f"{green}-shop", GREEN=green, START=start)
    app = apps(App(["python", "greenlet_app.py"], env, port, instrument=start == "instrument")).wait_ready()
    with ThreadPoolExecutor(max_workers=8) as pool:
        statuses = pool.map(lambda i: app.get("/fanout/10" + ("?copy=1" if i % 2 else ""))[0], range(8))
        assert set(statuses) == {200}
        assert set(pool.map(lambda _: app.get("/chain")[0], range(8))) == {200}
    # SIGTERM right away: the flush runs in a greenlet (blocking is not allowed in the event loop's signal callback)
    rc = app.stop(signal.SIGTERM, timeout=30)
    assert rc == -signal.SIGTERM, app.output()
    out = app.output()
    assert "BlockingSwitchOutError" not in out and "do not call blocking functions" not in out, out

    fanouts = {s["span_id"]: s for s in capture.spans if s["name"] == "GET /fanout/<int:n>"}
    works = [s for s in capture.spans if s["name"].startswith("work ")]
    assert len(fanouts) == 8 and len(works) == 80, (len(fanouts), len(works), out)
    linked = 0
    for work in works:
        # inside a greenlet the context follows the greenlet: work span -> requests CLIENT span -> nested SERVER span
        client = [s for s in capture.spans if s["kind"] == CLIENT and s["parent_span_id"] == work["span_id"]]
        assert len(client) == 1, work
        nested = [s for s in capture.spans if s["kind"] == SERVER and s["parent_span_id"] == client[0]["span_id"]]
        assert len(nested) == 1 and nested[0]["name"] == "GET /users/<int:user_id>"
        assert work["trace_id"] == client[0]["trace_id"] == nested[0]["trace_id"]
        if work["parent_span_id"]:
            # only greenlets that run in a copy of the request's context have the request span as parent
            parent = fanouts[work["parent_span_id"]]
            assert parent["trace_id"] == work["trace_id"]
            linked += 1
        else:
            assert work["trace_id"] not in {f["trace_id"] for f in fanouts.values()}
    assert linked == 40  # the ?copy=1 half; the other half starts new traces instead of joining another request
    _assert_chains_linked(capture, "GET /chain", "GET /users/<int:user_id>", 8)


@linux_only
@pytest.mark.skipif(not _installed("gevent"), reason="gevent not installed")
def test_gunicorn_gevent_workers(capture, apps):
    port = free_port()
    argv = [
        "gunicorn",
        "-k",
        "gevent",
        "--worker-connections",
        "100",
        "-w",
        "2",
        "-b",
        f"127.0.0.1:{port}",
        "--graceful-timeout",
        "10",
        "flask_app:app",
    ]
    app = apps(App(argv, agent_env(capture.url, "gevent-workers"), port)).wait_ready()
    worker_pids = _load(app, "/users/{}", 60, 16)
    with ThreadPoolExecutor(max_workers=8) as pool:
        assert set(pool.map(lambda _: app.get("/chain", headers={"Connection": "close"})[0], range(20))) == {200}
    rc = app.stop(signal.SIGTERM, timeout=40)
    assert rc == 0, app.output()
    name = "GET /users/<int:user_id>"
    spans = [s for s in capture.spans if s["name"] == name and s["kind"] == SERVER]
    assert len(spans) == 80, (len(spans), app.output())
    assert {s["resource"]["process.pid"] for s in spans} == worker_pids
    _assert_chains_linked(capture, "GET /chain", name, 20)

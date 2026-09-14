"""uWSGI deferral and gevent/eventlet helpers of the agent lifecycle (fake uwsgi module, no server)."""

import builtins
import importlib
import sys
import types

import pytest
from opentelemetry.sdk.trace import ReadableSpan
from opentelemetry.sdk.util.instrumentation import InstrumentationScope
from opentelemetry.trace import SpanContext, SpanKind

from openlog_agent import _agent
from openlog_agent.exporters import transform_span


@pytest.fixture
def fake_uwsgi(monkeypatch):
    module = types.ModuleType("uwsgi")
    monkeypatch.setattr(sys, "builtin_module_names", (*sys.builtin_module_names, "uwsgi"))
    monkeypatch.setitem(sys.modules, "uwsgi", module)
    monkeypatch.setattr(_agent, "_current", None)
    monkeypatch.setattr(_agent, "_globals_installed", True)  # never replace the test process' global providers
    finders = list(sys.meta_path)
    yield module
    sys.meta_path[:] = finders
    if _agent._current is not None:
        _agent._current.shutdown()


def test_uwsgi_detection(fake_uwsgi):
    assert _agent._uwsgi_module() is fake_uwsgi
    assert _agent._uwsgi_before_fork(fake_uwsgi)  # module not initialized yet: interpreter start-up in the master
    fake_uwsgi.worker_id = lambda: 0
    assert _agent._uwsgi_before_fork(fake_uwsgi)  # master imports the application
    fake_uwsgi.worker_id = lambda: 2
    assert not _agent._uwsgi_before_fork(fake_uwsgi)  # lazy-apps: the application is imported in a worker


def test_not_uwsgi():
    assert "uwsgi" not in sys.builtin_module_names
    assert _agent._uwsgi_module() is None


def test_uwsgi_master_defers_to_post_fork_hook(fake_uwsgi, monkeypatch):
    calls = []
    fake_uwsgi.post_fork_hook = lambda: calls.append("previous")
    env = {"OPENLOG_ENDPOINT": "http://127.0.0.1:9", "OPENLOG_SHUTDOWN_ON_SIGNAL": "false", "OPENLOG_LOG_LEVEL": "off"}
    pending = _agent._start({}, instrument=False, automatic=True, env=env)
    assert pending.pending and not pending.enabled
    assert _agent.get_agent() is pending
    fake_uwsgi.worker_id = lambda: 1
    fake_uwsgi.post_fork_hook()  # uWSGI calls it in each worker after fork
    agent = _agent.get_agent()
    assert agent is not pending and agent.enabled and not agent.pending
    assert calls == ["previous"]  # the application's own hook still runs
    fake_uwsgi.post_fork_hook()  # idempotent
    assert _agent.get_agent() is agent


def test_uwsgidecorators_chain_gets_the_worker_start(fake_uwsgi, tmp_path, monkeypatch):
    (tmp_path / "uwsgidecorators.py").write_text(
        "import uwsgi\npostfork_chain = []\n"
        "def postfork_chain_hook():\n    for f in postfork_chain:\n        f()\n"
        "uwsgi.post_fork_hook = postfork_chain_hook\n"
    )
    monkeypatch.syspath_prepend(str(tmp_path))
    monkeypatch.delitem(sys.modules, "uwsgidecorators", raising=False)
    env = {"OPENLOG_ENDPOINT": "http://127.0.0.1:9", "OPENLOG_SHUTDOWN_ON_SIGNAL": "false", "OPENLOG_LOG_LEVEL": "off"}
    pending = _agent._start({}, instrument=False, automatic=True, env=env)
    decorators = importlib.import_module("uwsgidecorators")  # replaces the agent's post_fork_hook
    assert len(decorators.postfork_chain) == 1
    fake_uwsgi.worker_id = lambda: 1
    fake_uwsgi.post_fork_hook()
    assert _agent.get_agent() is not pending and _agent.get_agent().enabled
    sys.modules.pop("uwsgidecorators", None)


def test_green_spawn_without_monkey_patch():
    assert _agent._green_spawn() is None


def _server_span(name, attrs, scope, version="0.65b0"):
    return ReadableSpan(
        name=name,
        context=SpanContext(1, 2, is_remote=False),
        attributes=attrs,
        kind=SpanKind.SERVER,
        instrumentation_scope=InstrumentationScope(scope, version),
    )


def test_tornado_route_from_span_name():
    s = transform_span(
        _server_span("GET /users/{user_id}", {"http.request.method": "GET"}, "opentelemetry.instrumentation.tornado"),
        "sanitized",
    )
    assert s.attributes["http.route"] == "/users/{user_id}" and s.name == "GET /users/{user_id}"
    # 404: the span is named by the method only
    s = transform_span(
        _server_span("GET", {"http.request.method": "GET"}, "opentelemetry.instrumentation.tornado"), "raw"
    )
    assert "http.route" not in s.attributes
    # contrib < 0.63 (the Python 3.9 line) names the span with the request path: not a route
    s = transform_span(
        _server_span(
            "GET /users/42", {"http.request.method": "GET"}, "opentelemetry.instrumentation.tornado", "0.62b1"
        ),
        "raw",
    )
    assert "http.route" not in s.attributes and s.name == "GET /users/42"


def test_pyramid_name_gets_the_method():
    attrs = {"http.request.method": "POST", "http.route": "/orders/{id}"}
    s = transform_span(_server_span("/orders/{id}", attrs, "opentelemetry.instrumentation.pyramid.callbacks"), "raw")
    assert s.name == "POST /orders/{id}"
    # other scopes keep their names
    s = transform_span(_server_span("/orders/{id}", dict(attrs), "opentelemetry.instrumentation.falcon"), "raw")
    assert s.name == "/orders/{id}"


@pytest.mark.parametrize("worker_at_first_import", [0, 1])
def test_uwsgi_module_appears_after_start(monkeypatch, worker_at_first_import):
    # Python < 3.12: no built-in module at sitecustomize time, only the uwsgi executable; the module shows up later
    monkeypatch.setattr(sys, "executable", "/usr/local/bin/uwsgi")
    monkeypatch.delitem(sys.modules, "uwsgi", raising=False)
    monkeypatch.setattr(_agent, "_current", None)
    monkeypatch.setattr(_agent, "_globals_installed", True)
    finders = list(sys.meta_path)
    original_import = builtins.__import__
    try:
        env = {
            "OPENLOG_ENDPOINT": "http://127.0.0.1:9",
            "OPENLOG_SHUTDOWN_ON_SIGNAL": "false",
            "OPENLOG_LOG_LEVEL": "off",
        }
        pending = _agent._start({}, instrument=False, automatic=True, env=env)
        assert pending.pending
        assert builtins.__import__ is not original_import  # watches import statements until uwsgi is initialized
        module = types.ModuleType("uwsgi")
        module.worker_id = lambda: worker_at_first_import
        monkeypatch.setitem(sys.modules, "uwsgi", module)
        __import__("json")  # any import statement, also of an already loaded module
        assert builtins.__import__ is original_import  # unhooked after the first one
        if worker_at_first_import == 0:
            assert _agent.get_agent() is pending  # master: waits for the fork
            module.worker_id = lambda: 1
            module.post_fork_hook()
        agent = _agent.get_agent()
        assert agent is not pending and agent.enabled
    finally:
        builtins.__import__ = original_import
        sys.meta_path[:] = finders
        if _agent._current is not None:
            _agent._current.shutdown()

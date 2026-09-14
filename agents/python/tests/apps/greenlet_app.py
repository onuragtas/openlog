"""Flask app on a monkey-patched gevent or eventlet server (GREEN=gevent|eventlet).

START=instrument: run under openlog-instrument (the agent starts before the monkey patch, in sitecustomize).
START=code: monkey patch first, then openlog_agent.start() (the order gevent/eventlet recommend).
"""

import os

GREEN = os.environ.get("GREEN", "gevent")
if GREEN == "gevent":
    from gevent import monkey

    monkey.patch_all()
else:
    import eventlet

    eventlet.monkey_patch()

if os.environ.get("START") == "code":
    import openlog_agent

    openlog_agent.start()

import contextvars  # noqa: E402
import functools  # noqa: E402
import logging  # noqa: E402
import time  # noqa: E402

import requests  # noqa: E402
from flask import Flask, jsonify, request  # noqa: E402
from opentelemetry import trace  # noqa: E402

PORT = int(os.environ.get("PORT", "8000"))
log = logging.getLogger("shop")
tracer = trace.get_tracer("greenlet-app")
app = Flask(__name__)


def spawn_all(fns):
    if GREEN == "gevent":
        import gevent

        return [g.get() for g in [gevent.spawn(fn) for fn in fns]]
    pool = eventlet.GreenPool()
    return [t.wait() for t in [pool.spawn(fn) for fn in fns]]


@app.get("/ready")
def ready():
    return "ok"


@app.get("/users/<int:user_id>")
def user(user_id):
    time.sleep(float(request.args.get("sleep", "0")))  # patched: yields to other greenlets
    log.warning("loading user %s", user_id)
    return jsonify(id=user_id, pid=os.getpid(), green=GREEN)


@app.get("/fanout/<int:n>")
def fanout(n):
    # n greenlets, each with its own span that must be the parent of the HTTP client span made in that greenlet.
    # New greenlets start with an empty context (like threads); ?copy=1 runs each in a copy of the request's context.
    copy = request.args.get("copy") == "1"

    def work(i):
        def run():
            with tracer.start_as_current_span(f"work {i}") as span:
                time.sleep(0.01 * (n - i))  # interleave the greenlets
                r = requests.get(f"http://127.0.0.1:{PORT}/users/{i}", timeout=10)
                assert trace.get_current_span() is span
                return {"i": i, "span_id": format(span.get_span_context().span_id, "016x"), "status": r.status_code}

        return run

    fns = [work(i) for i in range(n)]
    if copy:
        # one copy per greenlet: a Context cannot be entered by two greenlets at once
        fns = [functools.partial(contextvars.copy_context().run, fn) for fn in fns]
    return jsonify(spawn_all(fns))


@app.get("/chain")
def chain():
    r = requests.get(f"http://127.0.0.1:{PORT}/users/7", timeout=10)
    return jsonify(upstream=r.json())


if __name__ == "__main__":
    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(message)s trace_id=%(trace_id)s")
    if GREEN == "gevent":
        from gevent.pywsgi import WSGIServer

        WSGIServer(("127.0.0.1", PORT), app, log=None).serve_forever()
    else:
        import eventlet.wsgi

        eventlet.wsgi.server(eventlet.listen(("127.0.0.1", PORT)), app, log_output=False)

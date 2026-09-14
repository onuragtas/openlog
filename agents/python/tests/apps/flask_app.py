"""Flask sample app (also served by gunicorn in the fork-safety test: gunicorn flask_app:app)."""

import logging
import os
import time

import requests
from flask import Flask, jsonify, request

PORT = int(os.environ.get("PORT", "8000"))
log = logging.getLogger("shop")

app = Flask(__name__)


@app.get("/ready")
def ready():
    return "ok"


@app.get("/healthz")
def healthz():
    return "ok"


@app.get("/users/<int:user_id>")
def user(user_id):
    # ?sleep=<seconds> keeps a worker busy, so concurrent requests spread over pre-fork workers
    time.sleep(float(request.args.get("sleep", "0")))
    log.warning("loading user %s", user_id)
    return jsonify(id=user_id, pid=os.getpid())


@app.get("/bench/<int:n>")
def bench(n):
    # overhead benchmark route: no logging, so only request instrumentation is measured
    return jsonify(n=n, pid=os.getpid())


@app.get("/boom")
def boom():
    raise ValueError("flask boom 42")


@app.get("/chain")
def chain():
    r = requests.get(f"http://127.0.0.1:{PORT}/users/7", timeout=5)
    return jsonify(upstream=r.json())


if __name__ == "__main__":
    logging.basicConfig(
        level=logging.INFO, format="%(levelname)s %(message)s trace_id=%(trace_id)s span_id=%(span_id)s"
    )
    logging.getLogger("werkzeug").setLevel(logging.ERROR)
    app.run(host="127.0.0.1", port=PORT, threaded=True)

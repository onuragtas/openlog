"""Falcon sample app (WSGI, served by werkzeug's threaded development server)."""

import logging
import os

import falcon
import requests

PORT = int(os.environ.get("PORT", "8000"))
log = logging.getLogger("shop")


class Ready:
    def on_get(self, req, resp):
        resp.text = "ok"


class User:
    def on_get(self, req, resp, user_id):
        log.warning("loading user %s", user_id)
        resp.media = {"id": user_id, "pid": os.getpid()}


class Boom:
    def on_get(self, req, resp):
        raise ValueError("falcon boom 42")


class Chain:
    def on_get(self, req, resp):
        resp.media = {"upstream": requests.get(f"http://127.0.0.1:{PORT}/users/7", timeout=5).json()}


app = falcon.App()
app.add_route("/ready", Ready())
app.add_route("/users/{user_id:int}", User())
app.add_route("/boom", Boom())
app.add_route("/chain", Chain())


if __name__ == "__main__":
    from werkzeug.serving import run_simple

    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(message)s trace_id=%(trace_id)s")
    logging.getLogger("werkzeug").setLevel(logging.ERROR)
    run_simple("127.0.0.1", PORT, app, threaded=True)

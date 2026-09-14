"""Pyramid sample app (WSGI, served by werkzeug's threaded development server)."""

import logging
import os

import requests
from pyramid.config import Configurator
from pyramid.response import Response

PORT = int(os.environ.get("PORT", "8000"))
log = logging.getLogger("shop")


def ready(request):
    return Response("ok")


def user(request):
    user_id = int(request.matchdict["user_id"])
    log.warning("loading user %s", user_id)
    return {"id": user_id, "pid": os.getpid()}


def boom(request):
    raise ValueError("pyramid boom 42")


def chain(request):
    return {"upstream": requests.get(f"http://127.0.0.1:{PORT}/users/7", timeout=5).json()}


def make_app():
    with Configurator() as config:
        config.add_route("ready", "/ready")
        config.add_route("user", "/users/{user_id}")
        config.add_route("boom", "/boom")
        config.add_route("chain", "/chain")
        config.add_view(ready, route_name="ready")
        config.add_view(user, route_name="user", renderer="json")
        config.add_view(boom, route_name="boom")
        config.add_view(chain, route_name="chain", renderer="json")
        return config.make_wsgi_app()


app = make_app()


if __name__ == "__main__":
    from werkzeug.serving import run_simple

    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(message)s trace_id=%(trace_id)s")
    logging.getLogger("werkzeug").setLevel(logging.ERROR)
    run_simple("127.0.0.1", PORT, app, threaded=True)

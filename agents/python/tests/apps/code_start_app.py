"""Starts the agent from code (no openlog-instrument), then creates a Flask app.

Served directly (python code_start_app.py) or by gunicorn --preload (agent started in the master before fork).
"""

import logging
import os
import time

import openlog_agent

agent = openlog_agent.start(service_name=os.environ.get("APP_SERVICE", "code-start"))

from flask import Flask, jsonify, request  # noqa: E402

PORT = int(os.environ.get("PORT", "8000"))
app = Flask(__name__)


@app.get("/ready")
def ready():
    return "ok"


@app.get("/items/<name>")
def item(name):
    time.sleep(float(request.args.get("sleep", "0")))
    logging.getLogger("shop").warning("item %s", name)
    return jsonify(name=name, pid=os.getpid())


if __name__ == "__main__":
    logging.getLogger("werkzeug").setLevel(logging.ERROR)
    app.run(host="127.0.0.1", port=PORT, threaded=True)

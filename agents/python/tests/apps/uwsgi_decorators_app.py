"""uWSGI application that uses uwsgidecorators: importing it replaces uwsgi.post_fork_hook with the @postfork chain."""

import os

from flask_app import app  # noqa: F401
from uwsgidecorators import postfork


@postfork
def worker_started():
    print(f"postfork worker pid={os.getpid()}", flush=True)

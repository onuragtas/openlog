"""Celery sample: openlog-instrument celery -A celery_app worker --pool prefork -c 2"""

import logging
import os

from celery import Celery

REDIS_URL = os.environ["REDIS_URL"]
app = Celery("celery_app", broker=REDIS_URL, backend=REDIS_URL)
app.conf.update(worker_hijack_root_logger=False, broker_connection_retry_on_startup=True, task_track_started=False)


@app.task
def add(x, y):
    logging.getLogger("tasks").warning("adding %s + %s", x, y)
    return {"sum": x + y, "pid": os.getpid()}

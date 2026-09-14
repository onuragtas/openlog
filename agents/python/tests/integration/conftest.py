import os
import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "helpers"))

from otlp import Capture  # noqa: E402

DB_ENV = {
    "PG_URL": os.environ.get("PG_URL", "postgresql://openlog:openlog@127.0.0.1:55433/openlog"),
    "MYSQL_HOST": os.environ.get("MYSQL_HOST", "127.0.0.1"),
    "MYSQL_PORT": os.environ.get("MYSQL_PORT", "53307"),
    "REDIS_URL": os.environ.get("REDIS_URL", "redis://127.0.0.1:56380/0"),
    "RABBITMQ_URL": os.environ.get("RABBITMQ_URL", "amqp://openlog:openlog@127.0.0.1:55673/"),
    "KAFKA_BOOTSTRAP": os.environ.get("KAFKA_BOOTSTRAP", "127.0.0.1:59092"),
}


@pytest.fixture
def capture():
    cap = Capture()
    yield cap
    cap.close()


@pytest.fixture
def db_env():
    return dict(DB_ENV)

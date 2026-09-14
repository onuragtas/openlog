import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "helpers"))

from otlp import Capture  # noqa: E402


@pytest.fixture
def capture():
    cap = Capture()
    yield cap
    cap.close()


@pytest.fixture
def apps():
    started = []

    def track(app):
        started.append(app)
        return app

    yield track
    for app in started:
        app.close()

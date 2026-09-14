"""``openlog-instrument``: runs a Python program with the openlog agent, without code changes.

    openlog-instrument python app.py
    openlog-instrument gunicorn -w 4 app:app
    openlog-instrument uvicorn main:app --workers 4
    openlog-instrument celery -A proj worker

A thin wrapper of ``opentelemetry-instrument`` that selects the openlog distro and configurator (so another installed
distribution, e.g. opentelemetry-distro, is not used). Configuration comes from OPENLOG_* / OTEL_* variables.
"""

from __future__ import annotations

import os
import sys


def main() -> None:
    if len(sys.argv) < 2 or sys.argv[1] in ("-h", "--help"):
        sys.stdout.write(__doc__.strip() + "\n\n")
        if len(sys.argv) < 2:
            sys.exit(2)
    if len(sys.argv) >= 2 and sys.argv[1] == "--version":
        from .version import __version__  # pylint: disable=import-outside-toplevel

        sys.stdout.write(f"openlog-instrument {__version__}\n")
        return
    os.environ["OTEL_PYTHON_DISTRO"] = "openlog"
    os.environ["OTEL_PYTHON_CONFIGURATOR"] = "openlog"
    # pylint: disable=import-outside-toplevel
    from opentelemetry.instrumentation.auto_instrumentation import run

    run()


if __name__ == "__main__":
    main()

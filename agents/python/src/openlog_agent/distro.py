"""Entry points for ``opentelemetry-instrument`` / ``openlog-instrument`` (zero-code start).

The auto-instrumentation loader (sitecustomize) calls, in order: the distro's ``configure()``, the configurator's
``configure()`` and the distro's ``load_instrumentor()`` for every installed instrumentation. Both configure hooks
start the agent from the environment (idempotent, never raising into the application); ``load_instrumentor`` applies
OPENLOG_INSTRUMENTATIONS_DISABLED and the agent's instrumentation defaults.
"""

from __future__ import annotations

from typing import Any

from opentelemetry.instrumentation.distro import BaseDistro
from opentelemetry.sdk._configuration import _BaseConfigurator

from . import _agent


class OpenlogDistro(BaseDistro):
    """opentelemetry_distro entry point ``openlog``."""

    def _configure(self, **kwargs: Any) -> None:
        _agent.start_from_environment()

    def load_instrumentor(self, entry_point: Any, **kwargs: Any) -> None:
        _agent.load_instrumentor(entry_point, **kwargs)


class OpenlogConfigurator(_BaseConfigurator):
    """opentelemetry_configurator entry point ``openlog``."""

    def _configure(self, **kwargs: Any) -> None:
        _agent.start_from_environment()

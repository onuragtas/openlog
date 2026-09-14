"""Agent lifecycle: providers, exporters, instrumentation, fork and exit handling."""

from __future__ import annotations

import atexit
import os
import signal
import threading
import time
from typing import Any, Callable, Dict, List, Mapping, Optional

from opentelemetry import metrics, trace
from opentelemetry._logs import set_logger_provider
from opentelemetry.sdk._logs import LoggerProvider
from opentelemetry.sdk.metrics import MeterProvider
from opentelemetry.sdk.metrics.export import PeriodicExportingMetricReader
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace.export import BatchSpanProcessor

from .config import Config, ConfigError, load_config
from .diag import Diag, Write, install_sdk_log_handler
from .exporters import OpenlogSpanExporter, create_exporters
from .instrumentations import apply_environment_defaults, disabled_set, instrumentor_kwargs
from .logs import install_record_factory
from .resource import build_resource, update_process_pid
from .runtime_metrics import RuntimeMetrics, start_runtime_metrics
from .sampler import OpenlogTracerProvider, create_sampler
from .version import __version__

try:  # the batch log processor moved between SDK versions
    from opentelemetry.sdk._logs.export import BatchLogRecordProcessor
except ImportError:  # pragma: no cover
    from opentelemetry.sdk._logs._internal.export import BatchLogRecordProcessor  # type: ignore


class AlreadyStartedError(RuntimeError):
    """start() was called while an agent started from code is running."""

    def __init__(self) -> None:
        super().__init__("openlog: already started; call shutdown() first")


class Agent:
    """A running (or disabled) agent."""

    def __init__(
        self,
        config: Config,
        diag: Diag,
        resource: Optional[Resource] = None,
        tracer_provider: Optional[OpenlogTracerProvider] = None,
        meter_provider: Optional[MeterProvider] = None,
        logger_provider: Optional[LoggerProvider] = None,
        automatic: bool = False,
    ) -> None:
        self.config = config
        self.resource = resource
        self.tracer_provider = tracer_provider
        self.meter_provider = meter_provider
        self.logger_provider = logger_provider
        #: started by openlog-instrument / opentelemetry-instrument
        self.automatic = automatic
        self.instrumentors: List[Any] = []
        self._diag = diag
        self._cleanups: List[Callable[[], None]] = []
        self._shutdown_lock = threading.Lock()
        self._shut_down = False

    @property
    def enabled(self) -> bool:
        return self.tracer_provider is not None

    def force_flush(self, timeout: Optional[float] = None) -> bool:
        """Exports buffered telemetry now (bounded by ``timeout`` seconds, default OPENLOG_SHUTDOWN_TIMEOUT)."""
        if not self.enabled:
            return True
        deadline = time.monotonic() + (self.config.shutdown_timeout if timeout is None else timeout)
        ok = True
        for p in (self.tracer_provider, self.meter_provider, self.logger_provider):
            remaining = max(deadline - time.monotonic(), 0.001)
            try:
                ok = bool(p.force_flush(int(remaining * 1000))) and ok  # type: ignore[union-attr]
            except Exception as exc:  # pylint: disable=broad-except
                self._diag.warn("flush failed", error=exc)
                ok = False
        return ok

    def shutdown(self) -> None:
        """Flushes buffered telemetry and stops the exporters (bounded by OPENLOG_SHUTDOWN_TIMEOUT). Idempotent."""
        global _current  # pylint: disable=global-statement
        with self._shutdown_lock:
            if self._shut_down:
                return
            self._shut_down = True
        if self.enabled:
            for fn in reversed(self._cleanups):
                try:
                    fn()
                except Exception:  # pylint: disable=broad-except
                    pass
            deadline = time.monotonic() + self.config.shutdown_timeout
            self.force_flush()
            threads = []
            for p in (self.tracer_provider, self.meter_provider, self.logger_provider):
                t = threading.Thread(target=_quiet(p.shutdown, self._diag), name="openlog-shutdown", daemon=True)  # type: ignore[union-attr]
                t.start()
                threads.append(t)
            for t in threads:
                t.join(max(deadline - time.monotonic(), 0.0))
            for inst in self.instrumentors:
                try:
                    inst.uninstrument()
                except Exception:  # pylint: disable=broad-except
                    pass
        with _lock:
            if _current is self:
                _current = None


def _quiet(fn: Callable[[], Any], diag: Diag) -> Callable[[], None]:
    def run() -> None:
        try:
            fn()
        except Exception as exc:  # pylint: disable=broad-except
            diag.warn("shutdown", error=exc)

    return run


_lock = threading.RLock()
_current: Optional[Agent] = None
_globals_installed = False


def get_agent() -> Optional[Agent]:
    """The running agent, if any."""
    return _current


def _install_globals(agent: Agent) -> None:
    global _globals_installed  # pylint: disable=global-statement
    if _globals_installed:
        agent._diag.warn(  # pylint: disable=protected-access
            "global OpenTelemetry providers were installed by a previous start(); instrumentations that use the "
            "global providers keep exporting through the first agent (start() once per process)"
        )
        return
    _globals_installed = True
    trace.set_tracer_provider(agent.tracer_provider)  # type: ignore[arg-type]
    metrics.set_meter_provider(agent.meter_provider)  # type: ignore[arg-type]
    set_logger_provider(agent.logger_provider)  # type: ignore[arg-type]


def _start(
    options: Mapping[str, Any],
    *,
    instrument: bool,
    automatic: bool = False,
    env: Optional[Mapping[str, str]] = None,
    diag_write: Optional[Write] = None,
) -> Agent:
    global _current  # pylint: disable=global-statement
    cfg, warnings = load_config(env, **options)
    diag = Diag(cfg.log_level, diag_write)
    for w in warnings:
        diag.warn(w)
    with _lock:
        if _current is not None:
            if _current.automatic and not automatic:
                diag.info("already started by openlog-instrument; start() options are ignored")
                return _current
            raise AlreadyStartedError()
        if not cfg.enabled:
            diag.info("disabled (OPENLOG_ENABLED=false)")
            agent = Agent(cfg, diag, automatic=automatic)
            _current = agent
            return agent

        restore_diag = install_sdk_log_handler(diag)
        apply_environment_defaults(cfg)
        resource, host_id_source = build_resource(cfg, env)
        if host_id_source == "generated":
            diag.info(
                "host.id generated by the Python agent; it links to an infra agent only if both read the same "
                "machine-id (see README: host linking)"
            )
        span_exporter, metric_exporter, log_exporter = create_exporters(cfg)

        tracer_provider = OpenlogTracerProvider(
            sampler=create_sampler(cfg.sampling_ratio, cfg.sampling_rv), resource=resource, shutdown_on_exit=False
        )
        tracer_provider.add_span_processor(
            BatchSpanProcessor(
                OpenlogSpanExporter(span_exporter, cfg.db_query_text),
                max_queue_size=4096,
                max_export_batch_size=512,
                schedule_delay_millis=5000,
                export_timeout_millis=int(max(30.0, cfg.export_timeout) * 1000),
            )
        )
        interval_ms = max(1, int(round(cfg.metric_interval * 1000)))
        meter_provider = MeterProvider(
            resource=resource,
            metric_readers=[
                PeriodicExportingMetricReader(
                    metric_exporter,
                    export_interval_millis=interval_ms,
                    export_timeout_millis=min(max(int(cfg.export_timeout * 1000), 1), interval_ms),
                )
            ],
            shutdown_on_exit=False,
        )
        logger_provider = LoggerProvider(resource=resource, shutdown_on_exit=False)
        logger_provider.add_log_record_processor(
            BatchLogRecordProcessor(
                log_exporter,
                max_queue_size=4096,
                max_export_batch_size=512,
                schedule_delay_millis=2000,
                export_timeout_millis=int(max(30.0, cfg.export_timeout) * 1000),
            )
        )
        agent = Agent(cfg, diag, resource, tracer_provider, meter_provider, logger_provider, automatic=automatic)
        agent._cleanups.append(restore_diag)  # pylint: disable=protected-access
        _install_globals(agent)

        if cfg.runtime_metrics:
            runtime: RuntimeMetrics = start_runtime_metrics(meter_provider)
            agent._cleanups.append(runtime.stop)  # pylint: disable=protected-access
        if cfg.logs_correlation:
            agent._cleanups.append(install_record_factory())  # pylint: disable=protected-access

        _register_process_hooks(agent)
        _current = agent

    if instrument:
        instrument_libraries(agent)
    diag.info(
        "started",
        **{
            "service.name": cfg.service_name,
            "endpoint": cfg.endpoint,
            "protocol": cfg.protocol,
            "sampling_ratio": cfg.sampling_ratio,
            "host.id.source": host_id_source,
            "automatic": automatic,
            "version": __version__,
        },
    )
    return agent


def _register_process_hooks(agent: Agent) -> None:
    atexit.register(agent.shutdown)
    resource = agent.resource

    if hasattr(os, "register_at_fork"):

        def after_in_child() -> None:
            # The SDK restarts its export threads in the child (os.register_at_fork in the batch processors and the
            # periodic metric reader), but the resource still carries the parent's process.pid. SDK >= 1.42 providers
            # replace their resource in their own fork handler (registered before this one, so this update wins);
            # older SDKs share the one resource object, which is updated in place.
            if _current is not agent:
                return
            pid = os.getpid()
            for provider in (agent.tracer_provider, agent.meter_provider, agent.logger_provider):
                update = getattr(provider, "_update_resource", None)
                if callable(update):
                    try:
                        update(Resource({"process.pid": pid}))
                    except Exception:  # pylint: disable=broad-except
                        pass
            if resource is not None:
                update_process_pid(resource, pid)
            current = getattr(agent.tracer_provider, "resource", None)
            if isinstance(current, Resource):
                agent.resource = current

        os.register_at_fork(after_in_child=after_in_child)

    if not agent.config.shutdown_on_signal or threading.current_thread() is not threading.main_thread():
        return
    try:
        if signal.getsignal(signal.SIGTERM) is not signal.SIG_DFL:
            return  # the application (or its server) handles SIGTERM and exits through atexit
    except (AttributeError, ValueError):  # pragma: no cover
        return

    def on_sigterm(signum: int, _frame: Any) -> None:
        # installed only while SIGTERM had the default action: flush, then terminate like the default action would
        try:
            agent.shutdown()
        finally:
            signal.signal(signum, signal.SIG_DFL)
            os.kill(os.getpid(), signum)

    try:
        signal.signal(signal.SIGTERM, on_sigterm)
    except (ValueError, OSError):  # pragma: no cover
        return

    def restore_signal() -> None:
        try:
            if signal.getsignal(signal.SIGTERM) is on_sigterm:
                signal.signal(signal.SIGTERM, signal.SIG_DFL)
        except (ValueError, OSError):
            pass

    agent._cleanups.append(restore_signal)  # pylint: disable=protected-access


def _providers(agent: Agent) -> Dict[str, Any]:
    return {
        "tracer_provider": agent.tracer_provider,
        "meter_provider": agent.meter_provider,
        "logger_provider": agent.logger_provider,
    }


def load_instrumentor(entry_point: Any, **kwargs: Any) -> None:
    """Instruments one opentelemetry_instrumentor entry point with the agent defaults (called by the loader)."""
    agent = _current
    if agent is None or not agent.enabled:
        return
    cfg = agent.config
    disabled, _ = disabled_set(cfg.disabled_instrumentations)
    name = entry_point.name
    if name in disabled:
        agent._diag.debug("instrumentation disabled", name=name)  # pylint: disable=protected-access
        return
    instrumentor = entry_point.load()()
    merged = instrumentor_kwargs(name, cfg, _providers(agent))
    merged.update(kwargs)
    instrumentor.instrument(**merged)
    if getattr(instrumentor, "_is_instrumented_by_opentelemetry", True):
        agent.instrumentors.append(instrumentor)


def instrument_libraries(agent: Agent) -> None:
    """Instruments every installed library like the auto-instrumentation loader (start() from code)."""
    # pylint: disable=import-outside-toplevel
    from opentelemetry.instrumentation.auto_instrumentation import _load

    from .distro import OpenlogDistro

    _, unknown = disabled_set(agent.config.disabled_instrumentations)
    for n in unknown:
        agent._diag.debug("instrumentation name not bundled; disabled if installed", name=n)  # pylint: disable=protected-access

    class _SafeDistro(OpenlogDistro):
        def load_instrumentor(self, entry_point: Any, **kwargs: Any) -> None:
            try:
                load_instrumentor(entry_point, **kwargs)
            except (ImportError, ConfigError):
                raise
            except Exception as exc:  # pylint: disable=broad-except
                agent._diag.warn("instrumentation failed", name=entry_point.name, error=exc)  # pylint: disable=protected-access

    distro = object.__new__(_SafeDistro)
    _load._load_instrumentors(distro)  # pylint: disable=protected-access


def start(**options: Any) -> Agent:
    """Configures OpenTelemetry for openlog and instruments the installed libraries.

    Installs the global TracerProvider, MeterProvider and LoggerProvider and the OpenTelemetry instrumentations of
    every supported library that is installed. Options override OPENLOG_* variables, which override OTEL_* ones:

        import openlog_agent
        openlog_agent.start(service_name="checkout", service_version="1.4.0", environment="production")

    Call it before the application imports and creates its framework objects (Flask/FastAPI app, Django settings
    loaded, SQLAlchemy engine). Only raises for invalid configuration (ConfigError) or when an agent started from code
    is already running; when openlog-instrument already started the agent, the running agent is returned.
    """
    return _start(options, instrument=True)


def shutdown() -> None:
    """Shuts the running agent down (no-op when none is running)."""
    agent = _current
    if agent is not None:
        agent.shutdown()


def start_from_environment(diag_write: Optional[Write] = None) -> Optional[Agent]:
    """Zero-code entry point (distro/configurator): starts once from the environment and never raises."""
    with _lock:
        if _current is not None:
            return _current
        try:
            return _start({}, instrument=False, automatic=True, diag_write=diag_write)
        except Exception as exc:  # pylint: disable=broad-except
            Diag("error", diag_write).error("not started", error=exc)
            return None


def post_fork(_server: Any = None, _worker: Any = None) -> Optional[Agent]:
    """gunicorn ``post_fork`` hook for applications that start the agent from code in each worker.

    # gunicorn.conf.py
    from openlog_agent import post_fork

    Starts the agent in the worker process (after fork). Use it without ``--preload``, so the application is
    imported after the agent started. Not needed with ``openlog-instrument gunicorn …``.
    """
    try:
        return start()
    except AlreadyStartedError:
        return _current

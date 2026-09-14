"""Agent lifecycle: providers, exporters, instrumentation, fork and exit handling."""

from __future__ import annotations

import atexit
import importlib.util
import os
import signal
import sys
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
        #: in the uWSGI master: libraries are instrumented, the providers start in each worker after fork
        self.pending = False
        self.instrumentors: List[Any] = []
        self._host_id_source = ""
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
    defer_uwsgi: bool = True,
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

        uwsgi = _uwsgi_module() if defer_uwsgi else None
        if defer_uwsgi and _in_uwsgi() and (uwsgi is None or _uwsgi_before_fork(uwsgi)):
            agent = _defer_to_uwsgi_workers(uwsgi, cfg, diag, options, automatic, env, diag_write)
        else:
            agent = _create_agent(cfg, diag, automatic, env)
        _current = agent

    if instrument:
        instrument_libraries(agent)
    if agent.pending:
        return agent
    diag.info(
        "started",
        **{
            "service.name": cfg.service_name,
            "endpoint": cfg.endpoint,
            "protocol": cfg.protocol,
            "sampling_ratio": cfg.sampling_ratio,
            "host.id.source": agent._host_id_source,  # pylint: disable=protected-access
            "automatic": automatic,
            "version": __version__,
        },
    )
    return agent


def _create_agent(cfg: Config, diag: Diag, automatic: bool, env: Optional[Mapping[str, str]]) -> Agent:
    """Providers, exporters, global providers, runtime metrics and process hooks (called with _lock held)."""
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
    agent._host_id_source = host_id_source  # pylint: disable=protected-access
    agent._cleanups.append(restore_diag)  # pylint: disable=protected-access
    _install_globals(agent)

    if cfg.runtime_metrics:
        runtime: RuntimeMetrics = start_runtime_metrics(meter_provider)
        agent._cleanups.append(runtime.stop)  # pylint: disable=protected-access
    if cfg.logs_correlation:
        agent._cleanups.append(install_record_factory())  # pylint: disable=protected-access

    _register_process_hooks(agent)
    return agent


def _in_uwsgi() -> bool:
    """True when this interpreter is embedded in uWSGI (the module may not be importable yet)."""
    return (
        "uwsgi" in sys.modules
        or "uwsgi" in sys.builtin_module_names
        or os.path.basename(sys.executable or "").startswith("uwsgi")
    )


def _uwsgi_module() -> Any:
    """The embedded ``uwsgi`` module when it can be imported now, else None.

    Python >= 3.12 registers it as a built-in module before sitecustomize runs (empty until uWSGI initializes it);
    on older versions it appears in sys.modules only after the interpreter started.
    """
    module = sys.modules.get("uwsgi")
    if module is not None:
        return module
    if "uwsgi" not in sys.builtin_module_names:
        return None
    try:
        import uwsgi  # type: ignore[import-not-found]  # pylint: disable=import-outside-toplevel,import-error

        return uwsgi
    except ImportError:  # pragma: no cover
        return None


def _uwsgi_before_fork(uwsgi: Any) -> bool:
    """True in the uWSGI master before the workers are forked.

    The embedded module is still empty while the interpreter starts (sitecustomize of openlog-instrument), and
    ``worker_id()`` is 0 while the master imports the application (no ``lazy-apps``). uWSGI forks in C without Python's
    fork hooks, so export threads started here would not run in the workers and locks they hold would stay locked.
    """
    worker_id = getattr(uwsgi, "worker_id", None)
    if worker_id is None:
        return True
    try:
        return int(worker_id()) == 0
    except Exception:  # pylint: disable=broad-except
        return False


def _defer_to_uwsgi_workers(
    uwsgi: Optional[Any],
    cfg: Config,
    diag: Diag,
    options: Mapping[str, Any],
    automatic: bool,
    env: Optional[Mapping[str, str]],
    diag_write: Optional[Write],
) -> Agent:
    """uWSGI master: instrument now (through the global proxy providers), start the providers in each worker."""
    apply_environment_defaults(cfg)
    pending = Agent(cfg, diag, automatic=automatic)
    pending.pending = True

    def start_in_worker() -> None:
        global _current  # pylint: disable=global-statement
        with _lock:
            if _current is not pending:
                return  # already started: post_fork_hook and the uwsgidecorators chain may both call this
            _current = None
        try:
            agent = _start(options, instrument=False, automatic=automatic, env=env, diag_write=diag_write)
            agent.instrumentors = pending.instrumentors
        except Exception as exc:  # pylint: disable=broad-except
            Diag("error", diag_write).error("not started in uWSGI worker", error=exc)

    def install(module: Any) -> None:
        if not _uwsgi_before_fork(module):
            start_in_worker()  # first seen in a worker (lazy-apps): already forked
            return
        previous = getattr(module, "post_fork_hook", None)

        def post_fork_hook() -> None:
            start_in_worker()
            if callable(previous):
                previous()

        module.post_fork_hook = post_fork_hook

    # uwsgidecorators replaces uwsgi.post_fork_hook with its @postfork chain when the application imports it
    sys.meta_path.insert(0, _UwsgiDecoratorsFinder(start_in_worker))
    if uwsgi is not None:
        install(uwsgi)
    else:
        # Python < 3.12: the module appears in sys.modules only after the interpreter started. The first import
        # statement after that (loading the application: in the master before fork, or in a lazy-apps worker) installs
        # the hook; builtins.__import__ also sees imports of modules that are already loaded.
        _watch_uwsgi_module(install)
    diag.info("uWSGI master: libraries instrumented, the agent starts in each worker after fork")
    return pending


def _watch_uwsgi_module(install: Callable[[Any], None]) -> None:
    """Calls ``install`` at the first import statement that runs once the uwsgi module is initialized, then unhooks."""
    import builtins  # pylint: disable=import-outside-toplevel

    original = builtins.__import__
    done = [False]

    def watching_import(name: str, *args: Any, **kwargs: Any) -> Any:
        if not done[0]:
            module = sys.modules.get("uwsgi")
            if module is not None and hasattr(module, "worker_id"):
                done[0] = True
                if builtins.__import__ is watching_import:
                    builtins.__import__ = original
                try:
                    install(module)
                except Exception:  # pylint: disable=broad-except
                    pass
        return original(name, *args, **kwargs)

    builtins.__import__ = watching_import


class _UwsgiDecoratorsFinder:
    """Meta path finder that puts the agent's worker start first in uwsgidecorators' post-fork chain."""

    def __init__(self, hook: Callable[[], None]) -> None:
        self._hook = hook
        self._busy = False

    def find_spec(self, name: str, path: Any = None, target: Any = None) -> Any:
        if name != "uwsgidecorators" or self._busy:
            return None
        self._busy = True
        try:
            spec = importlib.util.find_spec(name)
        finally:
            self._busy = False
        loader = getattr(spec, "loader", None)
        exec_module = getattr(loader, "exec_module", None)
        if exec_module is None:
            return spec
        hook = self._hook

        def exec_and_chain(module: Any) -> None:
            exec_module(module)
            chain = getattr(module, "postfork_chain", None)
            if isinstance(chain, list):
                chain.insert(0, hook)

        loader.exec_module = exec_and_chain  # type: ignore[union-attr]
        return spec


def _green_spawn() -> Optional[Callable[..., Any]]:
    """gevent.spawn / eventlet.spawn when the process is monkey patched, else None."""
    try:
        gevent_monkey = sys.modules.get("gevent.monkey")
        if gevent_monkey is not None and gevent_monkey.is_anything_patched():
            import gevent  # type: ignore[import-not-found]  # pylint: disable=import-outside-toplevel,import-error

            return gevent.spawn  # type: ignore[no-any-return]
        patcher = sys.modules.get("eventlet.patcher")
        if patcher is not None and patcher.is_monkey_patched("thread"):
            import eventlet  # type: ignore[import-not-found]  # pylint: disable=import-outside-toplevel,import-error

            return eventlet.spawn  # type: ignore[no-any-return]
    except Exception:  # pylint: disable=broad-except
        return None
    return None


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

    # signal.signal raises ValueError outside the main thread (caught below); threading.current_thread() is not
    # reliable here, eventlet's monkey patch returns a green thread object for the main thread.
    if not agent.config.shutdown_on_signal:
        return
    try:
        if signal.getsignal(signal.SIGTERM) is not signal.SIG_DFL:
            return  # the application (or its server) handles SIGTERM and exits through atexit
    except (AttributeError, ValueError):  # pragma: no cover
        return

    def terminate(signum: int) -> None:
        try:
            agent.shutdown()
        finally:
            signal.signal(signum, signal.SIG_DFL)
            os.kill(os.getpid(), signum)

    def on_sigterm(signum: int, _frame: Any) -> None:
        # installed only while SIGTERM had the default action: flush, then terminate like the default action would.
        # gevent/eventlet run signal handlers in the event loop, where the blocking flush is not allowed.
        spawn = _green_spawn()
        if spawn is not None:
            spawn(terminate, signum)
            return
        terminate(signum)

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
    if agent is None or not (agent.enabled or agent.pending):
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

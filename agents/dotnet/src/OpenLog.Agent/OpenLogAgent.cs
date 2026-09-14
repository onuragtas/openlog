using System;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Extensions.Logging;
using OpenTelemetry;
using OpenTelemetry.Logs;
using OpenTelemetry.Metrics;
using OpenTelemetry.Trace;

namespace OpenLog.Agent;

/// <summary>
/// A running openlog agent for applications without a generic host (console apps, workers, .NET Framework).
/// ASP.NET Core and generic-host applications use <c>builder.Services.AddOpenLog()</c> instead.
/// <code>
/// using var agent = OpenLogAgent.Start(o =&gt; o.ServiceName = "checkout");
/// var logger = agent.LoggerFactory.CreateLogger("app");
/// </code>
/// </summary>
public sealed class OpenLogAgent : IDisposable
{
    private static readonly object Gate = new();
    private static OpenLogAgent? current;

    private readonly OpenTelemetrySdk? sdk;
    private readonly IDisposable? globals;
    private readonly Diag diag;
    private int shutdown;

    private OpenLogAgent(OpenLogConfig config, Diag diag, OpenTelemetrySdk? sdk, IDisposable? globals)
    {
        Config = config;
        this.diag = diag;
        this.sdk = sdk;
        this.globals = globals;
    }

    /// <summary>The resolved configuration.</summary>
    public OpenLogConfig Config { get; }

    /// <summary>The tracer provider (a no-op provider when the agent is disabled).</summary>
    public TracerProvider TracerProvider => sdk?.TracerProvider ?? NoopProviders.Tracer;

    /// <summary>The meter provider (a no-op provider when the agent is disabled).</summary>
    public MeterProvider MeterProvider => sdk?.MeterProvider ?? NoopProviders.Meter;

    /// <summary>The logger provider (a no-op provider when the agent is disabled).</summary>
    public LoggerProvider LoggerProvider => sdk?.LoggerProvider ?? NoopProviders.Logger;

    /// <summary>ILoggerFactory whose loggers export OTLP logs correlated with the active span.</summary>
    public ILoggerFactory LoggerFactory => sdk?.GetLoggerFactory() ?? Microsoft.Extensions.Logging.Abstractions.NullLoggerFactory.Instance;

    /// <summary>The running agent, if any.</summary>
    public static OpenLogAgent? Current
    {
        get
        {
            lock (Gate) return current;
        }
    }

    /// <summary>Starts the agent from the environment and the options set by <paramref name="configure"/>.</summary>
    public static OpenLogAgent Start(Action<OpenLogOptions>? configure = null)
    {
        var o = new OpenLogOptions();
        configure?.Invoke(o);
        return Start(o);
    }

    /// <summary>
    /// Configures OpenTelemetry for openlog: tracer, meter and logger providers exporting OTLP, the openlog sampler,
    /// resource detection, W3C tracecontext (with the random flag) + baggage propagators and the instrumentations.
    /// Throws <see cref="OpenLogConfigException"/> for invalid configuration and <see cref="InvalidOperationException"/>
    /// when an agent is already running. Export problems are logged (rate limited) and never thrown into the application.
    /// </summary>
    public static OpenLogAgent Start(OpenLogOptions options) => Start(options, null, null);

    internal static OpenLogAgent Start(OpenLogOptions options, IDictionary<string, string?>? env, Action<string>? diagWrite)
    {
        var cfg = ConfigLoader.Load(env ?? ConfigLoader.ProcessEnvironment(), options, out var warnings);
        var diag = new Diag(cfg.LogLevel, diagWrite);
        foreach (var w in warnings) diag.Warn(w);
        if (!cfg.Enabled)
        {
            diag.Info("disabled (OPENLOG_ENABLED=false)");
            return new OpenLogAgent(cfg, diag, null, null);
        }
        lock (Gate)
        {
            if (current != null) throw new InvalidOperationException("openlog: already started; dispose the running agent first");
            var globals = OpenLogSetup.InstallGlobals(cfg, diag);
            OpenTelemetrySdk sdk;
            try
            {
                sdk = OpenTelemetrySdk.Create(builder =>
                {
                    builder.ConfigureResource(rb => OpenLogSetup.ConfigureResource(rb, cfg, diag));
                    builder.WithTracing(b => OpenLogSetup.ConfigureTracing(b, cfg, diag, addExporter: true));
                    builder.WithMetrics(b => OpenLogSetup.ConfigureMetrics(b, cfg, addExporter: true));
                    builder.WithLogging(b => OpenLogSetup.ConfigureLogging(b, cfg, addExporter: true), OpenLogSetup.ConfigureLoggerOptions);
                });
            }
            catch
            {
                globals.Dispose();
                throw;
            }
            var agent = new OpenLogAgent(cfg, diag, sdk, globals);
            if (cfg.ShutdownOnExit) AppDomain.CurrentDomain.ProcessExit += agent.OnProcessExit;
            current = agent;
            OpenLogSetup.LogStarted(cfg, diag, "start");
            return agent;
        }
    }

    private void OnProcessExit(object? sender, EventArgs e) => Dispose();

    /// <summary>Exports buffered telemetry now (bounded by <paramref name="timeoutMilliseconds"/>).</summary>
    public bool ForceFlush(int timeoutMilliseconds = 5000)
    {
        if (sdk == null) return true;
        var ok = sdk.TracerProvider.ForceFlush(timeoutMilliseconds);
        ok &= sdk.MeterProvider.ForceFlush(timeoutMilliseconds);
        ok &= sdk.LoggerProvider.ForceFlush(timeoutMilliseconds);
        return ok;
    }

    /// <summary>Flushes buffered telemetry and stops the exporters (bounded by OPENLOG_SHUTDOWN_TIMEOUT). Idempotent.</summary>
    public void Dispose()
    {
        if (Interlocked.Exchange(ref shutdown, 1) != 0) return;
        AppDomain.CurrentDomain.ProcessExit -= OnProcessExit;
        if (sdk != null)
        {
            var timeout = Config.ShutdownTimeout;
            var task = Task.Run(() => sdk.Dispose());
            if (!task.Wait(timeout)) diag.Warn("shutdown timed out", ("timeout", timeout));
        }
        globals?.Dispose();
        lock (Gate)
        {
            if (ReferenceEquals(current, this)) current = null;
        }
    }

    private static class NoopProviders
    {
        public static readonly TracerProvider Tracer = new NoopTracerProvider();
        public static readonly MeterProvider Meter = new NoopMeterProvider();
        public static readonly LoggerProvider Logger = new NoopLoggerProvider();

        private sealed class NoopTracerProvider : TracerProvider { }
        private sealed class NoopMeterProvider : MeterProvider { }
        private sealed class NoopLoggerProvider : LoggerProvider { }
    }
}

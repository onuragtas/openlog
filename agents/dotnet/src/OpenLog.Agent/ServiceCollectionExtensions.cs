using System;
using Microsoft.Extensions.Logging;
using OpenLog.Agent;
using OpenTelemetry;

namespace Microsoft.Extensions.DependencyInjection;

/// <summary>openlog registration for ASP.NET Core and generic-host applications.</summary>
public static class OpenLogServiceCollectionExtensions
{
    /// <summary>
    /// Adds the openlog agent: OpenTelemetry tracing, metrics and logging (ILogger → OTLP) exporting to openlog, with
    /// the openlog sampler, resource detection, propagators and instrumentations. Configuration comes from the
    /// environment (OPENLOG_* &gt; OTEL_*) and <paramref name="configure"/>. The returned builder accepts further
    /// OpenTelemetry configuration, e.g. <c>.WithTracing(b =&gt; b.AddSource("MyCompany.*"))</c>.
    /// <code>
    /// var builder = WebApplication.CreateBuilder(args);
    /// builder.Services.AddOpenLog(o =&gt; o.ServiceName = "checkout");
    /// </code>
    /// Throws <see cref="OpenLogConfigException"/> for invalid configuration. With OPENLOG_ENABLED=false nothing is registered.
    /// </summary>
    public static IOpenTelemetryBuilder AddOpenLog(this IServiceCollection services, Action<OpenLogOptions>? configure = null)
    {
        if (services == null) throw new ArgumentNullException(nameof(services));
        var options = new OpenLogOptions();
        configure?.Invoke(options);
        var cfg = ConfigLoader.Load(options, out var warnings);
        var diag = new Diag(cfg.LogLevel);
        foreach (var w in warnings) diag.Warn(w);
        if (!cfg.Enabled)
        {
            diag.Info("disabled (OPENLOG_ENABLED=false)");
            return new DisabledBuilder(services);
        }
        // Process-wide for the lifetime of the application.
        _ = OpenLogSetup.InstallGlobals(cfg, diag);
        services.Configure<LoggerFactoryOptions>(o => o.ActivityTrackingOptions |= ActivityTrackingOptions.TraceId | ActivityTrackingOptions.SpanId);
        var builder = services.AddOpenTelemetry()
            .ConfigureResource(rb => OpenLogSetup.ConfigureResource(rb, cfg, diag))
            .WithTracing(b => OpenLogSetup.ConfigureTracing(b, cfg, diag, addExporter: true))
            .WithMetrics(b => OpenLogSetup.ConfigureMetrics(b, cfg, addExporter: true))
            .WithLogging(b => OpenLogSetup.ConfigureLogging(b, cfg, addExporter: true), OpenLogSetup.ConfigureLoggerOptions);
        services.AddSingleton(cfg);
        OpenLogSetup.LogStarted(cfg, diag, "services");
        return builder;
    }

    private sealed class DisabledBuilder : IOpenTelemetryBuilder
    {
        public DisabledBuilder(IServiceCollection services) => Services = services;

        public IServiceCollection Services { get; }
    }
}

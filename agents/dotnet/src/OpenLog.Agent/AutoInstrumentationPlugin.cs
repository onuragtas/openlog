using System;
using OpenTelemetry.Exporter;
using OpenTelemetry.Metrics;
using OpenTelemetry.Resources;
using OpenTelemetry.Trace;

namespace OpenLog.Agent.AutoInstrumentation;

/// <summary>
/// Plugin for the OpenTelemetry .NET automatic instrumentation (zero code changes):
/// <code>
/// OTEL_DOTNET_AUTO_PLUGINS="OpenLog.Agent.AutoInstrumentation.OpenLogPlugin, OpenLog.Agent, Version=1.0.0.0, Culture=neutral, PublicKeyToken=null"
/// </code>
/// The automatic instrumentation owns the providers, instrumentations and exporters (configured with OTEL_* variables);
/// this plugin adds what makes the data openlog-shaped: the openlog resource (host.id chain, container.id, service
/// settings from OPENLOG_*), the openlog sampler (ot=th, sampling.ratio), the random-flag propagator, route and DB
/// statement processors, process metrics and the license key header. Methods are discovered by name (no dependency on
/// the automatic instrumentation assemblies).
/// </summary>
public sealed class OpenLogPlugin
{
    private readonly OpenLogConfig cfg;
    private readonly Diag diag;
    private IDisposable? globals;

    public OpenLogPlugin()
    {
        cfg = ConfigLoader.Load(null, out var warnings);
        diag = new Diag(cfg.LogLevel);
        foreach (var w in warnings) diag.Warn(w);
    }

    /// <summary>Called once when the automatic instrumentation initializes.</summary>
    public void Initializing()
    {
        if (!cfg.Enabled) return;
        globals ??= OpenLogSetup.InstallGlobals(cfg, diag);
        OpenLogSetup.LogStarted(cfg, diag, "auto-instrumentation plugin");
    }

    public ResourceBuilder ConfigureResource(ResourceBuilder builder)
    {
        if (!cfg.Enabled) return builder;
        builder.AddDetector(new OpenLogResourceDetector(cfg));
        return builder;
    }

    public TracerProviderBuilder BeforeConfigureTracerProvider(TracerProviderBuilder builder)
    {
        if (!cfg.Enabled) return builder;
        builder.SetSampler(OpenLogSampler.Create(cfg.SamplingRatio, cfg.SamplingRV));
        foreach (var (name, source) in OpenLogSetup.NativeSources)
        {
            if (cfg.IsEnabled(name)) builder.AddSource(source);
        }
        foreach (var s in cfg.ActivitySources) builder.AddSource(s);
        builder.AddProcessor(new RouteProcessor());
        builder.AddProcessor(new DbStatementProcessor(cfg.DbQueryText));
        return builder;
    }

    public TracerProviderBuilder AfterConfigureTracerProvider(TracerProviderBuilder builder)
    {
        if (!cfg.Enabled) return builder;
        // OTEL_PROPAGATORS is applied by the automatic instrumentation before this point; keep the random flag.
        globals ??= OpenLogSetup.InstallGlobals(cfg, diag);
        return builder;
    }

    public MeterProviderBuilder BeforeConfigureMeterProvider(MeterProviderBuilder builder)
    {
        if (!cfg.Enabled || !cfg.RuntimeMetrics) return builder;
        ProcessMetrics.EnsureCreated();
        builder.AddMeter(ProcessMetrics.MeterName);
        return builder;
    }

    public void ConfigureTracesOptions(OtlpExporterOptions options) => AddHeaders(options);

    public void ConfigureMetricsOptions(OtlpExporterOptions options) => AddHeaders(options);

    public void ConfigureLogsOptions(OtlpExporterOptions options) => AddHeaders(options);

    private void AddHeaders(OtlpExporterOptions options)
    {
        if (!cfg.Enabled || cfg.LicenseKey.Length == 0) return;
        var header = OpenLogConfig.LicenseKeyHeader + "=" + cfg.LicenseKey;
        options.Headers = string.IsNullOrEmpty(options.Headers) ? header : options.Headers + "," + header;
    }
}

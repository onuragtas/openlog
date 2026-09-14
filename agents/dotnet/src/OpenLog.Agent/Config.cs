using System;
using System.Collections;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;
using System.Reflection;
using System.Text.RegularExpressions;
using OpenTelemetry.Logs;
using OpenTelemetry.Metrics;
using OpenTelemetry.Trace;

namespace OpenLog.Agent;

/// <summary>What happens to <c>db.query.text</c> / <c>db.statement</c> (OPENLOG_DB_QUERY_TEXT).</summary>
public enum DbQueryTextMode
{
    /// <summary>Literals replaced by <c>?</c> like the Go agent's openlogsql.Sanitize (default).</summary>
    Sanitized,
    /// <summary>The statement as the instrumentation recorded it.</summary>
    Raw,
    /// <summary>The attribute is removed.</summary>
    Off,
}

/// <summary>Thrown by <see cref="OpenLogAgent.Start(OpenLogOptions)"/> for invalid configuration.</summary>
public sealed class OpenLogConfigException : Exception
{
    public OpenLogConfigException(string message) : base(message) { }
}

/// <summary>
/// Options of <see cref="OpenLogAgent.Start(OpenLogOptions)"/> and <c>services.AddOpenLog()</c>. Every property set here
/// overrides the corresponding environment variable (precedence: options &gt; OPENLOG_* &gt; OTEL_* &gt; defaults).
/// </summary>
public sealed class OpenLogOptions
{
    /// <summary>OPENLOG_ENABLED (OTEL_SDK_DISABLED). false: nothing is installed.</summary>
    public bool? Enabled { get; set; }
    /// <summary>OPENLOG_LICENSE_KEY, sent as header openlog-license-key.</summary>
    public string? LicenseKey { get; set; }
    /// <summary>OPENLOG_ENDPOINT (OTEL_EXPORTER_OTLP_ENDPOINT): OTLP base URL; http/protobuf appends /v1/{traces,metrics,logs}.</summary>
    public string? Endpoint { get; set; }
    /// <summary>OPENLOG_PROTOCOL (OTEL_EXPORTER_OTLP_PROTOCOL): http/protobuf (default) or grpc.</summary>
    public string? Protocol { get; set; }
    /// <summary>OPENLOG_COMPRESSION (OTEL_EXPORTER_OTLP_COMPRESSION): gzip (default) or none.</summary>
    public string? Compression { get; set; }
    /// <summary>Extra export headers, merged over OTEL_EXPORTER_OTLP_HEADERS (the license key header wins).</summary>
    public IDictionary<string, string>? Headers { get; set; }
    public string? ServiceName { get; set; }
    public string? ServiceVersion { get; set; }
    public string? ServiceNamespace { get; set; }
    /// <summary>deployment.environment.name (OPENLOG_ENVIRONMENT).</summary>
    public string? Environment { get; set; }
    /// <summary>OPENLOG_SAMPLING_RATIO: parent-based head sampling ratio of new traces (0..1).</summary>
    public double? SamplingRatio { get; set; }
    /// <summary>OPENLOG_SAMPLING_RV: new traces write explicit randomness (tracestate ot=rv).</summary>
    public bool? SamplingRV { get; set; }
    /// <summary>OPENLOG_LOG_LEVEL (OTEL_LOG_LEVEL): debug, info, warn, error, off.</summary>
    public string? LogLevel { get; set; }
    public IDictionary<string, string>? ResourceAttributes { get; set; }
    /// <summary>OPENLOG_HOST_ID: explicit host.id instead of detection.</summary>
    public string? HostId { get; set; }
    /// <summary>OPENLOG_RUNTIME_METRICS: .NET runtime and process metrics (default true).</summary>
    public bool? RuntimeMetrics { get; set; }
    /// <summary>OPENLOG_METRIC_EXPORT_INTERVAL (Go duration, e.g. 60s; OTEL_METRIC_EXPORT_INTERVAL in ms).</summary>
    public TimeSpan? MetricExportInterval { get; set; }
    /// <summary>OPENLOG_SHUTDOWN_TIMEOUT: bound of the final flush (default 5s).</summary>
    public TimeSpan? ShutdownTimeout { get; set; }
    /// <summary>Timeout of one export request (default 10s).</summary>
    public TimeSpan? ExportTimeout { get; set; }
    /// <summary>OPENLOG_HOST_ROOT: prefix for host files (/etc/machine-id, …).</summary>
    public string? HostRoot { get; set; }
    public string? InfraStateDir { get; set; }
    public string? InfraRuntimeDir { get; set; }
    /// <summary>OPENLOG_STATE_DIR: where this agent persists a generated host id.</summary>
    public string? StateDir { get; set; }
    /// <summary>OPENLOG_DB_QUERY_TEXT: sanitized (default), raw or off.</summary>
    public DbQueryTextMode? DbQueryText { get; set; }
    /// <summary>OPENLOG_LOGS_EXPORT: export ILogger records as OTLP logs (default true).</summary>
    public bool? LogsExport { get; set; }
    /// <summary>OPENLOG_INSTRUMENTATIONS_DISABLED: e.g. ["sqlclient", "redis"] (see <see cref="Instrumentations.All"/>).</summary>
    public IList<string>? DisabledInstrumentations { get; set; }
    /// <summary>OPENLOG_HTTP_IGNORE_PATHS: incoming request paths without spans (exact match, e.g. /healthz).</summary>
    public IList<string>? HttpIgnorePaths { get; set; }
    /// <summary>OPENLOG_ACTIVITY_SOURCES: extra ActivitySource names to export (wildcards allowed, e.g. MyCompany.*).</summary>
    public IList<string>? ActivitySources { get; set; }
    /// <summary>OPENLOG_METERS: extra Meter names to export.</summary>
    public IList<string>? Meters { get; set; }
    /// <summary>OPENLOG_SHUTDOWN_ON_SIGNAL: OpenLogAgent.Start flushes on process exit / SIGTERM (default true).</summary>
    public bool? ShutdownOnExit { get; set; }

    /// <summary>Called after the agent configured the tracer provider (add sources or instrumentations).</summary>
    public Action<TracerProviderBuilder>? ConfigureTracing { get; set; }
    /// <summary>Called after the agent configured the meter provider.</summary>
    public Action<MeterProviderBuilder>? ConfigureMetrics { get; set; }
    /// <summary>Called after the agent configured the logger provider.</summary>
    public Action<LoggerProviderBuilder>? ConfigureLogging { get; set; }
}

/// <summary>Short names of the built-in instrumentations (OPENLOG_INSTRUMENTATIONS_DISABLED).</summary>
public static class Instrumentations
{
    public const string AspNetCore = "aspnetcore";
    public const string HttpClient = "httpclient";
    public const string SqlClient = "sqlclient";
    public const string Npgsql = "npgsql";
    public const string MySqlConnector = "mysqlconnector";
    public const string Redis = "redis";
    public const string MassTransit = "masstransit";
    public const string Grpc = "grpc";

    public static readonly IReadOnlyList<string> All = new[] { AspNetCore, HttpClient, SqlClient, Npgsql, MySqlConnector, Redis, MassTransit, Grpc };
}

/// <summary>Resolved agent configuration.</summary>
public sealed class OpenLogConfig
{
    public const string ProtocolHttpProtobuf = "http/protobuf";
    public const string ProtocolGrpc = "grpc";
    /// <summary>Ingest authentication header (docs/contracts/config.md).</summary>
    public const string LicenseKeyHeader = "openlog-license-key";

    public bool Enabled { get; internal set; } = true;
    public string LicenseKey { get; internal set; } = "";
    public string Endpoint { get; internal set; } = "";
    public string Protocol { get; internal set; } = ProtocolHttpProtobuf;
    public bool Gzip { get; internal set; } = true;
    public Dictionary<string, string> Headers { get; internal set; } = new(StringComparer.Ordinal);
    public string ServiceName { get; internal set; } = "";
    public string ServiceVersion { get; internal set; } = "";
    public string ServiceNamespace { get; internal set; } = "";
    public string Environment { get; internal set; } = "";
    public double SamplingRatio { get; internal set; } = 1;
    public bool SamplingRV { get; internal set; }
    public DiagnosticLevel LogLevel { get; internal set; } = DiagnosticLevel.Warn;
    public Dictionary<string, string> ResourceAttributes { get; internal set; } = new(StringComparer.Ordinal);
    public string HostId { get; internal set; } = "";
    public bool RuntimeMetrics { get; internal set; } = true;
    public TimeSpan MetricExportInterval { get; internal set; } = TimeSpan.FromSeconds(60);
    public TimeSpan ShutdownTimeout { get; internal set; } = TimeSpan.FromSeconds(5);
    public TimeSpan ExportTimeout { get; internal set; } = TimeSpan.FromSeconds(10);
    public string HostRoot { get; internal set; } = "/";
    public string InfraStateDir { get; internal set; } = "/var/lib/openlog-infra-agent";
    public string InfraRuntimeDir { get; internal set; } = "/run/openlog-infra-agent";
    public string StateDir { get; internal set; } = "";
    public DbQueryTextMode DbQueryText { get; internal set; } = DbQueryTextMode.Sanitized;
    public bool LogsExport { get; internal set; } = true;
    public List<string> DisabledInstrumentations { get; internal set; } = new();
    public List<string> HttpIgnorePaths { get; internal set; } = new();
    public List<string> ActivitySources { get; internal set; } = new();
    public List<string> Meters { get; internal set; } = new();
    public bool ShutdownOnExit { get; internal set; } = true;

    internal Action<TracerProviderBuilder>? ConfigureTracing { get; set; }
    internal Action<MeterProviderBuilder>? ConfigureMetrics { get; set; }
    internal Action<LoggerProviderBuilder>? ConfigureLogging { get; set; }

    /// <summary>True unless the instrumentation's short name is in OPENLOG_INSTRUMENTATIONS_DISABLED.</summary>
    public bool IsEnabled(string instrumentation) => !DisabledInstrumentations.Contains(instrumentation, StringComparer.OrdinalIgnoreCase);
}

/// <summary>Resolves defaults &lt; OTEL_* &lt; OPENLOG_* &lt; options (same precedence and names as the Go and Node.js agents).</summary>
public static class ConfigLoader
{
    private const string DefaultHttpEndpoint = "http://localhost:4318";
    private const string DefaultGrpcEndpoint = "http://localhost:4317";

    /// <summary>The process environment as a dictionary.</summary>
    public static IDictionary<string, string?> ProcessEnvironment()
    {
        var d = new Dictionary<string, string?>(StringComparer.Ordinal);
        foreach (DictionaryEntry e in System.Environment.GetEnvironmentVariables())
        {
            d[(string)e.Key] = e.Value as string;
        }
        return d;
    }

    /// <summary>Parses the W3C-baggage-like "k1=v1,k2=v2" format (values may be percent-encoded).</summary>
    public static Dictionary<string, string> ParseKV(string s)
    {
        var d = new Dictionary<string, string>(StringComparer.Ordinal);
        foreach (var part in s.Split(','))
        {
            var idx = part.IndexOf('=');
            if (idx < 0) continue;
            var k = part.Substring(0, idx).Trim();
            if (k.Length == 0) continue;
            var v = part.Substring(idx + 1).Trim();
            try
            {
                v = Uri.UnescapeDataString(v);
            }
            catch (Exception)
            {
                // keep the raw value, like Go's url.PathUnescape error path
            }
            d[k] = v;
        }
        return d;
    }

    private static readonly Regex DurationPart = new(@"\G(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)", RegexOptions.CultureInvariant);

    /// <summary>Parses a Go duration ("1m30s", "500ms", "1.5s"); null when invalid.</summary>
    public static TimeSpan? ParseGoDuration(string s)
    {
        var str = s.Trim();
        if (str.Length == 0) return null;
        if (str == "0") return TimeSpan.Zero;
        double ms = 0;
        var pos = 0;
        while (pos < str.Length)
        {
            var m = DurationPart.Match(str, pos);
            if (!m.Success) return null;
            var n = double.Parse(m.Groups[1].Value, NumberStyles.Float, CultureInfo.InvariantCulture);
            ms += m.Groups[2].Value switch
            {
                "ns" => n / 1e6,
                "us" or "µs" or "μs" => n / 1e3,
                "ms" => n,
                "s" => n * 1000,
                "m" => n * 60_000,
                _ => n * 3_600_000,
            };
            pos = m.Index + m.Length;
        }
        return TimeSpan.FromTicks((long)Math.Round(ms * TimeSpan.TicksPerMillisecond));
    }

    /// <summary>strconv.ParseBool.</summary>
    public static bool? ParseBool(string v) => v switch
    {
        "1" or "t" or "T" or "true" or "TRUE" or "True" => true,
        "0" or "f" or "F" or "false" or "FALSE" or "False" => false,
        _ => null,
    };

    private static List<string> List(string v) => v.Split(',').Select(x => x.Trim()).Where(x => x.Length > 0).ToList();

    /// <summary>Loads the configuration from the process environment and options.</summary>
    public static OpenLogConfig Load(OpenLogOptions? options, out List<string> warnings) => Load(ProcessEnvironment(), options, out warnings);

    /// <summary>Loads the configuration from <paramref name="env"/> and options.</summary>
    public static OpenLogConfig Load(IDictionary<string, string?> env, OpenLogOptions? options, out List<string> warnings)
    {
        var warns = new List<string>();
        warnings = warns;
        var c = new OpenLogConfig();
        string protocol = "", compression = "";

        (string Value, string Name)? Get(params string[] names)
        {
            foreach (var n in names)
            {
                if (env.TryGetValue(n, out var v) && v != null && v.Trim().Length > 0) return (v.Trim(), n);
            }
            return null;
        }
        void BoolVar(string name, Action<bool> set)
        {
            var g = Get(name);
            if (g == null) return;
            var b = ParseBool(g.Value.Value);
            if (b == null) warns.Add($"{name}=\"{g.Value.Value}\" is not a boolean; ignored");
            else set(b.Value);
        }
        void DurVar(string name, Action<TimeSpan> set)
        {
            var g = Get(name);
            if (g == null) return;
            var d = ParseGoDuration(g.Value.Value);
            if (d == null || d.Value <= TimeSpan.Zero) warns.Add($"{name}=\"{g.Value.Value}\" is not a positive duration; ignored");
            else set(d.Value);
        }

        // ---- OTEL_* (lower precedence) ----
        var g = Get("OTEL_SDK_DISABLED");
        if (g != null && ParseBool(g.Value.Value) is bool disabled) c.Enabled = !disabled;
        if ((g = Get("OTEL_EXPORTER_OTLP_ENDPOINT")) != null) c.Endpoint = g.Value.Value;
        if ((g = Get("OTEL_EXPORTER_OTLP_PROTOCOL")) != null) protocol = g.Value.Value;
        if ((g = Get("OTEL_EXPORTER_OTLP_COMPRESSION")) != null) compression = g.Value.Value;
        if ((g = Get("OTEL_EXPORTER_OTLP_HEADERS")) != null) Merge(c.Headers, ParseKV(g.Value.Value));
        if ((g = Get("OTEL_SERVICE_NAME")) != null) c.ServiceName = g.Value.Value;
        if ((g = Get("OTEL_RESOURCE_ATTRIBUTES")) != null) Merge(c.ResourceAttributes, ParseKV(g.Value.Value));
        if ((g = Get("OTEL_TRACES_SAMPLER_ARG")) != null)
        {
            var sampler = Get("OTEL_TRACES_SAMPLER")?.Value ?? "";
            if (sampler.Length == 0 || sampler.EndsWith("traceidratio", StringComparison.Ordinal))
            {
                if (double.TryParse(g.Value.Value, NumberStyles.Float, CultureInfo.InvariantCulture, out var r)) c.SamplingRatio = r;
            }
        }
        if ((g = Get("OTEL_LOG_LEVEL")) != null && Diag.ParseLevel(g.Value.Value) is DiagnosticLevel otelLevel) c.LogLevel = otelLevel;
        if ((g = Get("OTEL_METRIC_EXPORT_INTERVAL")) != null && int.TryParse(g.Value.Value, NumberStyles.None, CultureInfo.InvariantCulture, out var intervalMs) && intervalMs > 0)
        {
            c.MetricExportInterval = TimeSpan.FromMilliseconds(intervalMs);
        }

        // ---- OPENLOG_* ----
        BoolVar("OPENLOG_ENABLED", b => c.Enabled = b);
        if ((g = Get("OPENLOG_LICENSE_KEY")) != null) c.LicenseKey = g.Value.Value;
        if ((g = Get("OPENLOG_ENDPOINT")) != null) c.Endpoint = g.Value.Value;
        if ((g = Get("OPENLOG_PROTOCOL")) != null) protocol = g.Value.Value;
        if ((g = Get("OPENLOG_COMPRESSION")) != null) compression = g.Value.Value;
        if ((g = Get("OPENLOG_SERVICE_NAME")) != null) c.ServiceName = g.Value.Value;
        if ((g = Get("OPENLOG_SERVICE_VERSION")) != null) c.ServiceVersion = g.Value.Value;
        if ((g = Get("OPENLOG_SERVICE_NAMESPACE")) != null) c.ServiceNamespace = g.Value.Value;
        if ((g = Get("OPENLOG_ENVIRONMENT")) != null) c.Environment = g.Value.Value;
        if ((g = Get("OPENLOG_SAMPLING_RATIO")) != null)
        {
            if (double.TryParse(g.Value.Value, NumberStyles.Float, CultureInfo.InvariantCulture, out var r)) c.SamplingRatio = r;
            else warns.Add($"{g.Value.Name}=\"{g.Value.Value}\" is not a number; ignored");
        }
        if ((g = Get("OPENLOG_LOG_LEVEL")) != null)
        {
            if (Diag.ParseLevel(g.Value.Value) is DiagnosticLevel l) c.LogLevel = l;
            else warns.Add($"{g.Value.Name}: unknown log level \"{g.Value.Value}\" (debug, info, warn, error, off)");
        }
        if ((g = Get("OPENLOG_RESOURCE_ATTRIBUTES")) != null) Merge(c.ResourceAttributes, ParseKV(g.Value.Value));
        if ((g = Get("OPENLOG_HOST_ID")) != null) c.HostId = g.Value.Value;
        BoolVar("OPENLOG_RUNTIME_METRICS", b => c.RuntimeMetrics = b);
        DurVar("OPENLOG_METRIC_EXPORT_INTERVAL", d => c.MetricExportInterval = d);
        DurVar("OPENLOG_SHUTDOWN_TIMEOUT", d => c.ShutdownTimeout = d);
        if ((g = Get("OPENLOG_HOST_ROOT")) != null) c.HostRoot = g.Value.Value;
        if ((g = Get("OPENLOG_INFRA_STATE_DIR")) != null) c.InfraStateDir = g.Value.Value;
        if ((g = Get("OPENLOG_INFRA_RUNTIME_DIR")) != null) c.InfraRuntimeDir = g.Value.Value;
        BoolVar("OPENLOG_SAMPLING_RV", b => c.SamplingRV = b);
        if ((g = Get("OPENLOG_STATE_DIR")) != null) c.StateDir = g.Value.Value;
        if ((g = Get("OPENLOG_DB_QUERY_TEXT")) != null)
        {
            switch (g.Value.Value.ToLowerInvariant())
            {
                case "sanitized": c.DbQueryText = DbQueryTextMode.Sanitized; break;
                case "raw": c.DbQueryText = DbQueryTextMode.Raw; break;
                case "off": c.DbQueryText = DbQueryTextMode.Off; break;
                default: warns.Add($"{g.Value.Name}=\"{g.Value.Value}\": use sanitized, raw or off; ignored"); break;
            }
        }
        BoolVar("OPENLOG_LOGS_EXPORT", b => c.LogsExport = b);
        if ((g = Get("OPENLOG_INSTRUMENTATIONS_DISABLED")) != null) c.DisabledInstrumentations = List(g.Value.Value);
        if ((g = Get("OPENLOG_HTTP_IGNORE_PATHS")) != null) c.HttpIgnorePaths = List(g.Value.Value);
        if ((g = Get("OPENLOG_ACTIVITY_SOURCES")) != null) c.ActivitySources = List(g.Value.Value);
        if ((g = Get("OPENLOG_METERS")) != null) c.Meters = List(g.Value.Value);
        BoolVar("OPENLOG_SHUTDOWN_ON_SIGNAL", b => c.ShutdownOnExit = b);

        // ---- options ----
        var o = options;
        if (o != null)
        {
            if (o.Enabled.HasValue) c.Enabled = o.Enabled.Value;
            if (o.LicenseKey != null) c.LicenseKey = o.LicenseKey;
            if (o.Endpoint != null) c.Endpoint = o.Endpoint;
            if (o.Protocol != null) protocol = o.Protocol;
            if (o.Compression != null) compression = o.Compression;
            if (o.Headers != null) Merge(c.Headers, o.Headers);
            if (o.ServiceName != null) c.ServiceName = o.ServiceName;
            if (o.ServiceVersion != null) c.ServiceVersion = o.ServiceVersion;
            if (o.ServiceNamespace != null) c.ServiceNamespace = o.ServiceNamespace;
            if (o.Environment != null) c.Environment = o.Environment;
            if (o.SamplingRatio.HasValue) c.SamplingRatio = o.SamplingRatio.Value;
            if (o.SamplingRV.HasValue) c.SamplingRV = o.SamplingRV.Value;
            if (o.LogLevel != null)
            {
                if (Diag.ParseLevel(o.LogLevel) is DiagnosticLevel l) c.LogLevel = l;
                else warns.Add($"LogLevel: unknown log level \"{o.LogLevel}\"");
            }
            if (o.ResourceAttributes != null) Merge(c.ResourceAttributes, o.ResourceAttributes);
            if (o.HostId != null) c.HostId = o.HostId;
            if (o.RuntimeMetrics.HasValue) c.RuntimeMetrics = o.RuntimeMetrics.Value;
            if (o.MetricExportInterval.HasValue) c.MetricExportInterval = o.MetricExportInterval.Value;
            if (o.ShutdownTimeout.HasValue) c.ShutdownTimeout = o.ShutdownTimeout.Value;
            if (o.ExportTimeout.HasValue) c.ExportTimeout = o.ExportTimeout.Value;
            if (o.HostRoot != null) c.HostRoot = o.HostRoot;
            if (o.InfraStateDir != null) c.InfraStateDir = o.InfraStateDir;
            if (o.InfraRuntimeDir != null) c.InfraRuntimeDir = o.InfraRuntimeDir;
            if (o.StateDir != null) c.StateDir = o.StateDir;
            if (o.DbQueryText.HasValue) c.DbQueryText = o.DbQueryText.Value;
            if (o.LogsExport.HasValue) c.LogsExport = o.LogsExport.Value;
            if (o.DisabledInstrumentations != null) c.DisabledInstrumentations = o.DisabledInstrumentations.ToList();
            if (o.HttpIgnorePaths != null) c.HttpIgnorePaths = o.HttpIgnorePaths.ToList();
            if (o.ActivitySources != null) c.ActivitySources.AddRange(o.ActivitySources);
            if (o.Meters != null) c.Meters.AddRange(o.Meters);
            if (o.ShutdownOnExit.HasValue) c.ShutdownOnExit = o.ShutdownOnExit.Value;
            c.ConfigureTracing = o.ConfigureTracing;
            c.ConfigureMetrics = o.ConfigureMetrics;
            c.ConfigureLogging = o.ConfigureLogging;
        }

        // ---- normalize and validate ----
        switch (protocol.ToLowerInvariant())
        {
            case "http/protobuf":
            case "http":
            case "":
                c.Protocol = OpenLogConfig.ProtocolHttpProtobuf;
                break;
            case "grpc":
                c.Protocol = OpenLogConfig.ProtocolGrpc;
                break;
            default:
                throw new OpenLogConfigException($"openlog: unsupported protocol \"{protocol}\" (use http/protobuf or grpc)");
        }
        c.Gzip = compression.ToLowerInvariant() switch
        {
            "gzip" or "" => true,
            "none" => false,
            _ => throw new OpenLogConfigException($"openlog: unsupported compression \"{compression}\" (use gzip or none)"),
        };
        if (c.Endpoint.Length == 0) c.Endpoint = c.Protocol == OpenLogConfig.ProtocolGrpc ? DefaultGrpcEndpoint : DefaultHttpEndpoint;
        if (c.Endpoint.IndexOf("://", StringComparison.Ordinal) < 0) c.Endpoint = "https://" + c.Endpoint;
        if (!Uri.TryCreate(c.Endpoint, UriKind.Absolute, out var u) || (u.Scheme != "http" && u.Scheme != "https") || u.Host.Length == 0)
        {
            throw new OpenLogConfigException($"openlog: invalid endpoint \"{c.Endpoint}\"");
        }
        c.Endpoint = c.Endpoint.TrimEnd('/');
        if (double.IsNaN(c.SamplingRatio) || c.SamplingRatio < 0 || c.SamplingRatio > 1)
        {
            warns.Add($"sampling ratio {c.SamplingRatio.ToString(CultureInfo.InvariantCulture)} outside 0..1; clamped");
            c.SamplingRatio = double.IsNaN(c.SamplingRatio) ? 1 : Math.Min(Math.Max(c.SamplingRatio, 0), 1);
        }
        if (c.ServiceName.Length == 0)
        {
            if (c.ResourceAttributes.TryGetValue("service.name", out var fromAttrs) && fromAttrs.Length > 0)
            {
                c.ServiceName = fromAttrs;
            }
            else
            {
                var entry = Assembly.GetEntryAssembly()?.GetName().Name;
                c.ServiceName = "unknown_service:" + (string.IsNullOrEmpty(entry) ? "dotnet" : entry);
                warns.Add($"no service name configured (OPENLOG_SERVICE_NAME); using \"{c.ServiceName}\"");
            }
        }
        if (c.LicenseKey.Length > 0)
        {
            c.Headers[OpenLogConfig.LicenseKeyHeader] = c.LicenseKey;
        }
        else if (!c.Headers.Keys.Any(k => string.Equals(k, OpenLogConfig.LicenseKeyHeader, StringComparison.OrdinalIgnoreCase) || string.Equals(k, "authorization", StringComparison.OrdinalIgnoreCase)))
        {
            warns.Add("no license key configured (OPENLOG_LICENSE_KEY); openlog ingest will reject the data");
        }
        foreach (var name in c.DisabledInstrumentations)
        {
            if (!Instrumentations.All.Contains(name, StringComparer.OrdinalIgnoreCase)) warns.Add($"unknown instrumentation \"{name}\" in the disabled list; ignored");
        }
        if (c.MetricExportInterval <= TimeSpan.Zero) c.MetricExportInterval = TimeSpan.FromSeconds(60);
        if (c.ShutdownTimeout <= TimeSpan.Zero) c.ShutdownTimeout = TimeSpan.FromSeconds(5);
        if (c.ExportTimeout <= TimeSpan.Zero) c.ExportTimeout = TimeSpan.FromSeconds(10);
        if (c.HostRoot.Length == 0) c.HostRoot = "/";
        return c;
    }

    private static void Merge(IDictionary<string, string> into, IEnumerable<KeyValuePair<string, string>> from)
    {
        foreach (var kv in from) into[kv.Key] = kv.Value;
    }
}

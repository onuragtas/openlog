using System;
using System.Collections.Generic;
using OpenLog.Agent;
using Xunit;

namespace OpenLog.Agent.Tests;

public class ConfigTests
{
    private static OpenLogConfig Load(Dictionary<string, string?> env, OpenLogOptions? o, out List<string> warnings) => ConfigLoader.Load(env, o, out warnings);

    [Fact]
    public void Defaults()
    {
        var c = Load(new(), null, out var warnings);
        Assert.True(c.Enabled);
        Assert.Equal("http://localhost:4318", c.Endpoint);
        Assert.Equal(OpenLogConfig.ProtocolHttpProtobuf, c.Protocol);
        Assert.True(c.Gzip);
        Assert.Equal(1.0, c.SamplingRatio);
        Assert.Equal(DbQueryTextMode.Sanitized, c.DbQueryText);
        Assert.Equal(TimeSpan.FromSeconds(60), c.MetricExportInterval);
        Assert.Equal(TimeSpan.FromSeconds(5), c.ShutdownTimeout);
        Assert.Equal("/run/openlog-infra-agent", c.InfraRuntimeDir);
        Assert.StartsWith("unknown_service:", c.ServiceName);
        Assert.Contains(warnings, w => w.Contains("license key"));
        Assert.Contains(warnings, w => w.Contains("service name"));
    }

    [Fact]
    public void OpenlogVariablesWinOverOtelAndOptionsWinOverBoth()
    {
        var env = new Dictionary<string, string?>
        {
            ["OTEL_EXPORTER_OTLP_ENDPOINT"] = "http://otel:4318",
            ["OPENLOG_ENDPOINT"] = "http://openlog:4318/",
            ["OTEL_SERVICE_NAME"] = "otel-name",
            ["OPENLOG_SERVICE_NAME"] = "checkout",
            ["OTEL_EXPORTER_OTLP_HEADERS"] = "x-a=1,openlog-license-key=fromheaders",
            ["OPENLOG_LICENSE_KEY"] = "lk",
            ["OTEL_TRACES_SAMPLER"] = "parentbased_traceidratio",
            ["OTEL_TRACES_SAMPLER_ARG"] = "0.5",
            ["OPENLOG_SAMPLING_RATIO"] = "0.25",
            ["OTEL_RESOURCE_ATTRIBUTES"] = "a=1,b=x%20y",
            ["OPENLOG_RESOURCE_ATTRIBUTES"] = "a=2",
            ["OPENLOG_METRIC_EXPORT_INTERVAL"] = "1m30s",
            ["OPENLOG_SHUTDOWN_TIMEOUT"] = "1500ms",
            ["OPENLOG_DB_QUERY_TEXT"] = "RAW",
            ["OPENLOG_INSTRUMENTATIONS_DISABLED"] = "sqlclient, redis",
            ["OPENLOG_HTTP_IGNORE_PATHS"] = "/healthz,/readyz",
            ["OPENLOG_COMPRESSION"] = "none",
        };
        var c = Load(env, new OpenLogOptions { ServiceVersion = "1.2.3" }, out var warnings);
        Assert.Equal("http://openlog:4318", c.Endpoint);
        Assert.Equal("checkout", c.ServiceName);
        Assert.Equal("1.2.3", c.ServiceVersion);
        Assert.Equal("lk", c.Headers[OpenLogConfig.LicenseKeyHeader]);
        Assert.Equal("1", c.Headers["x-a"]);
        Assert.Equal(0.25, c.SamplingRatio);
        Assert.Equal("2", c.ResourceAttributes["a"]);
        Assert.Equal("x y", c.ResourceAttributes["b"]);
        Assert.Equal(TimeSpan.FromSeconds(90), c.MetricExportInterval);
        Assert.Equal(TimeSpan.FromMilliseconds(1500), c.ShutdownTimeout);
        Assert.Equal(DbQueryTextMode.Raw, c.DbQueryText);
        Assert.False(c.IsEnabled(Instrumentations.SqlClient));
        Assert.False(c.IsEnabled(Instrumentations.Redis));
        Assert.True(c.IsEnabled(Instrumentations.Npgsql));
        Assert.Equal(new[] { "/healthz", "/readyz" }, c.HttpIgnorePaths);
        Assert.False(c.Gzip);
        Assert.Empty(warnings);

        var o = Load(env, new OpenLogOptions { Endpoint = "https://ingest.example.com", SamplingRatio = 0.1, LicenseKey = "opt" }, out _);
        Assert.Equal("https://ingest.example.com", o.Endpoint);
        Assert.Equal(0.1, o.SamplingRatio);
        Assert.Equal("opt", o.Headers[OpenLogConfig.LicenseKeyHeader]);
    }

    [Fact]
    public void InvalidValuesWarnOrThrow()
    {
        var c = Load(new() { ["OPENLOG_ENABLED"] = "yes", ["OPENLOG_SAMPLING_RATIO"] = "abc", ["OPENLOG_METRIC_EXPORT_INTERVAL"] = "-1s", ["OPENLOG_LOG_LEVEL"] = "loud", ["OPENLOG_INSTRUMENTATIONS_DISABLED"] = "nope" }, null, out var warnings);
        Assert.True(c.Enabled);
        Assert.Equal(1.0, c.SamplingRatio);
        Assert.Contains(warnings, w => w.Contains("OPENLOG_ENABLED"));
        Assert.Contains(warnings, w => w.Contains("OPENLOG_SAMPLING_RATIO"));
        Assert.Contains(warnings, w => w.Contains("OPENLOG_METRIC_EXPORT_INTERVAL"));
        Assert.Contains(warnings, w => w.Contains("OPENLOG_LOG_LEVEL"));
        Assert.Contains(warnings, w => w.Contains("nope"));

        Assert.Throws<OpenLogConfigException>(() => Load(new() { ["OPENLOG_PROTOCOL"] = "thrift" }, null, out _));
        Assert.Throws<OpenLogConfigException>(() => Load(new() { ["OPENLOG_COMPRESSION"] = "zstd" }, null, out _));
        Assert.Throws<OpenLogConfigException>(() => Load(new() { ["OPENLOG_ENDPOINT"] = "ftp://x" }, null, out _));

        var clamped = Load(new() { ["OPENLOG_SAMPLING_RATIO"] = "1.5" }, null, out var w2);
        Assert.Equal(1.0, clamped.SamplingRatio);
        Assert.Contains(w2, w => w.Contains("clamped"));
    }

    [Fact]
    public void GrpcDefaultsAndSchemeLessEndpoints()
    {
        var c = Load(new() { ["OTEL_EXPORTER_OTLP_PROTOCOL"] = "grpc" }, null, out _);
        Assert.Equal(OpenLogConfig.ProtocolGrpc, c.Protocol);
        Assert.Equal("http://localhost:4317", c.Endpoint);
        var tls = Load(new() { ["OPENLOG_ENDPOINT"] = "ingest.example.com:4318" }, null, out _);
        Assert.Equal("https://ingest.example.com:4318", tls.Endpoint);
        var disabled = Load(new() { ["OTEL_SDK_DISABLED"] = "true" }, null, out _);
        Assert.False(disabled.Enabled);
        var reenabled = Load(new() { ["OTEL_SDK_DISABLED"] = "true", ["OPENLOG_ENABLED"] = "1" }, null, out _);
        Assert.True(reenabled.Enabled);
    }

    [Theory]
    [InlineData("500ms", 500)]
    [InlineData("1.5s", 1500)]
    [InlineData("1m30s", 90_000)]
    [InlineData("2h", 7_200_000)]
    [InlineData("0", 0)]
    public void GoDurations(string s, double ms)
    {
        Assert.Equal(TimeSpan.FromMilliseconds(ms), ConfigLoader.ParseGoDuration(s));
    }

    [Theory]
    [InlineData("")]
    [InlineData("10")]
    [InlineData("1x")]
    [InlineData("s")]
    public void InvalidGoDurations(string s)
    {
        Assert.Null(ConfigLoader.ParseGoDuration(s));
    }
}

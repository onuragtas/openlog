using System;
using System.Collections.Concurrent;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Net.Http;
using System.Runtime.InteropServices;
using System.Text.Json;
using System.Threading.Tasks;
using Xunit;
using Xunit.Abstractions;

namespace OpenLog.Agent.IntegrationTests;

/// <summary>Skipped unless test/autoinstrumentation.sh prepared the automatic instrumentation and the test app.</summary>
public sealed class AutoInstrumentationFactAttribute : FactAttribute
{
    public AutoInstrumentationFactAttribute()
    {
        if (string.IsNullOrEmpty(Environment.GetEnvironmentVariable(AutoInstrumentationFixture.HomeVariable))
            || string.IsNullOrEmpty(Environment.GetEnvironmentVariable(AutoInstrumentationFixture.AppVariable)))
        {
            Skip = "run test/autoinstrumentation.sh (OpenTelemetry .NET automatic instrumentation)";
        }
    }
}

/// <summary>
/// The test app (no OpenTelemetry reference) under the OpenTelemetry .NET automatic instrumentation with the openlog
/// plugin installed in its net/ directory, exporting to the OTLP capture server.
/// </summary>
public sealed class AutoInstrumentationFixture : IAsyncLifetime
{
    public const string HomeVariable = "OPENLOG_TEST_OTEL_AUTO_HOME";
    public const string AppVariable = "OPENLOG_TEST_AUTO_APP";
    public const string InfraHostId = "a0de0000000000000000000000000077";
    public const string LicenseKey = "auto-license-key";

    private readonly string runtimeDir = Path.Combine(Path.GetTempPath(), "openlog-auto-" + Guid.NewGuid().ToString("N"));
    private Process? process;

    public OtlpCaptureServer Capture { get; private set; } = null!;
    public HttpClient Http { get; private set; } = null!;
    public ConcurrentQueue<string> Output { get; } = new();
    public bool Enabled { get; private set; }

    public async Task InitializeAsync()
    {
        var home = Environment.GetEnvironmentVariable(HomeVariable);
        var app = Environment.GetEnvironmentVariable(AppVariable);
        if (string.IsNullOrEmpty(home) || string.IsNullOrEmpty(app)) return;
        Enabled = true;
        Directory.CreateDirectory(runtimeDir);
        await File.WriteAllTextAsync(Path.Combine(runtimeDir, "host-id"), InfraHostId + "\n");
        Capture = await OtlpCaptureServer.StartAsync();
        var port = AppFixture.FreePort();
        var arch = RuntimeInformation.ProcessArchitecture == Architecture.Arm64 ? "linux-arm64" : "linux-x64";

        var psi = new ProcessStartInfo("dotnet", app) { RedirectStandardOutput = true, RedirectStandardError = true, UseShellExecute = false };
        var env = psi.Environment;
        foreach (var key in env.Keys.Where(k => k.StartsWith("OTEL_", StringComparison.Ordinal) || k.StartsWith("OPENLOG_", StringComparison.Ordinal)).ToList()) env.Remove(key);
        // what instrument.sh sets (automatic instrumentation docs), without the shell
        env["CORECLR_ENABLE_PROFILING"] = "1";
        env["CORECLR_PROFILER"] = "{918728DD-259F-4A6A-AC2B-B85E1B658318}";
        env["CORECLR_PROFILER_PATH"] = Path.Combine(home, arch, "OpenTelemetry.AutoInstrumentation.Native.so");
        env["DOTNET_STARTUP_HOOKS"] = Path.Combine(home, "net", "OpenTelemetry.AutoInstrumentation.StartupHook.dll");
        env["OTEL_DOTNET_AUTO_HOME"] = home;
        env["OTEL_DOTNET_AUTO_PLUGINS"] = "OpenLog.Agent.AutoInstrumentation.OpenLogPlugin, OpenLog.Agent";
        env["OTEL_EXPORTER_OTLP_ENDPOINT"] = Capture.Endpoint;
        env["OTEL_EXPORTER_OTLP_PROTOCOL"] = "http/protobuf";
        env["OTEL_BSP_SCHEDULE_DELAY"] = "200";
        env["OTEL_BLRP_SCHEDULE_DELAY"] = "200";
        env["OTEL_METRIC_EXPORT_INTERVAL"] = "1000";
        env["OPENLOG_LICENSE_KEY"] = LicenseKey;
        env["OPENLOG_SERVICE_NAME"] = "dotnet-auto";
        env["OPENLOG_SERVICE_VERSION"] = "2.0.1";
        env["OPENLOG_ENVIRONMENT"] = "test";
        env["OPENLOG_INFRA_RUNTIME_DIR"] = runtimeDir;
        env["OPENLOG_HTTP_IGNORE_PATHS"] = "/health";
        env["OPENLOG_LOG_LEVEL"] = "info";
        env["HTTP_PORT"] = port.ToString(System.Globalization.CultureInfo.InvariantCulture);
        process = Process.Start(psi)!;
        process.OutputDataReceived += (_, e) => { if (e.Data != null) Output.Enqueue(e.Data); };
        process.ErrorDataReceived += (_, e) => { if (e.Data != null) Output.Enqueue(e.Data); };
        process.BeginOutputReadLine();
        process.BeginErrorReadLine();

        // plain handler: this test process has no agent, the traceparent headers are hand-written
        Http = new HttpClient(new SocketsHttpHandler { ActivityHeadersPropagator = null }) { BaseAddress = new Uri($"http://127.0.0.1:{port}") };
        var deadline = DateTime.UtcNow.AddSeconds(60);
        while (true)
        {
            if (process.HasExited) throw new InvalidOperationException($"test app exited with {process.ExitCode}:\n{string.Join("\n", Output)}");
            try
            {
                if ((await Http.GetAsync("/health")).IsSuccessStatusCode) break;
            }
            catch (HttpRequestException)
            {
            }
            if (DateTime.UtcNow > deadline) throw new TimeoutException("test app not healthy:\n" + string.Join("\n", Output));
            await Task.Delay(250);
        }
    }

    public async Task DisposeAsync()
    {
        Http?.Dispose();
        if (process != null)
        {
            if (!process.HasExited) process.Kill(entireProcessTree: true);
            await process.WaitForExitAsync();
            process.Dispose();
        }
        if (Capture != null) await Capture.DisposeAsync();
        try
        {
            Directory.Delete(runtimeDir, true);
        }
        catch (IOException)
        {
        }
    }
}

public sealed class AutoInstrumentationTests : IClassFixture<AutoInstrumentationFixture>
{
    private const int Server = 2;
    private const int Client = 3;

    private readonly AutoInstrumentationFixture f;
    private readonly ITestOutputHelper output;

    public AutoInstrumentationTests(AutoInstrumentationFixture fixture, ITestOutputHelper output)
    {
        f = fixture;
        this.output = output;
    }

    private async Task<HttpResponseMessage> Get(string path, string traceId, string flags, string? tracestate = null)
    {
        var req = new HttpRequestMessage(HttpMethod.Get, path);
        req.Headers.TryAddWithoutValidation("traceparent", $"00-{traceId}-{ActivitySpanId.CreateRandom().ToHexString()}-{flags}");
        if (tracestate != null) req.Headers.TryAddWithoutValidation("tracestate", tracestate);
        return await f.Http.SendAsync(req);
    }

    private async Task<SpanData> WaitSpan(string traceId, Func<SpanData, bool> match, string what)
    {
        try
        {
            return await f.Capture.WaitFor(c => c.Spans.FirstOrDefault(s => s.TraceId == traceId && match(s)), what);
        }
        catch (TimeoutException)
        {
            output.WriteLine(string.Join("\n", f.Output));
            throw;
        }
    }

    [AutoInstrumentationFact]
    public async Task PluginAddsTheOpenLogResourceAndLicenseHeader()
    {
        var tid = ActivityTraceId.CreateRandom().ToHexString();
        (await Get("/users/5", tid, "01")).EnsureSuccessStatusCode();
        var span = await WaitSpan(tid, s => s.Kind == Server, "server span of /users/5");
        Assert.Equal("/users/{id:int}", span.Attr("http.route"));
        Assert.Equal("GET /users/{id:int}", span.Name);
        var r = span.Resource;
        Assert.Equal("dotnet-auto", r["service.name"]);
        Assert.Equal("2.0.1", r["service.version"]);
        Assert.Equal("test", r["deployment.environment.name"]);
        Assert.Equal(AutoInstrumentationFixture.InfraHostId, r["host.id"]);
        Assert.Equal("openlog", r["telemetry.distro.name"]);
        Assert.Equal(AgentVersion.Version, r["telemetry.distro.version"]);
        // the automatic instrumentation's own SDK resource stays (it owns the providers)
        Assert.Equal("dotnet", r["telemetry.sdk.language"]);
        Assert.Contains(f.Capture.Requests, q => q.Path == "/v1/traces");
        Assert.All(f.Capture.Requests, q => Assert.Equal(AutoInstrumentationFixture.LicenseKey, q.LicenseKey));
        Assert.DoesNotContain(f.Capture.Spans, s => s.Attr("url.path") == "/health");
        var log = await f.Capture.WaitFor(c => c.Logs.FirstOrDefault(l => l.TraceId == tid), "correlated log record");
        Assert.Equal(span.SpanId, log.SpanId);
    }

    [AutoInstrumentationFact]
    public async Task PluginSamplerWeightsRemoteParentsAndPropagatesTheRandomFlag()
    {
        var tid = ActivityTraceId.CreateRandom().ToHexString();
        var res = await Get("/outbound", tid, "03", "ot=th:c");
        res.EnsureSuccessStatusCode();
        using var doc = JsonDocument.Parse(await res.Content.ReadAsStringAsync());
        var downstream = doc.RootElement.GetProperty("traceparent").GetString()!;
        Assert.Matches($"^00-{tid}-[0-9a-f]{{16}}-03$", downstream);
        Assert.Equal("ot=th:c", doc.RootElement.GetProperty("tracestate").GetString());
        var entry = await WaitSpan(tid, s => s.Kind == Server && s.Attr("http.route") == "/outbound", "entry span");
        Assert.Equal("0.25", Convert.ToString(entry.Attributes["sampling.ratio"], System.Globalization.CultureInfo.InvariantCulture));
        var client = await WaitSpan(tid, s => s.Kind == Client, "HttpClient span");
        Assert.Equal(entry.SpanId, client.ParentSpanId);
        Assert.Equal(3u, client.Flags & 0xff);
    }

    [AutoInstrumentationFact]
    public async Task RootRequestsGetTheRandomFlagAndErrorsAreRecorded()
    {
        var res = await f.Http.GetAsync("/outbound");
        res.EnsureSuccessStatusCode();
        using var doc = JsonDocument.Parse(await res.Content.ReadAsStringAsync());
        Assert.EndsWith("-03", doc.RootElement.GetProperty("traceparent").GetString());

        var tid = ActivityTraceId.CreateRandom().ToHexString();
        Assert.Equal(System.Net.HttpStatusCode.InternalServerError, (await Get("/error", tid, "01")).StatusCode);
        var span = await WaitSpan(tid, s => s.Kind == Server && s.Attr("http.route") == "/error", "error span");
        Assert.Equal(2, span.StatusCode);
    }

    [AutoInstrumentationFact]
    public async Task PluginProcessMetrics()
    {
        var cpu = await f.Capture.WaitFor(c => c.Metrics.LastOrDefault(m => m.Name == "process.cpu.time" && m.Scope.Name == "OpenLog.Agent.Process"), "openlog process metrics");
        Assert.Equal("s", cpu.Unit);
        Assert.Equal(AutoInstrumentationFixture.InfraHostId, cpu.Resource["host.id"]);
    }
}

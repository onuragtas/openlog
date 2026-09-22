using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Net;
using System.Net.Http;
using System.Net.Sockets;
using System.Text.Json;
using System.Threading.Tasks;
using Microsoft.AspNetCore.Builder;
using Microsoft.Extensions.Logging;
using OpenLog.Agent;
using OpenLog.SampleApp;
using Xunit;

[assembly: CollectionBehavior(DisableTestParallelization = true)]

namespace OpenLog.Agent.IntegrationTests;

/// <summary>Skipped unless the environment variable (a database connection string) is set.</summary>
public sealed class DbFactAttribute : FactAttribute
{
    public DbFactAttribute(string variable)
    {
        if (string.IsNullOrWhiteSpace(Environment.GetEnvironmentVariable(variable))) Skip = $"{variable} not set (start test/docker-compose.yml)";
    }
}

public sealed class AppFixture : IAsyncLifetime
{
    public const string InfraHostId = "a0de0000000000000000000000000042";
    public const string LicenseKey = "test-license-key";

    private readonly string runtimeDir = Path.Combine(Path.GetTempPath(), "openlog-it-" + Guid.NewGuid().ToString("N"));

    public OtlpCaptureServer Capture { get; private set; } = null!;
    public WebApplication App { get; private set; } = null!;
    public HttpClient Http { get; private set; } = null!;
    public SampleSettings Settings { get; private set; } = null!;

    public static int FreePort()
    {
        var l = new TcpListener(IPAddress.Loopback, 0);
        l.Start();
        var port = ((IPEndPoint)l.LocalEndpoint).Port;
        l.Stop();
        return port;
    }

    public async Task InitializeAsync()
    {
        Directory.CreateDirectory(runtimeDir);
        await File.WriteAllTextAsync(Path.Combine(runtimeDir, "host-id"), InfraHostId + "\n");
        // read by the ASP.NET Core instrumentation from configuration when the host is built
        Environment.SetEnvironmentVariable("OTEL_DOTNET_EXPERIMENTAL_ASPNETCORE_ENABLE_GRPC_INSTRUMENTATION", "true");
        Capture = await OtlpCaptureServer.StartAsync();
        var s = SampleSettings.FromEnvironment();
        s.HttpPort = FreePort();
        s.GrpcPort = FreePort();
        Settings = s;
        App = SampleApp.SampleApp.Build(s, o =>
        {
            o.Endpoint = Capture.Endpoint;
            o.LicenseKey = LicenseKey;
            o.ServiceName = "dotnet-sample";
            o.ServiceVersion = "1.2.3";
            o.Environment = "test";
            o.InfraRuntimeDir = runtimeDir;
            o.MetricExportInterval = TimeSpan.FromSeconds(1);
            // The agent's own diagnostics on stderr, so a failing run says where host.id came from
            // ("resource host.id.source=infra-agent" or "…=generated"). A run timed out with
            // process.cpu.time present but no host.id on its resource and nothing in the log explained it.
            o.LogLevel = "debug";
            o.ResourceAttributes = new Dictionary<string, string> { ["team"] = "payments" };
            // the capture server runs in the same process: its requests must not become spans
            o.HttpIgnorePaths = new List<string> { "/v1/traces", "/v1/metrics", "/v1/logs" };
        });
        await App.StartAsync();
        // test requests carry hand-written traceparent headers: keep the in-process agent from replacing them
        Http = new HttpClient(new UninstrumentedHandler()) { BaseAddress = new Uri($"http://127.0.0.1:{s.HttpPort}") };
    }

    public async Task DisposeAsync()
    {
        Http?.Dispose();
        if (App != null)
        {
            await App.StopAsync();
            await App.DisposeAsync();
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

[CollectionDefinition("sample-app")]
public sealed class SampleAppCollection : ICollectionFixture<AppFixture> { }

[Collection("sample-app")]
public sealed class SampleAppTests
{
    private const int Server = 2;
    private const int Client = 3;

    private readonly AppFixture f;

    public SampleAppTests(AppFixture fixture) => f = fixture;

    private static string Dump(SpanData s) => s.Name + " {" + string.Join(", ", s.Attributes.Select(kv => kv.Key + "=" + kv.Value)) + "}";

    private static string NewTraceId() => ActivityTraceId.CreateRandom().ToHexString();

    private async Task<HttpResponseMessage> Get(string path, string traceId, string flags = "01", string? tracestate = null)
    {
        var req = new HttpRequestMessage(HttpMethod.Get, path);
        req.Headers.TryAddWithoutValidation("traceparent", $"00-{traceId}-{ActivitySpanId.CreateRandom().ToHexString()}-{flags}");
        if (tracestate != null) req.Headers.TryAddWithoutValidation("tracestate", tracestate);
        return await f.Http.SendAsync(req);
    }

    private Task<SpanData> WaitSpan(string traceId, Func<SpanData, bool> match, string what) =>
        f.Capture.WaitFor(c => c.Spans.FirstOrDefault(s => s.TraceId == traceId && match(s)), what);

    [Fact]
    public async Task MinimalApiRouteResourceHeadersAndCorrelatedLogs()
    {
        var tid = NewTraceId();
        var res = await Get("/users/7", tid);
        res.EnsureSuccessStatusCode();
        var span = await WaitSpan(tid, s => s.Kind == Server && s.Resource.ContainsKey("host.id"), "server span of /users/7");
        Assert.Equal("/users/{id:int}", span.Attr("http.route"));
        Assert.Equal("GET /users/{id:int}", span.Name);
        Assert.Equal("200", span.Attr("http.response.status_code"));

        var r = span.Resource;
        Assert.Equal("dotnet-sample", r["service.name"]);
        Assert.Equal("1.2.3", r["service.version"]);
        Assert.Equal("test", r["deployment.environment.name"]);
        Assert.Equal("payments", r["team"]);
        Assert.True(r.TryGetValue("host.id", out var hostId), "the span resource carries no host.id: " + string.Join(", ", r.Keys));
        Assert.Equal(AppFixture.InfraHostId, hostId);
        Assert.Equal("openlog", r["telemetry.distro.name"]);
        Assert.Equal(AgentVersion.Version, r["telemetry.distro.version"]);
        Assert.Equal("dotnet", r["telemetry.sdk.language"]);
        Assert.Equal(".NET", r["process.runtime.name"]);
        Assert.IsType<long>(r["process.pid"]);
        Assert.True(r.ContainsKey("host.name") && r.ContainsKey("os.type") && r.ContainsKey("host.arch"));
        if (File.Exists("/.dockerenv")) Assert.Matches("^[0-9a-f]{64}$", r["container.id"]?.ToString() ?? "");

        var requests = f.Capture.Requests;
        Assert.All(requests, q => Assert.Equal(AppFixture.LicenseKey, q.LicenseKey));
        Assert.Contains(requests, q => q.Path == "/v1/traces" && q.Encoding == "gzip");

        var log = await f.Capture.WaitFor(c => c.Logs.FirstOrDefault(l => l.Body.Contains("user 7 requested") || (l.Attributes.TryGetValue("UserId", out var v) && v?.ToString() == "7" && l.TraceId == tid)), "correlated log");
        Assert.Equal(tid, log.TraceId);
        Assert.Equal(span.SpanId, log.SpanId);
        Assert.Equal("dotnet-sample", log.Resource["service.name"]);
    }

    [Fact]
    public async Task MvcAttributeRoute()
    {
        var tid = NewTraceId();
        (await Get("/orders/5", tid)).EnsureSuccessStatusCode();
        var span = await WaitSpan(tid, s => s.Kind == Server, "MVC server span");
        Assert.Equal("orders/{id:int}", span.Attr("http.route"));
        Assert.Equal("GET orders/{id:int}", span.Name);
    }

    [Fact]
    public async Task SampledRemoteParentPropagatesRandomFlagTraceStateAndWeight()
    {
        var tid = NewTraceId();
        var res = await Get("/outbound", tid, "03", "ot=th:c,congo=t61rcWkgMzE");
        res.EnsureSuccessStatusCode();
        using var doc = JsonDocument.Parse(await res.Content.ReadAsStringAsync());
        var downstream = doc.RootElement.GetProperty("traceparent").GetString()!;
        Assert.Matches($"^00-{tid}-[0-9a-f]{{16}}-03$", downstream);
        Assert.Equal("ot=th:c,congo=t61rcWkgMzE", doc.RootElement.GetProperty("tracestate").GetString());

        var entry = await WaitSpan(tid, s => s.Kind == Server && s.Attr("http.route") == "/outbound", "entry span");
        Assert.Equal("0.25", Convert.ToString(entry.Attributes["sampling.ratio"], System.Globalization.CultureInfo.InvariantCulture));
        Assert.Equal("ot=th:c,congo=t61rcWkgMzE", entry.TraceState);
        var client = await WaitSpan(tid, s => s.Kind == Client, "HttpClient span");
        Assert.Equal(entry.SpanId, client.ParentSpanId);
        Assert.Equal(downstream.Split('-')[2], client.SpanId);
        Assert.Equal(3u, client.Flags & 0xff);
        var inner = await WaitSpan(tid, s => s.Kind == Server && s.Attr("http.route") == "/headers", "downstream server span");
        Assert.Equal(client.SpanId, inner.ParentSpanId);
        // the downstream entry span has a sampled remote parent with ot=th:c: it is weighted too (apm.md §4)
        Assert.Equal("0.25", Convert.ToString(inner.Attributes["sampling.ratio"], System.Globalization.CultureInfo.InvariantCulture));
    }

    [Fact]
    public async Task UnsampledRemoteParentStillPropagatesAndExportsNothing()
    {
        var tid = NewTraceId();
        var res = await Get("/outbound", tid, "02", "ot=rv:00000000000001");
        res.EnsureSuccessStatusCode();
        using var doc = JsonDocument.Parse(await res.Content.ReadAsStringAsync());
        Assert.Matches($"^00-{tid}-[0-9a-f]{{16}}-02$", doc.RootElement.GetProperty("traceparent").GetString()!);
        Assert.Equal("ot=rv:00000000000001", doc.RootElement.GetProperty("tracestate").GetString());
        // a sampled request afterwards is exported, the unsampled trace is not
        var tid2 = NewTraceId();
        (await Get("/users/1", tid2)).EnsureSuccessStatusCode();
        await WaitSpan(tid2, s => s.Kind == Server, "later sampled span");
        await Task.Delay(500);
        Assert.DoesNotContain(f.Capture.Spans, s => s.TraceId == tid);
    }

    [Fact]
    public async Task RootRequestsGetTheRandomFlag()
    {
        var res = await f.Http.GetAsync("/outbound");
        res.EnsureSuccessStatusCode();
        using var doc = JsonDocument.Parse(await res.Content.ReadAsStringAsync());
        var tp = doc.RootElement.GetProperty("traceparent").GetString()!;
        Assert.EndsWith("-03", tp);
        var tid = tp.Split('-')[1];
        var entry = await WaitSpan(tid, s => s.Kind == Server && s.Attr("http.route") == "/outbound", "root entry span");
        Assert.Equal("", entry.ParentSpanId);
        Assert.Equal(3u, entry.Flags & 0xff);
    }

    [Fact]
    public async Task GrpcServerAndClient()
    {
        var tid = NewTraceId();
        var res = await Get("/grpc", tid);
        res.EnsureSuccessStatusCode();
        Assert.Equal("Hello openlog", await res.Content.ReadAsStringAsync());
        var server = await WaitSpan(tid, s => s.Kind == Server && s.Name.Contains("greet.Greeter/SayHello"), "gRPC server span");
        var dump = Dump(server);
        // OpenTelemetry semantic conventions 1.38+ names (ASP.NET Core instrumentation 1.18)
        Assert.True(server.Attr("rpc.system.name") == "grpc", dump);
        Assert.True(server.Attr("rpc.method") == "greet.Greeter/SayHello", dump);
        Assert.True(server.Attr("rpc.response.status_code") == "OK", dump);
        Assert.True(server.Attr("http.route") == "/greet.Greeter/SayHello", dump);
        var client = await WaitSpan(tid, s => s.Kind == Client && s.SpanId == server.ParentSpanId, "gRPC client span");
        Assert.Contains("greet.Greeter/SayHello", client.Attr("url.full") ?? client.Name);
        var greeting = await f.Capture.WaitFor(c => c.Logs.FirstOrDefault(l => l.TraceId == tid && l.Body.Contains("greeting")), "gRPC handler log");
        Assert.Equal(server.SpanId, greeting.SpanId);
    }

    [Fact]
    public async Task UnhandledExceptionMarksTheEntrySpan()
    {
        var tid = NewTraceId();
        var res = await Get("/error", tid);
        Assert.Equal(HttpStatusCode.InternalServerError, res.StatusCode);
        var span = await WaitSpan(tid, s => s.Kind == Server && s.Attr("http.route") == "/error", "error span");
        Assert.Equal(2, span.StatusCode);
        var ev = Assert.Single(span.Events, e => e.Name == "exception");
        Assert.Equal("System.InvalidOperationException", ev.Attributes["exception.type"]);
        Assert.Contains("boom from sample app", ev.Attributes["exception.message"]?.ToString());
    }

    [Fact]
    public async Task RuntimeProcessAndHttpMetrics()
    {
        (await f.Http.GetAsync("/users/3")).EnsureSuccessStatusCode();
        var names = await f.Capture.WaitFor(c =>
        {
            var set = c.Metrics.Select(m => m.Name).ToHashSet();
            // The resource is enriched from the infra agent's host-id file, which the first exported batch can
            // predate. Waiting only for the names let the assertions below read such a batch and throw
            // KeyNotFoundException on host.id; wait for the metric the assertions actually use.
            var cpuReady = c.Metrics.Any(m => m.Name == "process.cpu.time" && m.Resource.ContainsKey("host.id"));
            return cpuReady
                && set.Contains("http.server.request.duration") && set.Contains("process.cpu.time") && set.Contains("process.memory.usage")
                && set.Any(n => n.StartsWith("dotnet.", StringComparison.Ordinal) || n.StartsWith("process.runtime.dotnet.", StringComparison.Ordinal))
                ? set
                : null;
        }, "runtime/process/http metrics");
        Assert.True(names.Contains("dotnet.gc.collections") || names.Contains("process.runtime.dotnet.gc.collections.count"), string.Join(", ", names.OrderBy(n => n)));
        var cpu = f.Capture.Metrics.Last(m => m.Name == "process.cpu.time" && m.Resource.ContainsKey("host.id"));
        Assert.Equal("s", cpu.Unit);
        Assert.Contains(cpu.Points, p => p.Attributes.TryGetValue("cpu.mode", out var mode) && (string?)mode == "user");
        Assert.True(cpu.Resource.TryGetValue("host.id", out var hostId), "process.cpu.time carries no host.id: " + string.Join(", ", cpu.Resource.Keys));
        Assert.Equal(AppFixture.InfraHostId, hostId);
        var http = f.Capture.Metrics.Last(m => m.Name == "http.server.request.duration");
        Assert.Equal("histogram", http.Type);
        Assert.Contains(http.Points, p => (string?)p.Attributes.GetValueOrDefault("http.route") == "/users/{id:int}");
    }

    [Fact]
    public async Task IngestOutageNeverBreaksTheApplication()
    {
        f.Capture.Status = 503;
        try
        {
            for (var i = 0; i < 20; i++) (await f.Http.GetAsync("/users/" + i)).EnsureSuccessStatusCode();
        }
        finally
        {
            f.Capture.Status = 200;
        }
        var tid = NewTraceId();
        (await Get("/users/99", tid)).EnsureSuccessStatusCode();
        await WaitSpan(tid, s => s.Kind == Server, "span after the outage");
    }

    [DbFact("PG_CONNECTION")]
    public async Task NpgsqlStatementIsSanitized()
    {
        var tid = NewTraceId();
        var res = await Get("/db/pg", tid);
        Assert.Equal("42", await res.Content.ReadAsStringAsync());
        var span = await WaitSpan(tid, s => s.Kind == Client && (s.Attr("db.system") ?? s.Attr("db.system.name")) == "postgresql", "Npgsql span");
        var text = span.Attr("db.query.text") ?? span.Attr("db.statement");
        Assert.Equal("SELECT ? AS answer WHERE ? = ? AND ? = ?", text);
        Assert.DoesNotContain(span.Attributes.Values, v => v?.ToString()?.Contains("secret") == true);
    }

    [DbFact("PG_CONNECTION")]
    public async Task EfCoreQueriesAreTracedThroughTheProvider()
    {
        var tid = NewTraceId();
        var res = await Get("/db/ef", tid);
        res.EnsureSuccessStatusCode();
        Assert.Contains("keyboard", await res.Content.ReadAsStringAsync());
        var select = await WaitSpan(tid, s => s.Kind == Client && (s.Attr("db.query.text") ?? s.Attr("db.statement") ?? "").Contains("FROM \"Products\""), "EF Core SELECT span");
        var text = select.Attr("db.query.text") ?? select.Attr("db.statement")!;
        Assert.DoesNotContain("secret-name", text);
        Assert.DoesNotContain("10.5", text);
        Assert.Equal("postgresql", select.Attr("db.system") ?? select.Attr("db.system.name"));
    }

    [DbFact("MYSQL_CONNECTION")]
    public async Task MySqlConnectorStatementIsSanitizedWithMysqlRules()
    {
        var tid = NewTraceId();
        var res = await Get("/db/mysql", tid);
        Assert.Equal("7", await res.Content.ReadAsStringAsync());
        var span = await WaitSpan(tid, s => s.Kind == Client && (s.Attr("db.system") ?? s.Attr("db.system.name")) == "mysql" && (s.Attr("db.query.text") ?? s.Attr("db.statement")) != null, "MySqlConnector command span");
        Assert.True((span.Attr("db.query.text") ?? span.Attr("db.statement")) == "SELECT ? FROM DUAL WHERE ? = ? AND ? IN (?)", Dump(span));
    }

    [DbFact("REDIS_CONNECTION")]
    public async Task RedisCommandsCarryNoValues()
    {
        var tid = NewTraceId();
        var res = await Get("/db/redis", tid);
        Assert.Equal("secret-value", await res.Content.ReadAsStringAsync());
        var spans = await f.Capture.WaitFor(c =>
        {
            var l = c.Spans.Where(s => s.TraceId == tid && (s.Attr("db.system") ?? s.Attr("db.system.name")) == "redis").ToList();
            return l.Count >= 2 ? l : null;
        }, "two redis spans");
        foreach (var s in spans)
        {
            Assert.DoesNotContain(s.Attributes.Values, v => v?.ToString()?.Contains("secret-value") == true || v?.ToString()?.Contains("product:42") == true);
        }
        Assert.Contains(spans, s => (s.Attr("db.query.text") ?? s.Attr("db.statement") ?? s.Name).StartsWith("SET", StringComparison.Ordinal));
    }
}

/// <summary>OpenLogAgent.Start for applications without a host (console apps).</summary>
[Collection("sample-app")]
public sealed class ConsoleAgentTests
{
    private readonly AppFixture f;

    public ConsoleAgentTests(AppFixture fixture) => f = fixture;

    [Fact]
    public async Task StartExportsCustomSpansAndLogsAndFlushesOnDispose()
    {
        var sourceName = "OpenLog.ConsoleTest." + Guid.NewGuid().ToString("N");
        using var source = new ActivitySource(sourceName);
        var capture = f.Capture;
        string traceId;
        using (var agent = OpenLogAgent.Start(o =>
        {
            o.Endpoint = capture.Endpoint;
            o.LicenseKey = AppFixture.LicenseKey;
            o.ServiceName = "dotnet-console";
            o.RuntimeMetrics = false;
            o.ShutdownOnExit = false;
            o.ActivitySources = new[] { sourceName };
            o.DisabledInstrumentations = Instrumentations.All.ToList();
        }))
        {
            Assert.Same(agent, OpenLogAgent.Current);
            Assert.Throws<InvalidOperationException>(() => OpenLogAgent.Start());
            var logger = agent.LoggerFactory.CreateLogger("console");
            Activity.Current = null;
            using (var a = source.StartActivity("job", ActivityKind.Internal))
            {
                Assert.NotNull(a);
                traceId = a!.TraceId.ToHexString();
                a.SetTag("db.system.name", "postgresql");
                a.SetTag("db.query.text", "DELETE FROM t WHERE id = 5");
                logger.LogWarning("job {JobId} done", 17);
            }
        }
        Assert.Null(OpenLogAgent.Current);
        var span = await capture.WaitFor(c => c.Spans.FirstOrDefault(s => s.TraceId == traceId), "console span");
        Assert.Equal("job", span.Name);
        Assert.Equal("DELETE FROM t WHERE id = ?", span.Attr("db.query.text"));
        Assert.Equal("dotnet-console", span.Resource["service.name"]);
        var log = await capture.WaitFor(c => c.Logs.FirstOrDefault(l => l.TraceId == traceId), "console log");
        Assert.Equal(span.SpanId, log.SpanId);
        Assert.Equal("dotnet-console", log.Resource["service.name"]);
    }
}

using System;
using System.Collections;
using System.Collections.Generic;
using System.Diagnostics;
using System.Globalization;
using System.Linq;
using System.Net.Http;
using System.Threading.Tasks;
using OpenLog.Agent.IntegrationTests;

namespace OpenLog.NetFx.SmokeTest;

/// <summary>
/// Drives the ASP.NET 4.x sample (samples/OpenLog.AspNetFramework.Sample) hosted by IIS Express / IIS and asserts the
/// spans its OpenLog.Agent exports to an in-process OTLP capture server on a fixed port (the site's web.config points
/// OPENLOG_ENDPOINT there). Exit code 0 = PASS, 1 = FAIL, 2 = usage error.
/// </summary>
internal static class Program
{
    private const int KindServer = 2;

    private static readonly List<(bool Ok, string Name, string Detail)> Results = new();

    private static async Task<int> Main(string[] args)
    {
        Options o;
        try
        {
            o = Options.Parse(args);
        }
        catch (ArgumentException e)
        {
            Console.Error.WriteLine("usage error: " + e.Message);
            Console.Error.WriteLine(Options.Usage);
            return 2;
        }

        Log($"site {o.Site}, capture port {o.CapturePort}, service.name {o.ServiceName}, relaxed={o.Relaxed}");
        OtlpCaptureServer capture;
        try
        {
            capture = await OtlpCaptureServer.StartAsync(o.CapturePort);
        }
        catch (Exception e)
        {
            Check("OTLP capture server listens on 127.0.0.1:" + o.CapturePort, false, e.GetType().Name + ": " + e.Message);
            return Finish();
        }
        Log("OTLP capture server listening at " + capture.Endpoint);

        await using (capture)
        {
            using var http = new HttpClient(new UninstrumentedHandler()) { Timeout = TimeSpan.FromSeconds(60) };
            try
            {
                await Run(o, capture, http);
            }
            catch (Exception e)
            {
                Check("smoke test ran to completion", false, e.ToString());
            }
            DumpCapture(capture);
        }
        return Finish();
    }

    private static async Task Run(Options o, OtlpCaptureServer capture, HttpClient http)
    {
        // 1. readiness: the first request starts the application (Application_Start starts the agent)
        Log($"waiting up to {o.ReadyTimeout.TotalSeconds:0}s for {o.Site}/users?id=0 to answer 200");
        var sw = Stopwatch.StartNew();
        var ready = false;
        var lastReport = "";
        var attempt = 0;
        while (sw.Elapsed < o.ReadyTimeout)
        {
            attempt++;
            string report;
            try
            {
                var (status, body) = await Get(http, o.Site + "/users?id=0", null);
                if (status == 200 && body.Contains("user 0", StringComparison.Ordinal))
                {
                    ready = true;
                    Log($"site ready after {sw.Elapsed.TotalSeconds:0.0}s ({attempt} attempts)");
                    break;
                }
                report = $"HTTP {status}: {Truncate(body, 2000)}";
            }
            catch (Exception e)
            {
                report = e.GetType().Name + ": " + (e.InnerException?.Message ?? e.Message);
            }
            if (report != lastReport)
            {
                Log($"not ready yet (attempt {attempt}, {sw.Elapsed.TotalSeconds:0}s): {report}");
                lastReport = report;
            }
            await Task.Delay(TimeSpan.FromSeconds(2));
        }
        Check("site answers GET /users?id=0 with 200 \"user 0\"", ready, ready ? "" : $"timed out after {o.ReadyTimeout.TotalSeconds:0}s; last: {lastReport}");
        if (!ready) return;

        // 2. a request continuing an incoming W3C trace
        var traceId = ActivityTraceId.CreateRandom().ToHexString();
        var parentSpanId = ActivitySpanId.CreateRandom().ToHexString();
        var traceparent = $"00-{traceId}-{parentSpanId}-01";
        Log("GET /users?id=42 with traceparent " + traceparent);
        var (s42, b42) = await Get(http, o.Site + "/users?id=42", traceparent);
        Check("GET /users?id=42 answers 200 \"user 42\"", s42 == 200 && b42.Contains("user 42", StringComparison.Ordinal), $"HTTP {s42}: {Truncate(b42, 500)}");

        // 3. plain requests (new root traces)
        const int plain = 3;
        for (var i = 1; i <= plain; i++)
        {
            var (s, b) = await Get(http, o.Site + "/users?id=" + (100 + i), null);
            Check($"GET /users?id={100 + i} answers 200", s == 200, $"HTTP {s}: {Truncate(b, 300)}");
        }

        // 4. a missing page
        var (s404, b404) = await Get(http, o.Site + "/openlog-missing-page", null);
        Check("GET /openlog-missing-page answers 404", s404 == 404, $"HTTP {s404}: {Truncate(b404, 300)}");

        // 5. spans (batch export every 5s)
        Log($"waiting up to {o.SpanTimeout.TotalSeconds:0}s for the SERVER span of trace {traceId}");
        SpanData? traced = null;
        try
        {
            traced = await capture.WaitFor(c => c.Spans.FirstOrDefault(sp => sp.TraceId == traceId && sp.Kind == KindServer), "SERVER span of the traced request", (int)o.SpanTimeout.TotalMilliseconds);
        }
        catch (TimeoutException e)
        {
            Log(e.Message);
        }
        Check("SERVER span (kind 2) exported for the incoming trace " + traceId, traced != null, traced == null ? "not received; see the captured spans below" : "");

        List<SpanData> roots = new();
        try
        {
            roots = await capture.WaitFor(c =>
            {
                var r = c.Spans.Where(sp => sp.Kind == KindServer && sp.TraceId != traceId && sp.ParentSpanId.Length == 0 && PathOf(sp) == "/users").ToList();
                return r.Count >= plain + 1 ? r : null;
            }, "root SERVER spans of the plain requests", 30_000);
        }
        catch (TimeoutException e)
        {
            Log(e.Message);
        }
        Check($"root SERVER spans for the readiness + {plain} plain /users requests (>= {plain + 1})", roots.Count >= plain + 1, "got " + roots.Count);

        if (traced != null)
        {
            Check("span continues the incoming trace: parent span id = " + parentSpanId, traced.ParentSpanId == parentSpanId, "parent span id " + Show(traced.ParentSpanId));
            var path = PathOf(traced);
            Check("url.path is /users", path == "/users", $"path {Show(path)} (url.path={Show(traced.Attr("url.path"))}, url.full={Show(traced.Attr("url.full"))}, http.target={Show(traced.Attr("http.target"))})");
            var status = traced.Attr("http.response.status_code") ?? traced.Attr("http.status_code");
            Check("http.response.status_code is 200", status == "200", "got " + Show(status));
            var method = traced.Attr("http.request.method") ?? traced.Attr("http.method");
            Check("http.request.method is GET", method == "GET", "got " + Show(method));
            Check("span status is not error", traced.StatusCode != 2, "status code " + traced.StatusCode);
            Check("instrumentation scope is the ASP.NET instrumentation", traced.Scope.Name.Contains("AspNet", StringComparison.Ordinal), "scope " + Show(traced.Scope.Name));
            if (!o.Relaxed)
            {
                Check("scope is ASP.NET 4.x, not ASP.NET Core", !traced.Scope.Name.Contains("AspNetCore", StringComparison.Ordinal), "scope " + Show(traced.Scope.Name));
            }

            var r = traced.Resource;
            Check("resource service.name is " + o.ServiceName, Str(r, "service.name") == o.ServiceName, "got " + Show(Str(r, "service.name")));
            Check("resource telemetry.distro.name is openlog", Str(r, "telemetry.distro.name") == "openlog", "got " + Show(Str(r, "telemetry.distro.name")));
            Check("resource telemetry.distro.version is set", !string.IsNullOrEmpty(Str(r, "telemetry.distro.version")), "missing");
            var runtime = Str(r, "process.runtime.description");
            if (!o.Relaxed)
            {
                Check("resource process.runtime.description is .NET Framework", runtime != null && runtime.StartsWith(".NET Framework", StringComparison.Ordinal), "got " + Show(runtime));
            }
            Log($"info: process.executable.name={Show(Str(r, "process.executable.name"))} process.runtime.description={Show(runtime)} host.id={Show(Str(r, "host.id"))} os.type={Show(Str(r, "os.type"))}");
        }

        var missing = capture.Spans.Where(sp => sp.Kind == KindServer && PathOf(sp) == "/openlog-missing-page").ToList();
        Log(missing.Count > 0
            ? $"info: 404 request has a SERVER span (status code attribute {Show(missing[0].Attr("http.response.status_code") ?? missing[0].Attr("http.status_code"))})"
            : "info: no SERVER span for the 404 request (the module only runs for managed handlers; not asserted)");

        // 6. export requests
        var traceRequests = capture.Requests.Where(q => q.Path == "/v1/traces").ToList();
        Check("trace export requests received", traceRequests.Count > 0, "none");
        var badKeys = traceRequests.Where(q => q.LicenseKey != o.LicenseKey).Select(q => Show(q.LicenseKey)).Distinct().ToList();
        Check("every trace export carries header openlog-license-key: " + o.LicenseKey, traceRequests.Count > 0 && badKeys.Count == 0, "other values: " + string.Join(", ", badKeys));
        foreach (var g in capture.Requests.GroupBy(q => (q.Path, q.Encoding, q.UserAgent)))
        {
            Log($"info: {g.Count()} export request(s) {g.Key.Path} encoding={Show(g.Key.Encoding)} user-agent={Show(g.Key.UserAgent)}");
        }
    }

    private static async Task<(int Status, string Body)> Get(HttpClient http, string url, string? traceparent)
    {
        using var req = new HttpRequestMessage(HttpMethod.Get, url);
        if (traceparent != null) req.Headers.TryAddWithoutValidation("traceparent", traceparent);
        using var resp = await http.SendAsync(req);
        var body = await resp.Content.ReadAsStringAsync();
        return ((int)resp.StatusCode, body);
    }

    /// <summary>The request path from url.path, else url.full / http.url / http.target (older semantic conventions).</summary>
    private static string? PathOf(SpanData s)
    {
        var p = s.Attr("url.path");
        if (p != null) return p;
        var full = s.Attr("url.full") ?? s.Attr("http.url");
        if (full != null && Uri.TryCreate(full, UriKind.Absolute, out var u)) return u.AbsolutePath;
        var target = s.Attr("http.target");
        if (target != null)
        {
            var q = target.IndexOf('?');
            return q < 0 ? target : target.Substring(0, q);
        }
        return null;
    }

    private static string? Str(Dictionary<string, object?> d, string k) => d.TryGetValue(k, out var v) ? v?.ToString() : null;

    private static string Show(string? v) => v == null ? "<missing>" : "\"" + v + "\"";

    private static string Truncate(string s, int max)
    {
        var flat = s.Replace("\r", " ").Replace("\n", " ");
        return flat.Length <= max ? flat : flat.Substring(0, max) + "…";
    }

    private static string Format(object? v) => v switch
    {
        null => "null",
        string s => "\"" + s + "\"",
        bool b => b ? "true" : "false",
        double d => d.ToString(CultureInfo.InvariantCulture),
        byte[] bytes => "0x" + Otlp.Hex(bytes),
        IDictionary<string, object?> kv => "{" + string.Join(", ", kv.Select(e => e.Key + "=" + Format(e.Value))) + "}",
        IEnumerable list => "[" + string.Join(", ", list.Cast<object?>().Select(Format)) + "]",
        _ => Convert.ToString(v, CultureInfo.InvariantCulture) ?? "",
    };

    private static void DumpCapture(OtlpCaptureServer capture)
    {
        var spans = capture.Spans.ToList();
        Console.WriteLine();
        Console.WriteLine($"---- captured spans ({spans.Count}) ----");
        var i = 0;
        foreach (var s in spans)
        {
            Console.WriteLine($"[{++i}] name=\"{s.Name}\" kind={s.Kind} status={s.StatusCode} scope={s.Scope.Name}@{s.Scope.Version}");
            Console.WriteLine($"    trace={s.TraceId} span={s.SpanId} parent={(s.ParentSpanId.Length == 0 ? "<root>" : s.ParentSpanId)} flags={s.Flags} tracestate=\"{s.TraceState}\"");
            foreach (var a in s.Attributes.OrderBy(a => a.Key, StringComparer.Ordinal)) Console.WriteLine($"    {a.Key} = {Format(a.Value)}");
            foreach (var e in s.Events) Console.WriteLine($"    event {e.Name} {Format(e.Attributes)}");
        }
        var resources = spans.Select(s => s.Resource).Distinct().ToList();
        Console.WriteLine($"---- span resources ({resources.Count}) ----");
        foreach (var r in resources)
        {
            foreach (var a in r.OrderBy(a => a.Key, StringComparer.Ordinal)) Console.WriteLine($"    {a.Key} = {Format(a.Value)}");
            Console.WriteLine("    --");
        }
        Console.WriteLine($"---- export requests ({capture.Requests.Count}); metrics: {string.Join(", ", capture.Metrics.Select(m => m.Name).Distinct().OrderBy(n => n, StringComparer.Ordinal))} ----");
        Console.WriteLine();
    }

    private static void Check(string name, bool ok, string detail)
    {
        Results.Add((ok, name, detail));
        Console.WriteLine(ok ? $"PASS: {name}" : $"FAIL: {name} -- {detail}");
    }

    private static int Finish()
    {
        var failed = Results.Count(r => !r.Ok);
        Console.WriteLine();
        if (failed > 0) Console.WriteLine("failed checks:");
        foreach (var r in Results.Where(r => !r.Ok)) Console.WriteLine($"  FAIL: {r.Name} -- {r.Detail}");
        if (failed == 0 && Results.Count > 0)
        {
            Console.WriteLine($"RESULT: PASS ({Results.Count} checks)");
            return 0;
        }
        Console.WriteLine($"RESULT: FAIL ({failed} of {Results.Count} checks failed)");
        return 1;
    }

    private static void Log(string message) => Console.WriteLine($"[smoke {DateTime.Now:HH:mm:ss}] {message}");

    private sealed class Options
    {
        public const string Usage = "usage: OpenLog.NetFx.SmokeTest --site http://localhost:8085 --capture-port 4319 --license-key KEY [--service-name legacy-web] [--ready-timeout 240] [--span-timeout 90] [--relaxed]";

        public string Site = "";
        public int CapturePort;
        public string LicenseKey = "";
        public string ServiceName = "legacy-web";
        public TimeSpan ReadyTimeout = TimeSpan.FromSeconds(240);
        public TimeSpan SpanTimeout = TimeSpan.FromSeconds(90);
        /// <summary>For a stand-in site (ASP.NET Core): no .NET Framework runtime / ASP.NET 4.x scope checks.</summary>
        public bool Relaxed;

        public static Options Parse(string[] args)
        {
            var o = new Options();
            for (var i = 0; i < args.Length; i++)
            {
                string Next() => i + 1 < args.Length ? args[++i] : throw new ArgumentException(args[i] + " needs a value");
                switch (args[i])
                {
                    case "--site": o.Site = Next().TrimEnd('/'); break;
                    case "--capture-port": o.CapturePort = Int(Next(), "--capture-port"); break;
                    case "--license-key": o.LicenseKey = Next(); break;
                    case "--service-name": o.ServiceName = Next(); break;
                    case "--ready-timeout": o.ReadyTimeout = TimeSpan.FromSeconds(Int(Next(), "--ready-timeout")); break;
                    case "--span-timeout": o.SpanTimeout = TimeSpan.FromSeconds(Int(Next(), "--span-timeout")); break;
                    case "--relaxed": o.Relaxed = true; break;
                    default: throw new ArgumentException("unknown argument " + args[i]);
                }
            }
            if (!Uri.TryCreate(o.Site, UriKind.Absolute, out _)) throw new ArgumentException("--site must be an absolute URL");
            if (o.CapturePort <= 0 || o.CapturePort > 65535) throw new ArgumentException("--capture-port must be a port number");
            if (o.LicenseKey.Length == 0) throw new ArgumentException("--license-key is required");
            return o;
        }

        private static int Int(string v, string name) =>
            int.TryParse(v, NumberStyles.None, CultureInfo.InvariantCulture, out var n) ? n : throw new ArgumentException(name + " must be a number");
    }
}

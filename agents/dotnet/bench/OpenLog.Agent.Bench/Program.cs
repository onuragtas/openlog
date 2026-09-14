using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Globalization;
using System.IO;
using System.Linq;
using System.Net;
using System.Net.Http;
using System.Net.Sockets;
using System.Reflection;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.AspNetCore.Builder;
using Microsoft.AspNetCore.Hosting;
using Microsoft.AspNetCore.Http;
using Microsoft.Extensions.Logging;
using OpenLog.SampleApp;

// Overhead benchmark (make bench): requests per second, latency and RSS of the sample app's /users/{id} route
//   none      - agent not registered
//   sampled   - agent, OPENLOG_SAMPLING_RATIO=1 (server span, ILogger → OTLP log, HTTP + runtime metrics, export)
//   unsampled - agent, OPENLOG_SAMPLING_RATIO=0 (propagation only, logs and metrics still exported)
//   sampled-nologs - agent, ratio 1, OPENLOG_LOGS_EXPORT=false (spans + metrics; the route's ILogger call is not exported)
// Each scenario runs in its own process exporting to a local OTLP sink in this process.
//   BENCH_SECONDS=8 BENCH_CONCURRENCY=16 BENCH_ROUNDS=3 BENCH_WARMUP=3

if (args.Length > 0 && args[0] == "serve") return await Serve(args);
return await RunAll();

static int Env(string name, int def) => int.TryParse(Environment.GetEnvironmentVariable(name), out var v) ? v : def;

static int FreePort()
{
    var l = new TcpListener(IPAddress.Loopback, 0);
    l.Start();
    var p = ((IPEndPoint)l.LocalEndpoint).Port;
    l.Stop();
    return p;
}

static async Task<int> Serve(string[] args)
{
    var scenario = args[1];
    var port = int.Parse(args[2], CultureInfo.InvariantCulture);
    var endpoint = args[3];
    var settings = new SampleSettings { HttpPort = port, GrpcPort = 0, Agent = scenario != "none", ConsoleLogs = false };
    var app = SampleApp.Build(settings, o =>
    {
        o.Endpoint = endpoint;
        o.LicenseKey = "bench";
        o.ServiceName = "dotnet-bench";
        o.SamplingRatio = scenario.StartsWith("sampled", StringComparison.Ordinal) ? 1 : 0;
        o.LogsExport = !scenario.EndsWith("-nologs", StringComparison.Ordinal);
        o.LogLevel = "error";
    });
    await app.StartAsync();
    Console.WriteLine("ready");
    await Task.Delay(Timeout.Infinite);
    return 0;
}

static async Task<int> RunAll()
{
    var seconds = Env("BENCH_SECONDS", 8);
    var concurrency = Env("BENCH_CONCURRENCY", 16);
    var rounds = Env("BENCH_ROUNDS", 3);
    var warmup = Env("BENCH_WARMUP", 3);

    long exportedBytes = 0;
    var sinkBuilder = WebApplication.CreateSlimBuilder();
    sinkBuilder.Logging.ClearProviders();
    var sinkPort = FreePort();
    sinkBuilder.WebHost.UseKestrel(k => k.Listen(IPAddress.Loopback, sinkPort));
    var sink = sinkBuilder.Build();
    sink.MapPost("/v1/{signal}", async (HttpContext ctx) =>
    {
        using var ms = new MemoryStream();
        await ctx.Request.Body.CopyToAsync(ms);
        Interlocked.Add(ref exportedBytes, ms.Length);
        return Results.Bytes(Array.Empty<byte>(), "application/x-protobuf");
    });
    await sink.StartAsync();

    Console.WriteLine($"# .NET {Environment.Version} · {Environment.ProcessorCount} CPUs · {concurrency} connections · {seconds} s × {rounds} rounds (median) · warmup {warmup} s");
    Console.WriteLine("| Scenario | req/s | vs. no agent | p50 ms | p99 ms | RSS MB |");
    Console.WriteLine("|---|---|---|---|---|---|");
    double? baseline = null;
    foreach (var scenario in new[] { "none", "sampled", "unsampled", "sampled-nologs" })
    {
        var port = FreePort();
        var self = Assembly.GetExecutingAssembly().Location;
        var psi = new ProcessStartInfo(Environment.ProcessPath!, $"\"{self}\" serve {scenario} {port} http://127.0.0.1:{sinkPort}")
        {
            RedirectStandardOutput = true,
            UseShellExecute = false,
        };
        psi.Environment["DOTNET_gcServer"] = "1";
        psi.Environment["ASPNETCORE_ENVIRONMENT"] = "Production";
        using var proc = Process.Start(psi)!;
        var line = await proc.StandardOutput.ReadLineAsync();
        if (line != "ready") throw new InvalidOperationException($"{scenario}: server did not start ({line})");
        _ = proc.StandardOutput.ReadToEndAsync();

        using var handler = new SocketsHttpHandler { MaxConnectionsPerServer = concurrency, ActivityHeadersPropagator = null };
        using var client = new HttpClient(handler) { BaseAddress = new Uri($"http://127.0.0.1:{port}") };
        await Load(client, concurrency, warmup);
        var results = new List<(double Rps, double P50, double P99)>();
        for (var r = 0; r < rounds; r++) results.Add(await Load(client, concurrency, seconds));
        var mid = results.OrderBy(x => x.Rps).ElementAt(results.Count / 2);
        var rss = Rss(proc.Id);
        proc.Kill(true);
        await proc.WaitForExitAsync();
        baseline ??= mid.Rps;
        var delta = scenario == "none" ? "—" : ((mid.Rps / baseline.Value - 1) * 100).ToString("+0.0;−0.0", CultureInfo.InvariantCulture) + " %";
        Console.WriteLine(string.Create(CultureInfo.InvariantCulture, $"| {scenario} | {mid.Rps:0} | {delta} | {mid.P50:0.00} | {mid.P99:0.00} | {rss:0} |"));
    }
    Console.WriteLine(string.Create(CultureInfo.InvariantCulture, $"# OTLP bytes received by the sink: {exportedBytes / 1024.0 / 1024.0:0.0} MB"));
    await sink.StopAsync();
    return 0;
}

static async Task<(double Rps, double P50, double P99)> Load(HttpClient client, int concurrency, int seconds)
{
    var latencies = new List<double>[concurrency];
    var end = Stopwatch.GetTimestamp() + (long)(seconds * (double)Stopwatch.Frequency);
    var workers = Enumerable.Range(0, concurrency).Select(async i =>
    {
        var l = latencies[i] = new List<double>(1 << 16);
        while (Stopwatch.GetTimestamp() < end)
        {
            var t0 = Stopwatch.GetTimestamp();
            using var res = await client.GetAsync("/users/42", HttpCompletionOption.ResponseContentRead);
            res.EnsureSuccessStatusCode();
            l.Add((Stopwatch.GetTimestamp() - t0) * 1000.0 / Stopwatch.Frequency);
        }
    }).ToArray();
    await Task.WhenAll(workers);
    var all = latencies.SelectMany(x => x).OrderBy(x => x).ToArray();
    return (all.Length / (double)seconds, all[all.Length / 2], all[(int)(all.Length * 0.99)]);
}

static double Rss(int pid)
{
    try
    {
        var line = File.ReadLines($"/proc/{pid}/status").FirstOrDefault(l => l.StartsWith("VmRSS:", StringComparison.Ordinal));
        if (line == null) return 0;
        var kb = double.Parse(line.Split(' ', StringSplitOptions.RemoveEmptyEntries)[1], CultureInfo.InvariantCulture);
        return kb / 1024;
    }
    catch (IOException)
    {
        return 0;
    }
}

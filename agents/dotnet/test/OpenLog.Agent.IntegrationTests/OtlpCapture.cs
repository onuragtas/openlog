using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.IO;
using System.IO.Compression;
using System.Linq;
using System.Net;
using System.Net.Http;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.AspNetCore.Builder;
using Microsoft.AspNetCore.Hosting;
using Microsoft.AspNetCore.Hosting.Server;
using Microsoft.AspNetCore.Hosting.Server.Features;
using Microsoft.AspNetCore.Http;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Logging;

namespace OpenLog.Agent.IntegrationTests;

// OTLP/HTTP capture server for tests: decodes protobuf ExportTraceServiceRequest, ExportMetricsServiceRequest and
// ExportLogsServiceRequest (gzip or plain) with a minimal hand-written decoder of the opentelemetry-proto fields the
// tests assert on (no protobuf dependency), like agents/node/test/helpers/otlp.ts.

public sealed class ProtoReader
{
    private readonly byte[] buf;
    private int pos;
    private readonly int end;

    public ProtoReader(byte[] buf, int offset = 0, int length = -1)
    {
        this.buf = buf;
        pos = offset;
        end = length < 0 ? buf.Length : offset + length;
    }

    public bool Done => pos >= end;

    public ulong Varint()
    {
        ulong result = 0;
        var shift = 0;
        while (true)
        {
            var b = buf[pos++];
            result |= (ulong)(b & 0x7f) << shift;
            if ((b & 0x80) == 0) return result;
            shift += 7;
        }
    }

    public (int Field, int Wire) Tag()
    {
        var t = Varint();
        return ((int)(t >> 3), (int)(t & 7));
    }

    public byte[] Bytes()
    {
        var len = (int)Varint();
        var b = new byte[len];
        Array.Copy(buf, pos, b, 0, len);
        pos += len;
        return b;
    }

    public string String() => Encoding.UTF8.GetString(Bytes());

    public ulong Fixed64()
    {
        var v = BitConverter.ToUInt64(buf, pos);
        pos += 8;
        return v;
    }

    public uint Fixed32()
    {
        var v = BitConverter.ToUInt32(buf, pos);
        pos += 4;
        return v;
    }

    public double Double() => BitConverter.Int64BitsToDouble((long)Fixed64());

    public void Skip(int wire)
    {
        switch (wire)
        {
            case 0: Varint(); break;
            case 1: pos += 8; break;
            case 2: Bytes(); break;
            case 5: pos += 4; break;
            default: throw new InvalidDataException($"unsupported wire type {wire}");
        }
    }

    public static void Decode(byte[] data, Action<ProtoReader, int, int> handler)
    {
        var r = new ProtoReader(data);
        while (!r.Done)
        {
            var (f, w) = r.Tag();
            var before = r.pos;
            handler(r, f, w);
            if (r.pos == before) r.Skip(w);
        }
    }
}

public sealed class Scope
{
    public string Name = "";
    public string Version = "";
}

public sealed class SpanData
{
    public string TraceId = "";
    public string SpanId = "";
    public string ParentSpanId = "";
    public string TraceState = "";
    public string Name = "";
    public int Kind;
    public uint Flags;
    public int StatusCode;
    public Dictionary<string, object?> Attributes = new();
    public List<(string Name, Dictionary<string, object?> Attributes)> Events = new();
    public List<(string TraceId, string SpanId)> Links = new();
    public Dictionary<string, object?> Resource = new();
    public Scope Scope = new();

    public string? Attr(string k) => Attributes.TryGetValue(k, out var v) ? v?.ToString() : null;
    public override string ToString() => $"{Name} kind={Kind} scope={Scope.Name}";
}

public sealed class MetricData
{
    public string Name = "";
    public string Unit = "";
    public string Type = "";
    public List<(Dictionary<string, object?> Attributes, double Value)> Points = new();
    public Dictionary<string, object?> Resource = new();
    public Scope Scope = new();
}

public sealed class LogData
{
    public string Body = "";
    public int SeverityNumber;
    public string SeverityText = "";
    public string TraceId = "";
    public string SpanId = "";
    public Dictionary<string, object?> Attributes = new();
    public Dictionary<string, object?> Resource = new();
    public Scope Scope = new();
}

public static class Otlp
{
    public static string Hex(byte[] b) => string.Concat(b.Select(x => x.ToString("x2")));

    public static object? AnyValue(byte[] data)
    {
        object? v = null;
        ProtoReader.Decode(data, (r, f, w) =>
        {
            switch (f)
            {
                case 1: v = r.String(); break;
                case 2: v = r.Varint() != 0; break;
                case 3: v = (long)r.Varint(); break;
                case 4: v = r.Double(); break;
                case 5:
                    var list = new List<object?>();
                    ProtoReader.Decode(r.Bytes(), (ar, af, aw) =>
                    {
                        if (af == 1) list.Add(AnyValue(ar.Bytes()));
                    });
                    v = list;
                    break;
                case 6:
                    var kv = new Dictionary<string, object?>();
                    ProtoReader.Decode(r.Bytes(), (kr, kf, kw) =>
                    {
                        if (kf == 1) KeyValue(kr.Bytes(), kv);
                    });
                    v = kv;
                    break;
                case 7: v = r.Bytes(); break;
            }
        });
        return v;
    }

    public static void KeyValue(byte[] data, Dictionary<string, object?> into)
    {
        var key = "";
        object? value = null;
        ProtoReader.Decode(data, (r, f, w) =>
        {
            if (f == 1) key = r.String();
            else if (f == 2) value = AnyValue(r.Bytes());
        });
        into[key] = value;
    }

    private static Dictionary<string, object?> Resource(byte[] data)
    {
        var attrs = new Dictionary<string, object?>();
        ProtoReader.Decode(data, (r, f, w) =>
        {
            if (f == 1) KeyValue(r.Bytes(), attrs);
        });
        return attrs;
    }

    private static Scope ScopeOf(byte[] data)
    {
        var s = new Scope();
        ProtoReader.Decode(data, (r, f, w) =>
        {
            if (f == 1) s.Name = r.String();
            else if (f == 2) s.Version = r.String();
        });
        return s;
    }

    public static List<SpanData> DecodeTraces(byte[] body)
    {
        var spans = new List<SpanData>();
        ProtoReader.Decode(body, (r, f, w) =>
        {
            if (f != 1) return;
            var resource = new Dictionary<string, object?>();
            var scopeBlobs = new List<byte[]>();
            ProtoReader.Decode(r.Bytes(), (rr, rf, rw) =>
            {
                if (rf == 1) resource = Resource(rr.Bytes());
                else if (rf == 2) scopeBlobs.Add(rr.Bytes());
            });
            foreach (var blob in scopeBlobs)
            {
                var scope = new Scope();
                var spanBlobs = new List<byte[]>();
                ProtoReader.Decode(blob, (sr, sf, sw) =>
                {
                    if (sf == 1) scope = ScopeOf(sr.Bytes());
                    else if (sf == 2) spanBlobs.Add(sr.Bytes());
                });
                foreach (var sb in spanBlobs)
                {
                    var s = new SpanData { Resource = resource, Scope = scope };
                    ProtoReader.Decode(sb, (pr, pf, pw) =>
                    {
                        switch (pf)
                        {
                            case 1: s.TraceId = Hex(pr.Bytes()); break;
                            case 2: s.SpanId = Hex(pr.Bytes()); break;
                            case 3: s.TraceState = pr.String(); break;
                            case 4: s.ParentSpanId = Hex(pr.Bytes()); break;
                            case 5: s.Name = pr.String(); break;
                            case 6: s.Kind = (int)pr.Varint(); break;
                            case 9: KeyValue(pr.Bytes(), s.Attributes); break;
                            case 11:
                                var evName = "";
                                var evAttrs = new Dictionary<string, object?>();
                                ProtoReader.Decode(pr.Bytes(), (er, ef, ew) =>
                                {
                                    if (ef == 2) evName = er.String();
                                    else if (ef == 3) KeyValue(er.Bytes(), evAttrs);
                                });
                                s.Events.Add((evName, evAttrs));
                                break;
                            case 13:
                                var linkTrace = "";
                                var linkSpan = "";
                                ProtoReader.Decode(pr.Bytes(), (lr, lf, lw) =>
                                {
                                    if (lf == 1) linkTrace = Hex(lr.Bytes());
                                    else if (lf == 2) linkSpan = Hex(lr.Bytes());
                                });
                                s.Links.Add((linkTrace, linkSpan));
                                break;
                            case 15:
                                ProtoReader.Decode(pr.Bytes(), (xr, xf, xw) =>
                                {
                                    if (xf == 3) s.StatusCode = (int)xr.Varint();
                                });
                                break;
                            case 16: s.Flags = pr.Fixed32(); break;
                        }
                    });
                    spans.Add(s);
                }
            }
        });
        return spans;
    }

    public static List<MetricData> DecodeMetrics(byte[] body)
    {
        var metrics = new List<MetricData>();
        ProtoReader.Decode(body, (r, f, w) =>
        {
            if (f != 1) return;
            var resource = new Dictionary<string, object?>();
            var scopeBlobs = new List<byte[]>();
            ProtoReader.Decode(r.Bytes(), (rr, rf, rw) =>
            {
                if (rf == 1) resource = Resource(rr.Bytes());
                else if (rf == 2) scopeBlobs.Add(rr.Bytes());
            });
            foreach (var blob in scopeBlobs)
            {
                var scope = new Scope();
                var metricBlobs = new List<byte[]>();
                ProtoReader.Decode(blob, (sr, sf, sw) =>
                {
                    if (sf == 1) scope = ScopeOf(sr.Bytes());
                    else if (sf == 2) metricBlobs.Add(sr.Bytes());
                });
                foreach (var mb in metricBlobs)
                {
                    var m = new MetricData { Resource = resource, Scope = scope };
                    ProtoReader.Decode(mb, (mr, mf, mw) =>
                    {
                        switch (mf)
                        {
                            case 1: m.Name = mr.String(); break;
                            case 3: m.Unit = mr.String(); break;
                            case 5:
                            case 7:
                                m.Type = mf == 5 ? "gauge" : "sum";
                                ProtoReader.Decode(mr.Bytes(), (dr, df, dw) =>
                                {
                                    if (df == 1) m.Points.Add(NumberPoint(dr.Bytes()));
                                });
                                break;
                            case 9:
                                m.Type = "histogram";
                                ProtoReader.Decode(mr.Bytes(), (dr, df, dw) =>
                                {
                                    if (df == 1) m.Points.Add(HistogramPoint(dr.Bytes()));
                                });
                                break;
                            case 10:
                                m.Type = "exponential_histogram";
                                break;
                        }
                    });
                    metrics.Add(m);
                }
            }
        });
        return metrics;
    }

    private static (Dictionary<string, object?>, double) NumberPoint(byte[] data)
    {
        var attrs = new Dictionary<string, object?>();
        double v = 0;
        ProtoReader.Decode(data, (r, f, w) =>
        {
            if (f == 7) KeyValue(r.Bytes(), attrs);
            else if (f == 4) v = r.Double();
            else if (f == 6) v = (long)r.Fixed64();
        });
        return (attrs, v);
    }

    private static (Dictionary<string, object?>, double) HistogramPoint(byte[] data)
    {
        var attrs = new Dictionary<string, object?>();
        double count = 0;
        ProtoReader.Decode(data, (r, f, w) =>
        {
            if (f == 9) KeyValue(r.Bytes(), attrs);
            else if (f == 4) count = r.Fixed64();
        });
        return (attrs, count);
    }

    public static List<LogData> DecodeLogs(byte[] body)
    {
        var logs = new List<LogData>();
        ProtoReader.Decode(body, (r, f, w) =>
        {
            if (f != 1) return;
            var resource = new Dictionary<string, object?>();
            var scopeBlobs = new List<byte[]>();
            ProtoReader.Decode(r.Bytes(), (rr, rf, rw) =>
            {
                if (rf == 1) resource = Resource(rr.Bytes());
                else if (rf == 2) scopeBlobs.Add(rr.Bytes());
            });
            foreach (var blob in scopeBlobs)
            {
                var scope = new Scope();
                var recordBlobs = new List<byte[]>();
                ProtoReader.Decode(blob, (sr, sf, sw) =>
                {
                    if (sf == 1) scope = ScopeOf(sr.Bytes());
                    else if (sf == 2) recordBlobs.Add(sr.Bytes());
                });
                foreach (var lb in recordBlobs)
                {
                    var l = new LogData { Resource = resource, Scope = scope };
                    ProtoReader.Decode(lb, (lr, lf, lw) =>
                    {
                        switch (lf)
                        {
                            case 2: l.SeverityNumber = (int)lr.Varint(); break;
                            case 3: l.SeverityText = lr.String(); break;
                            case 5: l.Body = AnyValue(lr.Bytes())?.ToString() ?? ""; break;
                            case 6: KeyValue(lr.Bytes(), l.Attributes); break;
                            case 9: l.TraceId = Hex(lr.Bytes()); break;
                            case 10: l.SpanId = Hex(lr.Bytes()); break;
                        }
                    });
                    logs.Add(l);
                }
            }
        });
        return logs;
    }
}

/// <summary>
/// HttpClient handler for test requests: no client span, no header injection by the in-process agent
/// (OpenTelemetry suppression scope for the HttpClient instrumentation, no runtime propagator).
/// </summary>
public sealed class UninstrumentedHandler : DelegatingHandler
{
    public UninstrumentedHandler() : base(new SocketsHttpHandler { ActivityHeadersPropagator = null }) { }

    protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
    {
        using (OpenTelemetry.SuppressInstrumentationScope.Begin())
        {
            return await base.SendAsync(request, cancellationToken).ConfigureAwait(false);
        }
    }
}

/// <summary>In-process OTLP/HTTP receiver (Kestrel on 127.0.0.1, random port).</summary>
public sealed class OtlpCaptureServer : IAsyncDisposable
{
    private readonly WebApplication app;
    private readonly ConcurrentQueue<SpanData> spans = new();
    private readonly ConcurrentQueue<MetricData> metrics = new();
    private readonly ConcurrentQueue<LogData> logs = new();
    private readonly ConcurrentQueue<(string Path, string? LicenseKey, string? Encoding, string? UserAgent)> requests = new();

    private OtlpCaptureServer(WebApplication app)
    {
        this.app = app;
    }

    public string Endpoint { get; private set; } = "";
    /// <summary>HTTP status returned to exporters (tests simulate ingest outages).</summary>
    public int Status { get; set; } = 200;

    public IReadOnlyCollection<SpanData> Spans => spans.ToArray();
    public IReadOnlyCollection<MetricData> Metrics => metrics.ToArray();
    public IReadOnlyCollection<LogData> Logs => logs.ToArray();
    public IReadOnlyCollection<(string Path, string? LicenseKey, string? Encoding, string? UserAgent)> Requests => requests.ToArray();

    /// <param name="port">0: a random free port; a fixed port for out-of-process agents (test/OpenLog.NetFx.SmokeTest).</param>
    public static async Task<OtlpCaptureServer> StartAsync(int port = 0)
    {
        var builder = WebApplication.CreateSlimBuilder();
        builder.Logging.ClearProviders();
        builder.WebHost.UseKestrel(o => o.Listen(IPAddress.Loopback, port));
        var app = builder.Build();
        var server = new OtlpCaptureServer(app);
        app.MapPost("/v1/{signal}", async (HttpContext ctx, string signal) =>
        {
            using var ms = new MemoryStream();
            await ctx.Request.Body.CopyToAsync(ms);
            var body = ms.ToArray();
            var encoding = ctx.Request.Headers.ContentEncoding.ToString();
            if (encoding == "gzip")
            {
                using var gz = new GZipStream(new MemoryStream(body), CompressionMode.Decompress);
                using var outMs = new MemoryStream();
                await gz.CopyToAsync(outMs);
                body = outMs.ToArray();
            }
            server.requests.Enqueue((ctx.Request.Path.Value ?? "", ctx.Request.Headers["openlog-license-key"].FirstOrDefault(), encoding, ctx.Request.Headers.UserAgent.ToString()));
            if (server.Status != 200) return Results.StatusCode(server.Status);
            switch (signal)
            {
                case "traces":
                    foreach (var s in Otlp.DecodeTraces(body)) server.spans.Enqueue(s);
                    break;
                case "metrics":
                    foreach (var m in Otlp.DecodeMetrics(body)) server.metrics.Enqueue(m);
                    break;
                case "logs":
                    foreach (var l in Otlp.DecodeLogs(body)) server.logs.Enqueue(l);
                    break;
                default:
                    return Results.NotFound();
            }
            return Results.Bytes(Array.Empty<byte>(), "application/x-protobuf");
        });
        await app.StartAsync();
        var address = app.Services.GetRequiredService<IServer>().Features.Get<IServerAddressesFeature>()!.Addresses.First();
        server.Endpoint = address.Replace("[::1]", "127.0.0.1");
        return server;
    }

    public async Task<T> WaitFor<T>(Func<OtlpCaptureServer, T?> probe, string what, int timeoutMs = 20_000) where T : class
    {
        var deadline = DateTime.UtcNow.AddMilliseconds(timeoutMs);
        while (true)
        {
            var v = probe(this);
            if (v != null) return v;
            if (DateTime.UtcNow > deadline)
            {
                // Names alone hid the reason: a run timed out with process.cpu.time present but no host.id on
                // its resource, and the message could not tell that from the metric never arriving. Say what
                // each distinct metric's resource actually carried.
                var detail = string.Join("; ", Metrics.GroupBy(m => m.Name).OrderBy(g => g.Key).Select(g =>
                {
                    var withHost = g.Count(m => m.Resource.ContainsKey("host.id"));
                    var keys = string.Join(",", g.Last().Resource.Keys.OrderBy(k => k));
                    return $"{g.Key} x{g.Count()} host.id={withHost}/{g.Count()} scope={g.Last().Scope.Name} resource=[{keys}]";
                }));
                throw new TimeoutException($"timed out waiting for {what}; metrics: {detail}; spans: {string.Join(" | ", Spans.Select(s => s.ToString()))}");
            }
            await Task.Delay(100);
        }
    }

    public async ValueTask DisposeAsync()
    {
        using var cts = new CancellationTokenSource(TimeSpan.FromSeconds(5));
        await app.StopAsync(cts.Token);
        await app.DisposeAsync();
    }
}

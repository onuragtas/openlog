using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Text.Json;
using OpenLog.Agent;
using OpenTelemetry;
using OpenTelemetry.Context.Propagation;
using OpenTelemetry.Trace;
using Xunit;

namespace OpenLog.Agent.Tests;

/// <summary>Tests that replace Activity.TraceIdGenerator or install global listeners run one at a time.</summary>
[CollectionDefinition("global-activity-state", DisableParallelization = true)]
public class GlobalActivityState { }

[Collection("global-activity-state")]
public class SamplerTests
{
    public sealed class FixtureSpan
    {
        public bool Sampled { get; set; }
        public string Traceparent { get; set; } = "";
        public string Tracestate { get; set; } = "";
        public double? SamplingRatio { get; set; }
    }

    public sealed class Incoming
    {
        public string Traceparent { get; set; } = "";
        public string Tracestate { get; set; } = "";
    }

    public sealed class Fixture
    {
        public string Name { get; set; } = "";
        public double Ratio { get; set; }
        public bool WriteRV { get; set; }
        public string TraceId { get; set; } = "";
        public string SpanId { get; set; } = "";
        public string ChildSpanId { get; set; } = "";
        public string? Randomness { get; set; }
        public Incoming? Incoming { get; set; }
        public FixtureSpan Span { get; set; } = new();
        public FixtureSpan Child { get; set; } = new();
        public override string ToString() => Name;
    }

    private static readonly List<Fixture> Fixtures = LoadFixtures();

    private static List<Fixture> LoadFixtures()
    {
        var path = Path.Combine(AppContext.BaseDirectory, "go-sampler-fixtures.json");
        using var doc = JsonDocument.Parse(File.ReadAllText(path));
        var opts = new JsonSerializerOptions { PropertyNameCaseInsensitive = true };
        return doc.RootElement.GetProperty("cases").EnumerateArray().Select(e => e.Deserialize<Fixture>(opts)!).ToList();
    }

    public static IEnumerable<object[]> FixtureNames() => Fixtures.Select(f => new object[] { f.Name });

    [Fact]
    public void FixturesAreLoaded()
    {
        Assert.True(Fixtures.Count >= 15, $"only {Fixtures.Count} fixtures");
    }

    [Theory]
    [InlineData(0.5, "8")]
    [InlineData(0.25, "c")]
    [InlineData(0.125, "e")]
    [InlineData(0.75, "4")]
    [InlineData(0.1, "e6666666666666")]
    [InlineData(0.001, "ffbe76c8b43958")]
    public void ThresholdEncoding(double ratio, string th)
    {
        var s = new ConsistentRatioRootSampler(ratio, false, OtTraceState.DefaultRandomness);
        Assert.Equal(th, s.Th);
        var p = OtTraceState.Probability(new List<KeyValuePair<string, string>> { new("th", th) });
        Assert.NotNull(p);
        Assert.True(Math.Abs(p!.Value - ratio) < 1e-12, $"decoded {p}");
    }

    [Fact]
    public void ThresholdParsing()
    {
        Assert.Equal("0", OtTraceState.EncodeThreshold(0));
        Assert.Equal(0.25, OtTraceState.Probability(new List<KeyValuePair<string, string>> { new("p", "2") }));
        Assert.Equal(0.0, OtTraceState.Probability(new List<KeyValuePair<string, string>> { new("p", "63") }));
        Assert.Null(OtTraceState.Probability(new List<KeyValuePair<string, string>> { new("p", "64") }));
        Assert.Null(OtTraceState.ParseThreshold("xyz"));
        Assert.Null(OtTraceState.ParseThreshold("123456789abcdef"));
        var ot = OtTraceState.Parse("th:c;rv:00000000000001;bad;:x");
        Assert.Equal(new[] { "th:c", "rv:00000000000001" }, ot.Select(f => f.Key + ":" + f.Value));
    }

    [Fact]
    public void WithOtKeepsW3CLimits()
    {
        var members = string.Join(",", Enumerable.Range(0, 32).Select(i => $"v{i}=x"));
        var ts = OtTraceState.WithOt(members, new List<KeyValuePair<string, string>> { new("th", "c") })!;
        var outMembers = ts.Split(',');
        Assert.Equal(32, outMembers.Length);
        Assert.Equal("ot=th:c", outMembers[0]);
        Assert.Equal("v30=x", outMembers[31]);
        var longOt = new List<KeyValuePair<string, string>> { new("th", "c"), new("x", new string('a', 200)), new("y", new string('b', 100)) };
        Assert.Equal("th:c;x:" + new string('a', 200), OtTraceState.GetMember(OtTraceState.WithOt(null, longOt), "ot"));
        Assert.Equal("k=v", OtTraceState.WithOt("ot=th:c,k=v", new List<KeyValuePair<string, string>>()));
        Assert.Equal("k=v", OtTraceState.WithOt("k=v", new List<KeyValuePair<string, string>> { new("x", "a,b") }));
    }

    private static readonly OpenLogTraceContextPropagator Propagator = new();

    private static (string Traceparent, string Tracestate) Headers(ActivityContext ctx)
    {
        var carrier = new Dictionary<string, string>();
        Propagator.Inject(new PropagationContext(ctx, default), carrier, (c, k, v) => c[k] = v);
        return (carrier.TryGetValue("traceparent", out var tp) ? tp : "", carrier.TryGetValue("tracestate", out var ts) ? ts : "");
    }

    [Theory]
    [MemberData(nameof(FixtureNames))]
    public void CrossLanguageFixturesFromTheGoAgent(string name)
    {
        var f = Fixtures.Single(x => x.Name == name);
        var sourceName = "openlog.fixtures." + Guid.NewGuid().ToString("N");
        using var source = new ActivitySource(sourceName);
        var exported = new List<Activity>();
        var rnd = f.Randomness ?? "00000000000000";
        RandomnessSource randomness = () => (Convert.ToUInt64(rnd, 16), rnd);
        var previousGenerator = Activity.TraceIdGenerator;
        Activity.TraceIdGenerator = () => ActivityTraceId.CreateFromString(f.TraceId.AsSpan());
        var previousDefault = Activity.DefaultIdFormat;
        Activity.DefaultIdFormat = ActivityIdFormat.W3C;
        Activity.ForceDefaultIdFormat = true;
        try
        {
            using var randomFlag = new RandomFlagListener();
            using var provider = Sdk.CreateTracerProviderBuilder()
                .AddSource(sourceName)
                .SetSampler(OpenLogSampler.Create(f.Ratio, f.WriteRV, randomness))
                .AddInMemoryExporter(exported)
                .Build();

            ActivityContext parent = default;
            if (f.Incoming != null)
            {
                var carrier = new Dictionary<string, string> { ["traceparent"] = f.Incoming.Traceparent, ["tracestate"] = f.Incoming.Tracestate };
                parent = Propagator.Extract(default, carrier, (c, k) => c.TryGetValue(k, out var v) ? new[] { v } : null).ActivityContext;
            }
            var previousCurrent = Activity.Current;
            Activity.Current = null;
            using var span = source.StartActivity("span", ActivityKind.Server, parent);
            Assert.NotNull(span);
            Activity? child;
            using (child = source.StartActivity("child", ActivityKind.Client))
            {
                if (child != null) Assert.Equal(span!.SpanId, child.ParentSpanId);
            }
            span!.Stop();
            Activity.Current = previousCurrent;

            Check(span, f.Span, "span", exported);
            if (child != null)
            {
                Check(child, f.Child, "child", exported);
            }
            else
            {
                // OpenTelemetry .NET creates no Activity below an unsampled local parent (Go/Node create a non-recording
                // span); outgoing calls then propagate the parent's context, so the child's expected flags and
                // tracestate must be what the parent propagates (only the span id differs, and it is never exported).
                Assert.False(f.Child.Sampled, "a sampled child must be created");
                Check(span, new FixtureSpan { Sampled = false, Traceparent = f.Child.Traceparent, Tracestate = f.Child.Tracestate }, "child (parent context)", exported);
            }
        }
        finally
        {
            Activity.TraceIdGenerator = previousGenerator;
            Activity.DefaultIdFormat = previousDefault;
        }
    }

    private static void Check(Activity a, FixtureSpan want, string which, List<Activity> exported)
    {
        var (tp, ts) = Headers(a.Context);
        // .NET cannot fix span ids; compare traceparent with the generated span id substituted.
        var parts = want.Traceparent.Split('-');
        var expected = string.Join("-", parts[0], parts[1], a.SpanId.ToHexString(), parts[3]);
        Assert.True(expected == tp, $"{which} traceparent: want {expected}, got {tp}");
        Assert.True(want.Tracestate == ts, $"{which} tracestate: want '{want.Tracestate}', got '{ts}'");
        Assert.True(want.Sampled == a.Recorded, $"{which} sampled: want {want.Sampled}, got {a.Recorded}");
        var recorded = exported.FirstOrDefault(e => e.SpanId == a.SpanId);
        var ratio = recorded?.GetTagItem(OtTraceState.SamplingRatioKey) as double?;
        Assert.True(want.SamplingRatio == ratio, $"{which} sampling.ratio: want {want.SamplingRatio}, got {ratio}");
    }

    [Fact]
    public void RatioQuarterKeepsAboutAQuarterAndWeightsToTheTotal()
    {
        var sourceName = "openlog.ratio." + Guid.NewGuid().ToString("N");
        using var source = new ActivitySource(sourceName);
        var exported = new List<Activity>();
        using var randomFlag = new RandomFlagListener();
        using (var provider = Sdk.CreateTracerProviderBuilder().AddSource(sourceName).SetSampler(OpenLogSampler.Create(0.25)).AddInMemoryExporter(exported).Build())
        {
            const int n = 20_000;
            for (var i = 0; i < n; i++)
            {
                Activity.Current = null;
                source.StartActivity("root")?.Stop();
            }
            var weighted = exported.Sum(a => 1 / (double)a.GetTagItem(OtTraceState.SamplingRatioKey)!);
            Assert.True(Math.Abs(exported.Count / (double)n - 0.25) < 0.02, $"kept {exported.Count}");
            Assert.True(Math.Abs(weighted - n) / n < 0.08, $"weighted {weighted}");
            foreach (var a in exported.Take(10))
            {
                Assert.Equal("th:c", OtTraceState.GetMember(a.TraceStateString, "ot"));
                Assert.Equal((ActivityTraceFlags)3, a.ActivityTraceFlags);
            }
        }
    }
}

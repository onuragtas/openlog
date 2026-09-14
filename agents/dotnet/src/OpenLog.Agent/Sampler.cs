using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Globalization;
using System.Linq;
using System.Text.RegularExpressions;
using System.Threading;
using OpenTelemetry.Trace;

namespace OpenLog.Agent;

// Consistent probability sampling with the OpenTelemetry tracestate `ot` entry
// (https://opentelemetry.io/docs/specs/otel/trace/tracestate-probability-sampling/), a port of the Go agent's
// sampler.go (and the Node.js agent's sampler.ts). Cross-language fixtures produced by the Go agent:
// agents/node/test/interop/go-sampler-fixtures.json, verified in SamplerFixtureTests.

/// <summary>56 random bits and their ot=rv encoding (14 hex digits).</summary>
public delegate (ulong Value, string Hex) RandomnessSource();

/// <summary>Helpers for the tracestate <c>ot</c> entry.</summary>
public static class OtTraceState
{
    /// <summary>Span attribute carrying the head sampling probability (only set when p &lt; 1; apm.md §4 weights by 1/p).</summary>
    public const string SamplingRatioKey = "sampling.ratio";

    internal const string OtKey = "ot";
    internal const int MaxOtValueLen = 256;
    internal const int MaxMembers = 32;
    /// <summary>2^56 (exclusive upper bound of the threshold; T = 2^56 would mean p = 0).</summary>
    public const ulong MaxThreshold = 1UL << 56;
    internal const ulong RandomnessMask = MaxThreshold - 1;

    private static readonly ThreadLocal<Random> Rng = new(() => new Random(Guid.NewGuid().GetHashCode()));

    /// <summary>Default randomness: 56 pseudo-random bits (crypto-grade randomness is not required).</summary>
    public static readonly RandomnessSource DefaultRandomness = () =>
    {
        var r = Rng.Value!;
        var hi = (ulong)r.Next(0, 0x10000000);
        var lo = (ulong)r.Next(0, 0x10000000);
        var v = (hi << 28) | lo;
        return (v, v.ToString("x14", CultureInfo.InvariantCulture));
    };

    /// <summary>Parses the ot value ("k1:v1;k2:v2").</summary>
    public static List<KeyValuePair<string, string>> Parse(string? s)
    {
        var o = new List<KeyValuePair<string, string>>();
        if (string.IsNullOrEmpty(s)) return o;
        foreach (var part in s!.Split(';'))
        {
            var i = part.IndexOf(':');
            if (i > 0) o.Add(new KeyValuePair<string, string>(part.Substring(0, i), part.Substring(i + 1)));
        }
        return o;
    }

    internal static string? Get(List<KeyValuePair<string, string>> o, string k)
    {
        foreach (var f in o)
        {
            if (f.Key == k) return f.Value;
        }
        return null;
    }

    internal static List<KeyValuePair<string, string>> Without(List<KeyValuePair<string, string>> o, string k) => o.Where(f => f.Key != k).ToList();

    internal static List<KeyValuePair<string, string>> With(List<KeyValuePair<string, string>> o, string k, string v)
    {
        var r = new List<KeyValuePair<string, string>> { new(k, v) };
        r.AddRange(Without(o, k));
        return r;
    }

    public static string Format(IEnumerable<KeyValuePair<string, string>> o) => string.Join(";", o.Select(f => f.Key + ":" + f.Value));

    private static readonly Regex Hex14 = new("^[0-9a-fA-F]{14}$", RegexOptions.CultureInvariant);
    private static readonly Regex Hex1To14 = new("^[0-9a-fA-F]{1,14}$", RegexOptions.CultureInvariant);
    private static readonly Regex Int = new("^[+-]?[0-9]+$", RegexOptions.CultureInvariant);

    /// <summary>Explicit 56-bit randomness ot=rv:&lt;14 hex digits&gt;.</summary>
    public static ulong? Randomness(List<KeyValuePair<string, string>> o)
    {
        var v = Get(o, "rv");
        if (v == null || !Hex14.IsMatch(v)) return null;
        return ulong.Parse(v, NumberStyles.HexNumber, CultureInfo.InvariantCulture);
    }

    /// <summary>Encodes T as up to 14 hex digits without trailing zeros ("0" for T=0).</summary>
    public static string EncodeThreshold(ulong t)
    {
        var s = t.ToString("x14", CultureInfo.InvariantCulture).TrimEnd('0');
        return s.Length == 0 ? "0" : s;
    }

    /// <summary>Decodes th:&lt;hex&gt; (1..14 hex digits, right-padded with zeros).</summary>
    public static ulong? ParseThreshold(string s)
    {
        if (!Hex1To14.IsMatch(s)) return null;
        return ulong.Parse(s.PadRight(14, '0'), NumberStyles.HexNumber, CultureInfo.InvariantCulture);
    }

    /// <summary>p from th:&lt;hex&gt; (p = 1 − T/2^56) or legacy p:&lt;n&gt; (p = 2^−n).</summary>
    public static double? Probability(List<KeyValuePair<string, string>> o)
    {
        var th = Get(o, "th");
        if (th != null && ParseThreshold(th) is ulong t) return 1 - (double)t / MaxThreshold;
        var p = Get(o, "p");
        if (p != null && Int.IsMatch(p) && int.TryParse(p, NumberStyles.AllowLeadingSign, CultureInfo.InvariantCulture, out var n) && n >= 0 && n <= 63)
        {
            return n == 63 ? 0 : Math.Pow(2, -n);
        }
        return null;
    }

    // W3C tracestate value: printable ASCII except ',' and '=', not ending with a space, at most 256 characters.
    private static readonly Regex ValidValue = new(@"^[\x20-\x2b\x2d-\x3c\x3e-\x7e]{0,255}[\x21-\x2b\x2d-\x3c\x3e-\x7e]$", RegexOptions.CultureInvariant);

    /// <summary>Value of the member <paramref name="key"/> of a tracestate header value.</summary>
    public static string? GetMember(string? traceState, string key)
    {
        if (string.IsNullOrEmpty(traceState)) return null;
        foreach (var raw in traceState!.Split(','))
        {
            var m = raw.Trim();
            if (m.Length > key.Length && m[key.Length] == '=' && m.StartsWith(key, StringComparison.Ordinal)) return m.Substring(key.Length + 1);
        }
        return null;
    }

    /// <summary>
    /// Stores <paramref name="o"/> as the ot member of <paramref name="traceState"/> (moved to the front), keeping W3C
    /// limits: the value is at most 256 characters (sub-keys other than th and rv are dropped, last first) and the list
    /// keeps at most 32 members (the right-most member is dropped). An empty <paramref name="o"/> removes the member.
    /// On an invalid value the tracestate is returned unchanged.
    /// </summary>
    public static string? WithOt(string? traceState, List<KeyValuePair<string, string>> o)
    {
        var fields = o;
        while (Format(fields).Length > MaxOtValueLen)
        {
            var i = fields.Count - 1;
            while (i >= 0 && (fields[i].Key == "th" || fields[i].Key == "rv")) i--;
            if (i < 0) return traceState;
            fields = fields.Where((_, idx) => idx != i).ToList();
        }
        var others = string.IsNullOrEmpty(traceState)
            ? new List<string>()
            : traceState!.Split(',').Select(m => m.Trim()).Where(m => m.Length > 0 && !m.StartsWith(OtKey + "=", StringComparison.Ordinal)).ToList();
        if (fields.Count == 0)
        {
            if (GetMember(traceState, OtKey) == null) return traceState;
            return string.Join(",", others);
        }
        var value = Format(fields);
        if (!ValidValue.IsMatch(value)) return traceState;
        var members = new List<string> { OtKey + "=" + value };
        members.AddRange(others);
        return string.Join(",", members.Take(MaxMembers));
    }
}

/// <summary>Root sampler for 0 &lt; ratio &lt; 1 (threshold comparison against the trace id or ot=rv randomness).</summary>
public sealed class ConsistentRatioRootSampler : Sampler
{
    private readonly bool writeRV;
    private readonly RandomnessSource randomness;

    public ConsistentRatioRootSampler(double ratio, bool writeRV, RandomnessSource randomness)
    {
        Ratio = ratio;
        this.writeRV = writeRV;
        this.randomness = randomness;
        // ratio·2^56 is exact in float64 (power-of-two scaling); 1-ratio would not be.
        var keep = (ulong)Math.Floor(ratio * OtTraceState.MaxThreshold + 0.5);
        if (keep == 0) keep = 1;
        Threshold = OtTraceState.MaxThreshold - keep;
        Th = OtTraceState.EncodeThreshold(Threshold);
        Description = $"OpenlogConsistentRatio{{{ratio.ToString(CultureInfo.InvariantCulture)}}}";
    }

    public double Ratio { get; }
    public ulong Threshold { get; }
    public string Th { get; }

    public override SamplingResult ShouldSample(in SamplingParameters p)
    {
        var ts = p.ParentContext.TraceState;
        var ot = OtTraceState.Parse(OtTraceState.GetMember(ts, OtTraceState.OtKey));
        var r = OtTraceState.Randomness(ot);
        if (r == null)
        {
            if (writeRV)
            {
                var (value, hex) = randomness();
                r = value;
                ot = OtTraceState.Without(ot, "rv");
                ot.Add(new KeyValuePair<string, string>("rv", hex));
            }
            else
            {
                r = TraceIdRandomness(p.TraceId);
            }
        }
        if (r.Value < Threshold)
        {
            return new SamplingResult(SamplingDecision.Drop, OtTraceState.WithOt(ts, OtTraceState.Without(ot, "th")) ?? "");
        }
        return new SamplingResult(
            SamplingDecision.RecordAndSample,
            new[] { new KeyValuePair<string, object>(OtTraceState.SamplingRatioKey, Ratio) },
            OtTraceState.WithOt(ts, OtTraceState.With(ot, "th", Th)) ?? "");
    }

    internal static ulong TraceIdRandomness(ActivityTraceId traceId)
    {
        var hex = traceId.ToHexString();
        return ulong.Parse(hex.Substring(18, 14), NumberStyles.HexNumber, CultureInfo.InvariantCulture) & OtTraceState.RandomnessMask;
    }
}

/// <summary>Adds ot=rv to roots decided by another sampler (ratio 0 or 1).</summary>
internal sealed class RVRootSampler : Sampler
{
    private readonly Sampler inner;
    private readonly RandomnessSource randomness;

    public RVRootSampler(Sampler inner, RandomnessSource randomness)
    {
        this.inner = inner;
        this.randomness = randomness;
        Description = $"OpenlogRV{{{inner.Description}}}";
    }

    public override SamplingResult ShouldSample(in SamplingParameters p)
    {
        var res = inner.ShouldSample(p);
        var ts = res.TraceStateString ?? p.ParentContext.TraceState;
        var ot = OtTraceState.Parse(OtTraceState.GetMember(ts, OtTraceState.OtKey));
        if (OtTraceState.Randomness(ot) != null) return res;
        var (_, hex) = randomness();
        var fields = OtTraceState.Without(ot, "rv");
        fields.Add(new KeyValuePair<string, string>("rv", hex));
        return new SamplingResult(res.Decision, res.Attributes, OtTraceState.WithOt(ts, fields) ?? "");
    }
}

/// <summary>Samples every span whose remote parent is sampled and records the upstream sampling probability on it.</summary>
public sealed class RemoteParentSampledSampler : Sampler
{
    public RemoteParentSampledSampler()
    {
        Description = "OpenlogRemoteParentSampled";
    }

    public override SamplingResult ShouldSample(in SamplingParameters p)
    {
        var ts = p.ParentContext.TraceState;
        var prob = OtTraceState.Probability(OtTraceState.Parse(OtTraceState.GetMember(ts, OtTraceState.OtKey)));
        if (prob is double v && v > 0 && v < 1)
        {
            return new SamplingResult(SamplingDecision.RecordAndSample, new[] { new KeyValuePair<string, object>(OtTraceState.SamplingRatioKey, v) }, ts ?? "");
        }
        return new SamplingResult(SamplingDecision.RecordAndSample, ts ?? "");
    }
}

/// <summary>Follows the parent's decision and keeps the parent's tracestate.</summary>
internal sealed class ParentDecisionSampler : Sampler
{
    private readonly bool sample;

    public ParentDecisionSampler(bool sample)
    {
        this.sample = sample;
        Description = sample ? "OpenlogParentSampled" : "OpenlogParentNotSampled";
    }

    public override SamplingResult ShouldSample(in SamplingParameters p) =>
        new(sample ? SamplingDecision.RecordAndSample : SamplingDecision.Drop, p.ParentContext.TraceState ?? "");
}

/// <summary>Always samples / never samples new traces (ratio 1 / 0).</summary>
internal sealed class FixedRootSampler : Sampler
{
    private readonly bool sample;

    public FixedRootSampler(bool sample)
    {
        this.sample = sample;
        Description = sample ? "AlwaysOnSampler" : "AlwaysOffSampler";
    }

    public override SamplingResult ShouldSample(in SamplingParameters p) => new(sample ? SamplingDecision.RecordAndSample : SamplingDecision.Drop);
}

/// <summary>The openlog sampler.</summary>
public static class OpenLogSampler
{
    /// <summary>
    /// Parent-based sampler with the Go agent's semantics:
    /// new traces are sampled with probability <paramref name="ratio"/> by comparing the 56-bit randomness (tracestate
    /// ot=rv, else the lower 56 bits of the trace id) against T = (1 − ratio)·2^56; sampled roots carry sampling.ratio
    /// and ot=th:&lt;T&gt;; children of a sampled remote parent are sampled and get sampling.ratio = p when tracestate
    /// carries p &lt; 1; other children follow their parent. With <paramref name="writeRV"/>, roots without ot=rv write
    /// explicit randomness.
    /// </summary>
    public static Sampler Create(double ratio, bool writeRV = false, RandomnessSource? randomness = null)
    {
        var rnd = randomness ?? OtTraceState.DefaultRandomness;
        Sampler root;
        var keepAll = ratio >= 1 || double.IsNaN(ratio);
        if (!keepAll && ratio > 0 && (ulong)Math.Floor(ratio * OtTraceState.MaxThreshold + 0.5) >= OtTraceState.MaxThreshold) keepAll = true;
        if (keepAll) root = new FixedRootSampler(true);
        else if (ratio <= 0) root = new FixedRootSampler(false);
        else root = new ConsistentRatioRootSampler(ratio, writeRV, rnd);
        if (writeRV && root is not ConsistentRatioRootSampler) root = new RVRootSampler(root, rnd);
        return new ParentBasedSampler(
            root,
            remoteParentSampled: new RemoteParentSampledSampler(),
            remoteParentNotSampled: new ParentDecisionSampler(false),
            localParentSampled: new ParentDecisionSampler(true),
            localParentNotSampled: new ParentDecisionSampler(false));
    }
}

using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Globalization;
using System.Linq;
using System.Text.RegularExpressions;
using OpenTelemetry.Context.Propagation;

namespace OpenLog.Agent;

/// <summary>
/// W3C Trace Context propagator that keeps the Level 2 random flag (traceparent flags 0x02). The OpenTelemetry .NET
/// TraceContextPropagator only reads and writes the sampled bit, which would make downstream Go/Node.js agents treat
/// the trace id as non-random.
/// </summary>
public sealed class OpenLogTraceContextPropagator : TextMapPropagator
{
    internal const string TraceParent = "traceparent";
    internal const string TraceState = "tracestate";
    internal const ActivityTraceFlags RandomFlag = (ActivityTraceFlags)0x02;

    private static readonly Regex TraceParentFormat = new(@"^([0-9a-f]{2})-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})(-.*)?$", RegexOptions.CultureInvariant);
    private static readonly Regex MemberFormat = new(@"^(?:[a-z0-9][_0-9a-z\-*/]{0,255}|[a-z0-9][_0-9a-z\-*/]{0,240}@[a-z][_0-9a-z\-*/]{0,13})=[\x20-\x2b\x2d-\x3c\x3e-\x7e]{0,255}[\x21-\x2b\x2d-\x3c\x3e-\x7e]$", RegexOptions.CultureInvariant);

    public override ISet<string> Fields { get; } = new HashSet<string> { TraceParent, TraceState };

    public override PropagationContext Extract<T>(PropagationContext context, T carrier, Func<T, string, IEnumerable<string>?> getter)
    {
        if (IsValid(context.ActivityContext) || carrier == null) return context;
        IEnumerable<string>? parents;
        try
        {
            parents = getter(carrier, TraceParent);
        }
        catch (Exception)
        {
            return context;
        }
        var list = parents?.ToList();
        if (list == null || list.Count != 1) return context;
        if (!TryParseTraceParent(list[0], out var traceId, out var spanId, out var flags)) return context;
        string? state = null;
        try
        {
            var states = getter(carrier, TraceState)?.ToList();
            if (states != null && states.Count > 0) state = ParseTraceState(states);
        }
        catch (Exception)
        {
            state = null;
        }
        return new PropagationContext(new ActivityContext(traceId, spanId, flags, state, isRemote: true), context.Baggage);
    }

    public override void Inject<T>(PropagationContext context, T carrier, Action<T, string, string> setter)
    {
        var ac = context.ActivityContext;
        if (!IsValid(ac) || carrier == null) return;
        var flags = (int)ac.TraceFlags & 0x03;
        setter(carrier, TraceParent, "00-" + ac.TraceId.ToHexString() + "-" + ac.SpanId.ToHexString() + "-" + flags.ToString("x2", CultureInfo.InvariantCulture));
        if (!string.IsNullOrEmpty(ac.TraceState)) setter(carrier, TraceState, ac.TraceState!);
    }

    internal static bool IsValid(ActivityContext ctx) => ctx.TraceId != default && ctx.SpanId != default;

    internal static bool TryParseTraceParent(string value, out ActivityTraceId traceId, out ActivitySpanId spanId, out ActivityTraceFlags flags)
    {
        traceId = default;
        spanId = default;
        flags = ActivityTraceFlags.None;
        var m = TraceParentFormat.Match(value.Trim());
        if (!m.Success) return false;
        var version = m.Groups[1].Value;
        if (version == "ff") return false;
        if (version == "00" && m.Groups[5].Success && m.Groups[5].Length > 0) return false;
        var tid = m.Groups[2].Value;
        var sid = m.Groups[3].Value;
        if (tid.All(ch => ch == '0') || sid.All(ch => ch == '0')) return false;
        traceId = ActivityTraceId.CreateFromString(tid.AsSpan());
        spanId = ActivitySpanId.CreateFromString(sid.AsSpan());
        var raw = byte.Parse(m.Groups[4].Value, NumberStyles.HexNumber, CultureInfo.InvariantCulture);
        flags = (ActivityTraceFlags)(raw & 0x03);
        return true;
    }

    /// <summary>Combines tracestate headers; returns null when a member is invalid, keys repeat or there are more than 32 members.</summary>
    internal static string? ParseTraceState(IEnumerable<string> headers)
    {
        var members = new List<string>();
        var keys = new HashSet<string>(StringComparer.Ordinal);
        foreach (var header in headers)
        {
            foreach (var raw in header.Split(','))
            {
                var m = raw.Trim(' ', '\t');
                if (m.Length == 0) continue;
                if (!MemberFormat.IsMatch(m)) return null;
                var key = m.Substring(0, m.IndexOf('='));
                if (!keys.Add(key)) return null;
                members.Add(m);
            }
        }
        if (members.Count > OtTraceState.MaxMembers) return null;
        return members.Count == 0 ? null : string.Join(",", members);
    }
}

/// <summary>
/// Sets the W3C Trace Context Level 2 random flag (traceparent flags 0x02) like the Go agent's randomTracerProvider:
/// root activities get it (trace ids generated by .NET are random), children inherit it from their parent, so a remote
/// W3C Level 1 parent without it is continued unchanged. .NET derives ActivityTraceFlags from the sampling decision
/// only, so the flag is added right after the activity started (before it is propagated or exported). This listener
/// never influences sampling (it returns ActivitySamplingResult.None).
/// </summary>
internal sealed class RandomFlagListener : IDisposable
{
    private readonly ActivityListener listener;

    public RandomFlagListener()
    {
        listener = new ActivityListener
        {
            ShouldListenTo = _ => true,
            Sample = (ref ActivityCreationOptions<ActivityContext> _) => ActivitySamplingResult.None,
            SampleUsingParentId = (ref ActivityCreationOptions<string> _) => ActivitySamplingResult.None,
            ActivityStarted = Apply,
        };
        ActivitySource.AddActivityListener(listener);
    }

    internal static void Apply(Activity activity)
    {
        if (activity.IdFormat != ActivityIdFormat.W3C || (activity.ActivityTraceFlags & OpenLogTraceContextPropagator.RandomFlag) != 0) return;
        bool random;
        if (activity.Parent is Activity parent && parent.IdFormat == ActivityIdFormat.W3C && parent.TraceId == activity.TraceId)
        {
            random = (parent.ActivityTraceFlags & OpenLogTraceContextPropagator.RandomFlag) != 0;
        }
        else if (activity.ParentSpanId != default)
        {
            random = RemoteParentRandom(activity);
        }
        else
        {
            random = true;
        }
        if (random) activity.ActivityTraceFlags |= OpenLogTraceContextPropagator.RandomFlag;
    }

    /// <summary>Activity.ParentId of a remote parent is "00-&lt;trace&gt;-&lt;span&gt;-&lt;flags&gt;".</summary>
    private static bool RemoteParentRandom(Activity activity)
    {
        var id = activity.ParentId;
        if (id == null || id.Length < 55 || id[52] != '-') return false;
        return byte.TryParse(id.Substring(53, 2), NumberStyles.HexNumber, CultureInfo.InvariantCulture, out var flags) && (flags & 0x02) != 0;
    }

    public void Dispose() => listener.Dispose();
}

using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.Diagnostics;
using System.Linq;
using System.Text.RegularExpressions;
using OpenTelemetry;

namespace OpenLog.Agent;

/// <summary>
/// Applies OPENLOG_DB_QUERY_TEXT to every span's db.query.text / db.statement when the span ends (before the export
/// processor): <c>Sanitized</c> normalizes SQL like the Go agent and key/value commands to <c>CMD ? ?</c>, <c>Raw</c>
/// keeps the text, <c>Off</c> removes it. Statements are capped at 4096 characters.
/// </summary>
public sealed class DbStatementProcessor : BaseProcessor<Activity>
{
    private static readonly string[] QueryTextKeys = { "db.query.text", "db.statement" };
    private static readonly HashSet<string> KvSystems = new(StringComparer.Ordinal) { "redis", "valkey", "memcached" };
    // Document/search stores whose instrumentations already mask values or send JSON bodies.
    private static readonly HashSet<string> NonSqlSystems = new(StringComparer.Ordinal)
    {
        "mongodb", "elasticsearch", "opensearch", "aws.dynamodb", "dynamodb", "couchdb", "couchbase", "azure.cosmosdb", "cosmosdb",
    };
    private static readonly Regex AlreadyKv = new(@"^[A-Z][A-Z._-]*( \?)*( …)?$", RegexOptions.CultureInvariant);
    private static readonly char[] Whitespace = { ' ', '\t', '\n', '\r' };

    private readonly DbQueryTextMode mode;

    public DbStatementProcessor(DbQueryTextMode mode)
    {
        this.mode = mode;
    }

    public override void OnEnd(Activity activity)
    {
        foreach (var key in QueryTextKeys)
        {
            if (activity.GetTagItem(key) is not string text) continue;
            if (mode == DbQueryTextMode.Off)
            {
                activity.SetTag(key, null);
                continue;
            }
            var result = text;
            if (mode == DbQueryTextMode.Sanitized)
            {
                var system = ((activity.GetTagItem("db.system.name") ?? activity.GetTagItem("db.system")) as string ?? "").ToLowerInvariant();
                if (KvSystems.Contains(system))
                {
                    if (!AlreadyKv.IsMatch(text))
                    {
                        var tokens = text.Trim().Split(Whitespace, StringSplitOptions.RemoveEmptyEntries);
                        result = SqlSanitizer.SanitizeKeyValue(tokens.Length > 0 ? tokens[0] : "", tokens.Skip(1).ToList());
                    }
                }
                else if (!NonSqlSystems.Contains(system))
                {
                    result = SqlSanitizer.Sanitize(text, system);
                }
            }
            result = SqlSanitizer.Truncate(result);
            if (!ReferenceEquals(result, text) && result != text) activity.SetTag(key, result);
        }
    }
}

/// <summary>
/// Makes APM transactions group by route (apm.md §2.1 uses http.route of the entry span). ASP.NET Core sets
/// http.route on the server span for minimal APIs, MVC, Razor Pages and gRPC endpoints; handlers that only put
/// http.route on their own spans (custom routers, middleware-based frameworks) are covered by this processor: it
/// remembers the longest http.route seen on any local descendant of an entry SERVER span and, when the entry span has
/// none, sets http.route and renames it <c>&lt;METHOD&gt; &lt;route&gt;</c> before it is exported.
/// </summary>
public sealed class RouteProcessor : BaseProcessor<Activity>
{
    private const int MaxTracked = 10_000;

    private sealed class Entry
    {
        public Entry(Activity span) => Span = span;
        public Activity Span { get; }
        public string? Route;
    }

    private readonly ConcurrentDictionary<ActivitySpanId, Entry> owner = new();

    public override void OnStart(Activity activity)
    {
        Entry? entry = null;
        if (activity.Kind == ActivityKind.Server && (activity.Parent == null || activity.HasRemoteParent))
        {
            entry = new Entry(activity);
        }
        else if (activity.Parent != null)
        {
            owner.TryGetValue(activity.Parent.SpanId, out entry);
        }
        if (entry == null) return;
        if (owner.Count >= MaxTracked) owner.Clear();
        owner[activity.SpanId] = entry;
    }

    public override void OnEnd(Activity activity)
    {
        if (!owner.TryRemove(activity.SpanId, out var entry)) return;
        if (!ReferenceEquals(entry.Span, activity))
        {
            if (activity.GetTagItem("http.route") is string route && route.Length > 0 && route != "*")
            {
                lock (entry)
                {
                    if (entry.Route == null || route.Length > entry.Route.Length) entry.Route = route;
                }
            }
            return;
        }
        string? best;
        lock (entry) best = entry.Route;
        var current = activity.GetTagItem("http.route") as string;
        if (best != null && string.IsNullOrEmpty(current))
        {
            activity.SetTag("http.route", best);
            if ((activity.GetTagItem("http.request.method") ?? activity.GetTagItem("http.method")) is string method && method.Length > 0)
            {
                activity.DisplayName = method + " " + best;
            }
        }
    }

    protected override bool OnShutdown(int timeoutMilliseconds)
    {
        owner.Clear();
        return true;
    }
}

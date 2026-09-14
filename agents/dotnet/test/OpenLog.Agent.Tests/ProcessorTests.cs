using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Linq;
using OpenLog.Agent;
using OpenTelemetry;
using OpenTelemetry.Context.Propagation;
using OpenTelemetry.Trace;
using Xunit;

namespace OpenLog.Agent.Tests;

[Collection("global-activity-state")]
public class ProcessorTests
{
    private static (ActivitySource Source, TracerProvider Provider, List<Activity> Exported) Setup(DbQueryTextMode mode)
    {
        var name = "openlog.processors." + Guid.NewGuid().ToString("N");
        var source = new ActivitySource(name);
        var exported = new List<Activity>();
        var provider = Sdk.CreateTracerProviderBuilder()
            .AddSource(name)
            .SetSampler(OpenLogSampler.Create(1))
            .AddProcessor(new RouteProcessor())
            .AddProcessor(new DbStatementProcessor(mode))
            .AddInMemoryExporter(exported)
            .Build()!;
        return (source, provider, exported);
    }

    private static string? Statement(DbQueryTextMode mode, string system, string key, string text)
    {
        var (source, provider, exported) = Setup(mode);
        using (source)
        using (provider)
        {
            Activity.Current = null;
            using (var a = source.StartActivity("db", ActivityKind.Client))
            {
                a!.SetTag(system.Length > 0 ? "db.system.name" : "x", system);
                a.SetTag(key, text);
            }
            return exported.Single().GetTagItem(key) as string;
        }
    }

    [Fact]
    public void SanitizesSqlAndKeyValueStatements()
    {
        Assert.Equal("SELECT * FROM t WHERE id = ?", Statement(DbQueryTextMode.Sanitized, "postgresql", "db.query.text", "SELECT * FROM t WHERE id = 42"));
        Assert.Equal("SELECT * FROM t WHERE name = ?", Statement(DbQueryTextMode.Sanitized, "mysql", "db.statement", "SELECT * FROM t WHERE name = \"bob\""));
        Assert.Equal("HSET ? ? ?", Statement(DbQueryTextMode.Sanitized, "redis", "db.query.text", "HSET user:1 name bob"));
        Assert.Equal("GET", Statement(DbQueryTextMode.Sanitized, "redis", "db.statement", "GET"));
        Assert.Equal("{\"find\":\"users\"}", Statement(DbQueryTextMode.Sanitized, "mongodb", "db.query.text", "{\"find\":\"users\"}"));
        Assert.Equal("SELECT 42", Statement(DbQueryTextMode.Raw, "postgresql", "db.query.text", "SELECT 42"));
        Assert.Null(Statement(DbQueryTextMode.Off, "postgresql", "db.query.text", "SELECT 42"));
        Assert.Equal(SqlSanitizer.MaxQueryText, Statement(DbQueryTextMode.Raw, "postgresql", "db.query.text", "SELECT " + new string('a', 5000))!.Length);
    }

    [Fact]
    public void CopiesTheLongestDescendantRouteToTheEntrySpan()
    {
        var (source, provider, exported) = Setup(DbQueryTextMode.Sanitized);
        using (source)
        using (provider)
        {
            Activity.Current = null;
            using (var server = source.StartActivity("GET", ActivityKind.Server))
            {
                server!.SetTag("http.request.method", "GET");
                using (var mw = source.StartActivity("middleware"))
                {
                    mw!.SetTag("http.route", "/api");
                    using var handler = source.StartActivity("handler");
                    handler!.SetTag("http.route", "/api/users/{id}");
                }
            }
            var entry = exported.Single(a => a.Kind == ActivityKind.Server);
            Assert.Equal("/api/users/{id}", entry.GetTagItem("http.route"));
            Assert.Equal("GET /api/users/{id}", entry.DisplayName);

            exported.Clear();
            using (var server = source.StartActivity("GET /orders/{id}", ActivityKind.Server))
            {
                server!.SetTag("http.route", "/orders/{id}");
                using var child = source.StartActivity("child");
                child!.SetTag("http.route", "/something/much/longer/{x}");
            }
            var kept = exported.Single(a => a.Kind == ActivityKind.Server);
            Assert.Equal("/orders/{id}", kept.GetTagItem("http.route"));
            Assert.Equal("GET /orders/{id}", kept.DisplayName);
        }
    }

    [Fact]
    public void PropagatorKeepsTheRandomFlagAndValidatesTraceState()
    {
        var p = new OpenLogTraceContextPropagator();
        IEnumerable<string>? Get(Dictionary<string, string[]> c, string k) => c.TryGetValue(k, out var v) ? v : null;
        var ctx = p.Extract(default, new Dictionary<string, string[]>
        {
            ["traceparent"] = new[] { "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-03" },
            ["tracestate"] = new[] { "ot=th:c", "congo=t61rcWkgMzE" },
        }, Get).ActivityContext;
        Assert.True(ctx.IsRemote);
        Assert.Equal((ActivityTraceFlags)3, ctx.TraceFlags);
        Assert.Equal("ot=th:c,congo=t61rcWkgMzE", ctx.TraceState);

        var carrier = new Dictionary<string, string>();
        p.Inject(new PropagationContext(ctx, default), carrier, (c, k, v) => c[k] = v);
        Assert.Equal("00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-03", carrier["traceparent"]);
        Assert.Equal("ot=th:c,congo=t61rcWkgMzE", carrier["tracestate"]);

        Assert.False(p.Extract(default, new Dictionary<string, string[]> { ["traceparent"] = new[] { "00-00000000000000000000000000000000-b7ad6b7169203331-01" } }, Get).ActivityContext.IsValid());
        Assert.False(p.Extract(default, new Dictionary<string, string[]> { ["traceparent"] = new[] { "ff-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01" } }, Get).ActivityContext.IsValid());
        Assert.False(p.Extract(default, new Dictionary<string, string[]> { ["traceparent"] = new[] { "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01-extra" } }, Get).ActivityContext.IsValid());
        var future = p.Extract(default, new Dictionary<string, string[]> { ["traceparent"] = new[] { "01-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-ff-extra" } }, Get).ActivityContext;
        Assert.Equal((ActivityTraceFlags)3, future.TraceFlags);
        var badState = p.Extract(default, new Dictionary<string, string[]>
        {
            ["traceparent"] = new[] { "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01" },
            ["tracestate"] = new[] { "a=1,a=2" },
        }, Get).ActivityContext;
        Assert.True(badState.IsValid());
        Assert.Null(badState.TraceState);
    }
}

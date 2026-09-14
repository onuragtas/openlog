using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Linq;
using System.Net.Http;
using System.Threading.Tasks;
using Xunit;

namespace OpenLog.Agent.IntegrationTests;

/// <summary>
/// MassTransit (RabbitMQ transport), Confluent.Kafka and SQL Server against throwaway containers (test/docker-compose.yml):
/// the HTTP request trace continues through the producer into the consumer, with messaging attributes.
/// </summary>
[Collection("sample-app")]
public sealed class MessagingTests
{
    private const int Server = 2;
    private const int Client = 3;
    private const int Producer = 4;
    private const int Consumer = 5;

    private readonly AppFixture f;

    public MessagingTests(AppFixture fixture) => f = fixture;

    private static string Dump(SpanData s) => $"{s.Name} kind={s.Kind} scope={s.Scope.Name} trace={s.TraceId} parent={s.ParentSpanId} links={string.Join(",", s.Links.Select(l => l.TraceId))} {{" + string.Join(", ", s.Attributes.Select(kv => kv.Key + "=" + kv.Value)) + "}";

    private async Task<string> Get(string path)
    {
        var tid = ActivityTraceId.CreateRandom().ToHexString();
        var req = new HttpRequestMessage(HttpMethod.Get, path);
        req.Headers.TryAddWithoutValidation("traceparent", $"00-{tid}-{ActivitySpanId.CreateRandom().ToHexString()}-01");
        (await f.Http.SendAsync(req)).EnsureSuccessStatusCode();
        return tid;
    }

    /// <summary>The spans of a trace, including spans that only link to it (consumers that start their own trace).</summary>
    private static List<SpanData> Related(OtlpCaptureServer c, string traceId) =>
        c.Spans.Where(s => s.TraceId == traceId || s.Links.Any(l => l.TraceId == traceId)).ToList();

    [DbFact("RABBITMQ_CONNECTION")]
    public async Task MassTransitPublishAndConsumeContinueTheRequestTrace()
    {
        var tid = await Get("/messaging/masstransit/41");
        var (producer, consumer) = await f.Capture.WaitFor(c =>
        {
            var spans = Related(c, tid);
            var p = spans.FirstOrDefault(s => s.Kind == Producer && s.Scope.Name == "MassTransit");
            var k = spans.FirstOrDefault(s => s.Kind == Consumer && s.Scope.Name == "MassTransit");
            return p != null && k != null ? Tuple.Create(p, k) : null;
        }, "MassTransit producer and consumer spans", 30_000);
        var server = f.Capture.Spans.First(s => s.TraceId == tid && s.Kind == Server);
        Assert.True(producer.TraceId == tid, Dump(producer));
        Assert.True(producer.Attr("messaging.system") == "rabbitmq", Dump(producer));
        Assert.Contains("OrderPlaced", producer.Attr("messaging.destination.name") ?? "");
        // producer → consumer: same trace, the consumer's parent is the producer span (or a span below it)
        Assert.True(consumer.TraceId == tid, Dump(consumer));
        Assert.NotEqual("", consumer.ParentSpanId);
        // MassTransit 8.5 consumer spans carry messaging.operation and messaging.masstransit.* (no messaging.system)
        Assert.True(consumer.Attr("messaging.operation") == "process", Dump(consumer));
        Assert.Contains("OrderPlaced", consumer.Attr("messaging.masstransit.destination_address") ?? "");
        Assert.Equal("OpenLog.SampleApp.OrderPlacedConsumer", consumer.Attr("messaging.masstransit.consumer_type"));
        Assert.NotEqual(server.SpanId, consumer.SpanId);
        var log = await f.Capture.WaitFor(c => c.Logs.FirstOrDefault(l => l.Body.Contains("consumed masstransit order 41") || (l.Attributes.TryGetValue("OrderId", out var v) && v?.ToString() == "41")), "consumer log record");
        Assert.Equal(tid, log.TraceId);
    }

    [DbFact("KAFKA_BOOTSTRAP")]
    public async Task ConfluentKafkaProduceAndConsumeAreLinkedToTheRequestTrace()
    {
        var tid = await Get("/messaging/kafka/42");
        var producer = await f.Capture.WaitFor(c => c.Spans.FirstOrDefault(s => s.TraceId == tid && s.Kind == Producer), "Kafka producer span", 30_000);
        Assert.True(producer.Attr("messaging.system") == "kafka", Dump(producer));
        Assert.True(producer.Attr("messaging.destination.name") == "openlog-dotnet-orders", Dump(producer));
        var server = f.Capture.Spans.First(s => s.TraceId == tid && s.Kind == Server);
        Assert.Equal(server.SpanId, producer.ParentSpanId);
        // consumer side: the instrumentation's "poll" span (CLIENT, messaging semantic conventions) continues the producer's
        // trace or links to it
        var consumer = await f.Capture.WaitFor(c => c.Spans.FirstOrDefault(s => s.Scope.Name == producer.Scope.Name && (s.Kind == Consumer || s.Kind == Client)
            && (s.TraceId == tid || s.Links.Any(l => l.TraceId == tid))), "Kafka poll span of the produced message", 60_000);
        Assert.True(consumer.Attr("messaging.system") == "kafka", Dump(consumer));
        Assert.True(consumer.Attr("messaging.destination.name") == "openlog-dotnet-orders", Dump(consumer));
        Assert.True(consumer.TraceId == tid || consumer.Links.Any(l => l.TraceId == tid && l.SpanId == producer.SpanId), Dump(consumer));
    }

    [DbFact("SQLSERVER_CONNECTION")]
    public async Task SqlClientStatementIsSanitized()
    {
        var tid = ActivityTraceId.CreateRandom().ToHexString();
        var req = new HttpRequestMessage(HttpMethod.Get, "/db/sqlserver");
        req.Headers.TryAddWithoutValidation("traceparent", $"00-{tid}-{ActivitySpanId.CreateRandom().ToHexString()}-01");
        var res = await f.Http.SendAsync(req);
        Assert.Equal("42", await res.Content.ReadAsStringAsync());
        var span = await f.Capture.WaitFor(c => c.Spans.FirstOrDefault(s => s.TraceId == tid && s.Kind == Client && (s.Attr("db.query.text") ?? s.Attr("db.statement")) != null), "SqlClient span");
        var system = span.Attr("db.system.name") ?? span.Attr("db.system");
        Assert.True(system == "microsoft.sql_server" || system == "mssql", Dump(span));
        Assert.True((span.Attr("db.query.text") ?? span.Attr("db.statement")) == "SELECT ? AS answer WHERE ? = ? AND ? IN (?)", Dump(span));
        Assert.DoesNotContain(span.Attributes.Values, v => v?.ToString()?.Contains("secret") == true);
    }
}

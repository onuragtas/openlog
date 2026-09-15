using System;
using System.Collections.Generic;
using System.Linq;
using System.Net;
using System.Net.Http;
using System.Threading;
using System.Threading.Tasks;
using Confluent.Kafka;
using Grpc.Core;
using MassTransit;
using Grpc.Net.Client;
using Microsoft.AspNetCore.Builder;
using Microsoft.AspNetCore.Hosting;
using Microsoft.AspNetCore.Http;
using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.Server.Kestrel.Core;
using Microsoft.Data.SqlClient;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;
using MySqlConnector;
using Npgsql;
using OpenLog.Agent;
using OpenTelemetry.Trace;
using OpenLog.SampleApp.Grpc;
using StackExchange.Redis;

namespace OpenLog.SampleApp;

/// <summary>Ports and backends of the sample app (environment: HTTP_PORT, GRPC_PORT, PG_CONNECTION, MYSQL_CONNECTION, REDIS_CONNECTION,
/// SQLSERVER_CONNECTION, RABBITMQ_CONNECTION, KAFKA_BOOTSTRAP).</summary>
public sealed class SampleSettings
{
    public int HttpPort { get; set; } = 8080;
    public int GrpcPort { get; set; } = 8081;
    public string? Postgres { get; set; }
    public string? MySql { get; set; }
    public string? Redis { get; set; }
    public string? SqlServer { get; set; }
    /// <summary>MassTransit RabbitMQ host URI, e.g. amqp://openlog:openlog@127.0.0.1:24672/</summary>
    public string? RabbitMq { get; set; }
    public string? Kafka { get; set; }
    /// <summary>false: the agent is not registered (overhead baseline).</summary>
    public bool Agent { get; set; } = true;
    /// <summary>false: no console logger (benchmark).</summary>
    public bool ConsoleLogs { get; set; } = true;

    public static SampleSettings FromEnvironment()
    {
        static string? Env(string n) => string.IsNullOrWhiteSpace(Environment.GetEnvironmentVariable(n)) ? null : Environment.GetEnvironmentVariable(n);
        return new SampleSettings
        {
            HttpPort = int.TryParse(Env("HTTP_PORT"), out var h) ? h : 8080,
            GrpcPort = int.TryParse(Env("GRPC_PORT"), out var g) ? g : 8081,
            Postgres = Env("PG_CONNECTION"),
            MySql = Env("MYSQL_CONNECTION"),
            Redis = Env("REDIS_CONNECTION"),
            SqlServer = Env("SQLSERVER_CONNECTION"),
            RabbitMq = Env("RABBITMQ_CONNECTION"),
            Kafka = Env("KAFKA_BOOTSTRAP"),
            Agent = Env("SAMPLE_AGENT") != "false",
        };
    }
}

public sealed class Product
{
    public int Id { get; set; }
    public string Name { get; set; } = "";
    public decimal Price { get; set; }
}

public sealed class ShopContext : DbContext
{
    public ShopContext(DbContextOptions<ShopContext> options) : base(options) { }

    public DbSet<Product> Products => Set<Product>();
}

/// <summary>MassTransit message published by /messaging/masstransit.</summary>
public sealed record OrderPlaced(int Id);

public sealed class OrderPlacedConsumer : IConsumer<OrderPlaced>
{
    private readonly ILogger<OrderPlacedConsumer> logger;

    public OrderPlacedConsumer(ILogger<OrderPlacedConsumer> logger) => this.logger = logger;

    public Task Consume(ConsumeContext<OrderPlaced> context)
    {
        logger.LogInformation("consumed masstransit order {OrderId}", context.Message.Id);
        return Task.CompletedTask;
    }
}

/// <summary>Consumes <see cref="SampleApp.KafkaTopic"/> with the instrumented Confluent.Kafka consumer.</summary>
public sealed class KafkaOrderConsumer : BackgroundService
{
    private readonly InstrumentedConsumerBuilder<string, string> builder;
    private readonly ILogger<KafkaOrderConsumer> logger;

    public KafkaOrderConsumer(InstrumentedConsumerBuilder<string, string> builder, ILogger<KafkaOrderConsumer> logger)
    {
        this.builder = builder;
        this.logger = logger;
    }

    protected override Task ExecuteAsync(CancellationToken stoppingToken) => Task.Factory.StartNew(() =>
    {
        using var consumer = builder.Build();
        consumer.Subscribe(SampleApp.KafkaTopic);
        while (!stoppingToken.IsCancellationRequested)
        {
            try
            {
                var result = consumer.Consume(TimeSpan.FromMilliseconds(250));
                if (result?.Message != null) logger.LogInformation("consumed kafka order {Order}", result.Message.Value);
            }
            catch (ConsumeException e)
            {
                logger.LogDebug("kafka consume: {Reason}", e.Error.Reason);
            }
        }
        consumer.Close();
    }, stoppingToken, TaskCreationOptions.LongRunning, TaskScheduler.Default);
}

public sealed class GreeterService : Greeter.GreeterBase
{
    private readonly ILogger<GreeterService> logger;

    public GreeterService(ILogger<GreeterService> logger) => this.logger = logger;

    public override Task<HelloReply> SayHello(HelloRequest request, ServerCallContext context)
    {
        logger.LogInformation("greeting {Name}", request.Name);
        return Task.FromResult(new HelloReply { Message = "Hello " + request.Name });
    }
}

[ApiController]
[Route("orders")]
public sealed class OrdersController : ControllerBase
{
    private readonly ILogger<OrdersController> logger;

    public OrdersController(ILogger<OrdersController> logger) => this.logger = logger;

    [HttpGet("{id:int}")]
    public IActionResult Get(int id)
    {
        logger.LogInformation("order {OrderId} requested", id);
        return Ok(new { id, status = "shipped" });
    }
}

public static class SampleApp
{
    public const string KafkaTopic = "openlog-dotnet-orders";

    public static WebApplication Build(SampleSettings settings, Action<OpenLogOptions>? configureAgent = null, string[]? args = null)
    {
        var builder = WebApplication.CreateBuilder(args ?? Array.Empty<string>());
        builder.Logging.ClearProviders();
        if (settings.ConsoleLogs) builder.Logging.AddSimpleConsole(o => o.IncludeScopes = true);
        builder.Logging.SetMinimumLevel(LogLevel.Information);
        builder.Logging.AddFilter("Microsoft", LogLevel.Warning);
        builder.WebHost.ConfigureKestrel(k =>
        {
            k.Listen(IPAddress.Loopback, settings.HttpPort, o => o.Protocols = HttpProtocols.Http1);
            if (settings.GrpcPort > 0) k.Listen(IPAddress.Loopback, settings.GrpcPort, o => o.Protocols = HttpProtocols.Http2);
        });
        if (Environment.GetEnvironmentVariable("SAMPLE_LISTEN_ANY") == "true")
        {
            builder.WebHost.ConfigureKestrel(k =>
            {
                k.ListenAnyIP(settings.HttpPort + 1000, o => o.Protocols = HttpProtocols.Http1);
            });
        }

        if (settings.Agent)
        {
            builder.Services.AddOpenLog(o =>
            {
                configureAgent?.Invoke(o);
                if (settings.Kafka == null) return;
                // Confluent.Kafka has no ActivitySource of its own: OpenTelemetry.Instrumentation.ConfluentKafka (prerelease)
                // wraps the producer and consumer builders registered below
                var previous = o.ConfigureTracing;
                o.ConfigureTracing = b =>
                {
                    previous?.Invoke(b);
                    b.AddKafkaProducerInstrumentation<string, string>().AddKafkaConsumerInstrumentation<string, string>();
                };
            });
        }
        // explicit application part: the host may be started from another entry assembly (tests, benchmark)
        builder.Services.AddControllers().AddApplicationPart(typeof(OrdersController).Assembly);
        builder.Services.AddGrpc();
        builder.Services.AddHttpClient("self", c => c.BaseAddress = new Uri($"http://127.0.0.1:{settings.HttpPort}"));
        if (settings.Postgres != null)
        {
            builder.Services.AddDbContext<ShopContext>(o => o.UseNpgsql(settings.Postgres));
        }
        if (settings.Redis != null)
        {
            // the OpenTelemetry StackExchange.Redis instrumentation picks the multiplexer up from DI
            builder.Services.AddSingleton<IConnectionMultiplexer>(_ => ConnectionMultiplexer.Connect(settings.Redis));
        }

        if (settings.RabbitMq != null)
        {
            builder.Services.AddMassTransit(x =>
            {
                x.AddConsumer<OrderPlacedConsumer>();
                x.UsingRabbitMq((ctx, cfg) =>
                {
                    cfg.Host(new Uri(settings.RabbitMq));
                    cfg.ConfigureEndpoints(ctx);
                });
            });
        }
        if (settings.Kafka != null)
        {
            builder.Services.AddSingleton(_ => new InstrumentedProducerBuilder<string, string>(new ProducerConfig { BootstrapServers = settings.Kafka }));
            builder.Services.AddSingleton(_ => new InstrumentedConsumerBuilder<string, string>(new ConsumerConfig
            {
                BootstrapServers = settings.Kafka,
                GroupId = "openlog-sample",
                AutoOffsetReset = AutoOffsetReset.Earliest,
                AllowAutoCreateTopics = true,
            }));
            builder.Services.AddSingleton(sp => sp.GetRequiredService<InstrumentedProducerBuilder<string, string>>().Build());
            builder.Services.AddHostedService<KafkaOrderConsumer>();
        }

        var app = builder.Build();
        app.MapControllers();
        app.MapGrpcService<GreeterService>();

        app.MapGet("/users/{id:int}", (int id, ILogger<SampleSettings> logger) =>
        {
            logger.LogInformation("user {UserId} requested", id);
            return Results.Json(new { id, name = "user" + id });
        });

        app.MapGet("/headers", (HttpRequest req) => Results.Json(new
        {
            traceparent = req.Headers["traceparent"].ToString(),
            tracestate = req.Headers["tracestate"].ToString(),
        }));

        app.MapGet("/outbound", async (IHttpClientFactory factory) =>
        {
            var client = factory.CreateClient("self");
            var body = await client.GetStringAsync("/headers");
            return Results.Text(body, "application/json");
        });

        app.MapGet("/error", (Func<IResult>)(() => throw new InvalidOperationException("boom from sample app")));

        app.MapGet("/grpc", async () =>
        {
            using var channel = GrpcChannel.ForAddress($"http://127.0.0.1:{settings.GrpcPort}");
            var client = new Greeter.GreeterClient(channel);
            var reply = await client.SayHelloAsync(new HelloRequest { Name = "openlog" });
            return Results.Text(reply.Message);
        });

        app.MapGet("/db/pg", async () =>
        {
            await using var conn = new NpgsqlConnection(settings.Postgres ?? throw new InvalidOperationException("PG_CONNECTION not set"));
            await conn.OpenAsync();
            await using var cmd = new NpgsqlCommand("SELECT 42 AS answer WHERE 'secret' = 'secret' AND 1 = 1", conn);
            var v = await cmd.ExecuteScalarAsync();
            return Results.Text(Convert.ToString(v, System.Globalization.CultureInfo.InvariantCulture) ?? "");
        });

        app.MapGet("/db/ef", async ([FromServices] ShopContext db) =>
        {
            await db.Database.EnsureCreatedAsync();
            if (!await db.Products.AnyAsync())
            {
                db.Products.Add(new Product { Name = "keyboard", Price = 49.90m });
                await db.SaveChangesAsync();
            }
            var cheap = await db.Products.Where(p => p.Price > 10.5m && p.Name != "secret-name").OrderBy(p => p.Id).Take(5).ToListAsync();
            return Results.Json(cheap.Select(p => p.Name));
        });

        app.MapGet("/db/mysql", async () =>
        {
            await using var conn = new MySqlConnection(settings.MySql ?? throw new InvalidOperationException("MYSQL_CONNECTION not set"));
            await conn.OpenAsync();
            await using var cmd = new MySqlCommand("SELECT 7 FROM DUAL WHERE \"secret\" = 'secret' AND 3 IN (1, 2, 3)", conn);
            var v = await cmd.ExecuteScalarAsync();
            return Results.Text(Convert.ToString(v, System.Globalization.CultureInfo.InvariantCulture) ?? "");
        });

        app.MapGet("/db/sqlserver", async () =>
        {
            await using var conn = new SqlConnection(settings.SqlServer ?? throw new InvalidOperationException("SQLSERVER_CONNECTION not set"));
            await conn.OpenAsync();
            await using var cmd = new SqlCommand("SELECT 42 AS answer WHERE 'secret' = 'secret' AND 1 IN (1, 2)", conn);
            var v = await cmd.ExecuteScalarAsync();
            return Results.Text(Convert.ToString(v, System.Globalization.CultureInfo.InvariantCulture) ?? "");
        });

        app.MapGet("/messaging/masstransit/{id:int}", async (int id, [FromServices] IPublishEndpoint publish) =>
        {
            await publish.Publish(new OrderPlaced(id));
            return Results.Text("published");
        });

        app.MapGet("/messaging/kafka/{id:int}", async (int id, [FromServices] IProducer<string, string> producer) =>
        {
            var r = await producer.ProduceAsync(KafkaTopic, new Message<string, string> { Key = "order", Value = "order-" + id });
            return Results.Text(r.Status.ToString());
        });

        app.MapGet("/db/redis", async ([FromServices] IConnectionMultiplexer redis) =>
        {
            var db = redis.GetDatabase();
            await db.StringSetAsync("product:42", "secret-value");
            var v = await db.StringGetAsync("product:42");
            return Results.Text(v.ToString());
        });

        return app;
    }

    public static async Task Main(string[] args)
    {
        var app = Build(SampleSettings.FromEnvironment(), null, args);
        await app.RunAsync();
    }
}

using System;
using System.Collections.Generic;
using System.Linq;
using System.Net;
using System.Net.Http;
using System.Threading.Tasks;
using Grpc.Core;
using Grpc.Net.Client;
using Microsoft.AspNetCore.Builder;
using Microsoft.AspNetCore.Hosting;
using Microsoft.AspNetCore.Http;
using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.Server.Kestrel.Core;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Logging;
using MySqlConnector;
using Npgsql;
using OpenLog.Agent;
using OpenLog.SampleApp.Grpc;
using StackExchange.Redis;

namespace OpenLog.SampleApp;

/// <summary>Ports and backends of the sample app (environment: HTTP_PORT, GRPC_PORT, PG_CONNECTION, MYSQL_CONNECTION, REDIS_CONNECTION).</summary>
public sealed class SampleSettings
{
    public int HttpPort { get; set; } = 8080;
    public int GrpcPort { get; set; } = 8081;
    public string? Postgres { get; set; }
    public string? MySql { get; set; }
    public string? Redis { get; set; }
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

        if (settings.Agent) builder.Services.AddOpenLog(configureAgent);
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

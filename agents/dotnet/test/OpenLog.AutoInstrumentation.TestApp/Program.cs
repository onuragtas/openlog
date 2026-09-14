using System;
using System.Net.Http;
using Microsoft.AspNetCore.Builder;
using Microsoft.AspNetCore.Hosting;
using Microsoft.AspNetCore.Http;
using Microsoft.Extensions.Logging;

// HTTP_PORT: listen port. Routes: /health, /users/{id:int} (logs), /outbound (HttpClient to /headers), /headers, /error.
var port = Environment.GetEnvironmentVariable("HTTP_PORT") ?? "8080";
var builder = WebApplication.CreateBuilder(args);
builder.WebHost.UseUrls("http://127.0.0.1:" + port);
builder.Logging.SetMinimumLevel(LogLevel.Information);
builder.Logging.AddFilter("Microsoft", LogLevel.Warning);
var app = builder.Build();
var http = new HttpClient { BaseAddress = new Uri("http://127.0.0.1:" + port) };

app.MapGet("/health", () => "ok");
app.MapGet("/users/{id:int}", (int id, ILogger<Program> logger) =>
{
    logger.LogInformation("user {UserId} requested", id);
    return Results.Json(new { id });
});
app.MapGet("/headers", (HttpRequest req) => Results.Json(new
{
    traceparent = req.Headers["traceparent"].ToString(),
    tracestate = req.Headers["tracestate"].ToString(),
}));
app.MapGet("/outbound", async () => Results.Text(await http.GetStringAsync("/headers"), "application/json"));
app.MapGet("/error", (Func<IResult>)(() => throw new InvalidOperationException("boom from auto-instrumented app")));

app.Run();

public partial class Program { }

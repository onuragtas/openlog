# dotnet-shop: the .NET agent's sample app (agents/dotnet/samples/OpenLog.SampleApp: ASP.NET Core minimal API + MVC,
# HttpClient, Npgsql/EF Core, StackExchange.Redis, ILogger) with OpenLog.Agent (project reference, as the NuGet package).
# Build contexts (test/localagents/docker-compose.yml): dotnetagent = ../../agents/dotnet.

FROM mcr.microsoft.com/dotnet/sdk:8.0 AS build
WORKDIR /src
COPY --from=dotnetagent Directory.Build.props Directory.Packages.props ./
COPY --from=dotnetagent src ./src
COPY --from=dotnetagent samples/OpenLog.SampleApp ./samples/OpenLog.SampleApp
# the sample multi-targets net8.0;net9.0: only net8.0 is evaluated with the 8.0 SDK
RUN dotnet publish samples/OpenLog.SampleApp/OpenLog.SampleApp.csproj -c Release -p:TargetFrameworks=net8.0 -f net8.0 -o /out

FROM mcr.microsoft.com/dotnet/aspnet:8.0
WORKDIR /app
COPY --from=build /out/ ./
# The sample listens on 127.0.0.1:HTTP_PORT; SAMPLE_LISTEN_ANY adds 0.0.0.0:HTTP_PORT+1000 (here 9080).
ENV HTTP_PORT=8080 GRPC_PORT=0 SAMPLE_LISTEN_ANY=true
USER app
EXPOSE 9080
ENTRYPOINT ["dotnet", "OpenLog.SampleApp.dll"]

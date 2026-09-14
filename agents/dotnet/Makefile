# .NET agent (OpenLog.Agent). Every target runs inside an SDK container, so no local .NET SDK is needed
# (DOTNET=local runs dotnet directly, e.g. in CI).
DOTNET_VERSION ?= 9.0
# volume and compose name prefix (openlog-m4-dotnet-nuget, -artifacts, -otelauto; project openlog-m4-dotnet-test)
DOTNET_TEST_VOLUME_PREFIX ?= openlog-m4-dotnet
NUGET_VOLUME ?= $(DOTNET_TEST_VOLUME_PREFIX)-nuget
ARTIFACTS_VOLUME ?= $(DOTNET_TEST_VOLUME_PREFIX)-artifacts
TFM := net$(DOTNET_VERSION)
# the 8.0 SDK cannot evaluate the net9.0 targets, so only net8.0 is built there
TFM_PROPS := $(if $(filter 8.0,$(DOTNET_VERSION)),-p:TargetFrameworks=net8.0,)
COMPOSE_PROJECT ?= openlog-m4-dotnet-test
COMPOSE := DOTNET_TEST_VOLUME_PREFIX=$(DOTNET_TEST_VOLUME_PREFIX) docker compose -p $(COMPOSE_PROJECT) -f test/docker-compose.yml
# SQL Server for the SqlClient test: mssql/server on amd64; azure-sql-edge (arm64 image) otherwise
SQLSERVER_IMAGE ?= $(if $(filter x86_64 amd64,$(shell uname -m)),mcr.microsoft.com/mssql/server:2022-latest,mcr.microsoft.com/azure-sql-edge:latest)
VERSION ?= 0.1.9

ifeq ($(DOTNET),local)
RUN := sh -c
else
RUN := docker run --rm -v "$(CURDIR)":/src -v "$(CURDIR)/../node/test/interop":/node/test/interop:ro \
	-v $(NUGET_VOLUME):/root/.nuget/packages -v $(ARTIFACTS_VOLUME):/artifacts \
	-e OPENLOG_DOTNET_ARTIFACTS=/artifacts -e DOTNET_CLI_TELEMETRY_OPTOUT=1 -e DOTNET_NOLOGO=1 \
	-w /src mcr.microsoft.com/dotnet/sdk:$(DOTNET_VERSION) sh -c
endif

.PHONY: build build-netfx test test-unit test-integration test-autoinstrumentation pack bench clean

build:
	$(RUN) 'dotnet build src/OpenLog.Agent/OpenLog.Agent.csproj -c Release'

# .NET Framework 4.8 / ASP.NET 4.x (System.Web + TelemetryHttpModule) against the netstandard2.0 build: compile only
build-netfx:
	$(RUN) 'dotnet build samples/OpenLog.AspNetFramework.Sample/OpenLog.AspNetFramework.Sample.csproj -c Release'

# unit tests (Go sampler fixtures, sanitizer, config, resource, processors) + end-to-end tests without databases
test: test-unit
	$(RUN) 'dotnet test test/OpenLog.Agent.IntegrationTests/OpenLog.Agent.IntegrationTests.csproj -f $(TFM) $(TFM_PROPS)'

test-unit:
	$(RUN) 'dotnet test test/OpenLog.Agent.Tests/OpenLog.Agent.Tests.csproj -f $(TFM) $(TFM_PROPS)'

# end-to-end tests against real PostgreSQL, MySQL, Redis, RabbitMQ (MassTransit), Kafka and SQL Server
# (compose project openlog-m4-dotnet-test, removed afterwards)
test-integration:
	SQLSERVER_IMAGE=$(SQLSERVER_IMAGE) $(COMPOSE) --profile sqlserver up -d --wait postgres mysql redis rabbitmq kafka sqlserver
	DOTNET_VERSION=$(DOTNET_VERSION) SQLSERVER_CONNECTION="Server=sqlserver,1433;User ID=sa;Password=Openlog-Test-1;TrustServerCertificate=true" \
		$(COMPOSE) --profile runner run --rm runner; status=$$?; $(COMPOSE) --profile sqlserver down -v; exit $$status

# the openlog plugin under the pinned OpenTelemetry .NET automatic instrumentation (downloads the release once)
test-autoinstrumentation:
	DOTNET_VERSION=$(DOTNET_VERSION) DOTNET_TEST_VOLUME_PREFIX=$(DOTNET_TEST_VOLUME_PREFIX) test/autoinstrumentation.sh

pack:
	$(RUN) 'dotnet pack src/OpenLog.Agent/OpenLog.Agent.csproj -c Release -p:OpenLogVersion=$(VERSION) -p:PackageOutputPath=/src/artifacts/nupkg'

bench:
	$(RUN) 'dotnet run -c Release --project bench/OpenLog.Agent.Bench/OpenLog.Agent.Bench.csproj -f $(TFM) $(TFM_PROPS)'

clean:
	rm -rf artifacts bin obj

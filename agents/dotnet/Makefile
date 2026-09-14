# .NET agent (OpenLog.Agent). Every target runs inside an SDK container, so no local .NET SDK is needed
# (DOTNET=local runs dotnet directly, e.g. in CI).
DOTNET_VERSION ?= 9.0
NUGET_VOLUME ?= openlog-m4-dotnet-nuget
ARTIFACTS_VOLUME ?= openlog-m4-dotnet-artifacts
TFM := net$(DOTNET_VERSION)
# the 8.0 SDK cannot evaluate the net9.0 targets, so only net8.0 is built there
TFM_PROPS := $(if $(filter 8.0,$(DOTNET_VERSION)),-p:TargetFrameworks=net8.0,)
COMPOSE := docker compose -p openlog-m4-dotnet-test -f test/docker-compose.yml
VERSION ?= 0.1.9

ifeq ($(DOTNET),local)
RUN := sh -c
else
RUN := docker run --rm -v "$(CURDIR)":/src -v "$(CURDIR)/../node/test/interop":/node/test/interop:ro \
	-v $(NUGET_VOLUME):/root/.nuget/packages -v $(ARTIFACTS_VOLUME):/artifacts \
	-e OPENLOG_DOTNET_ARTIFACTS=/artifacts -e DOTNET_CLI_TELEMETRY_OPTOUT=1 -e DOTNET_NOLOGO=1 \
	-w /src mcr.microsoft.com/dotnet/sdk:$(DOTNET_VERSION) sh -c
endif

.PHONY: build test test-unit test-integration pack bench clean

build:
	$(RUN) 'dotnet build src/OpenLog.Agent/OpenLog.Agent.csproj -c Release'

# unit tests (Go sampler fixtures, sanitizer, config, resource, processors) + end-to-end tests without databases
test: test-unit
	$(RUN) 'dotnet test test/OpenLog.Agent.IntegrationTests/OpenLog.Agent.IntegrationTests.csproj -f $(TFM) $(TFM_PROPS)'

test-unit:
	$(RUN) 'dotnet test test/OpenLog.Agent.Tests/OpenLog.Agent.Tests.csproj -f $(TFM) $(TFM_PROPS)'

# end-to-end tests against real PostgreSQL, MySQL and Redis (compose project openlog-m4-dotnet-test, removed afterwards)
test-integration:
	$(COMPOSE) up -d --wait postgres mysql redis
	DOTNET_VERSION=$(DOTNET_VERSION) $(COMPOSE) --profile runner run --rm runner; status=$$?; $(COMPOSE) down -v; exit $$status

pack:
	$(RUN) 'dotnet pack src/OpenLog.Agent/OpenLog.Agent.csproj -c Release -p:OpenLogVersion=$(VERSION) -o /src/artifacts/nupkg'

bench:
	$(RUN) 'dotnet run -c Release --project bench/OpenLog.Agent.Bench/OpenLog.Agent.Bench.csproj -f $(TFM) $(TFM_PROPS)'

clean:
	rm -rf artifacts bin obj

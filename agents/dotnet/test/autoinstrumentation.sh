#!/usr/bin/env bash
# End-to-end test of the openlog plugin under the OpenTelemetry .NET automatic instrumentation (zero-code install):
# downloads the pinned automatic instrumentation release (sha256-verified), installs OpenLog.Agent.dll into its net/
# directory, publishes test/OpenLog.AutoInstrumentation.TestApp (no OpenTelemetry reference) and runs
# AutoInstrumentationTests, which start the app under the profiler + startup hook against the OTLP capture server.
#
#   test/autoinstrumentation.sh                 # in mcr.microsoft.com/dotnet/sdk:$DOTNET_VERSION (default 9.0)
#   DOTNET=local test/autoinstrumentation.sh    # with the host SDK (CI)
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
DOTNET_VERSION="${DOTNET_VERSION:-9.0}"
# Pinned release; the sha256 of its Linux glibc archives (GitHub release asset digests) is below.
AUTO_VERSION=1.16.0

if [ "${DOTNET:-docker}" != local ]; then
  prefix="${DOTNET_TEST_VOLUME_PREFIX:-openlog-m4-dotnet}"
  exec docker run --rm -v "$here":/src -v "$here/../node/test/interop":/node/test/interop:ro \
    -v "$prefix-nuget":/root/.nuget/packages -v "$prefix-artifacts":/artifacts -v "$prefix-otelauto":/otelauto \
    -e OPENLOG_DOTNET_ARTIFACTS=/artifacts -e DOTNET_CLI_TELEMETRY_OPTOUT=1 -e DOTNET_NOLOGO=1 \
    -e DOTNET=local -e DOTNET_VERSION="$DOTNET_VERSION" -e OTEL_AUTO_CACHE=/otelauto -e OPENLOG_TEST_VERBOSE \
    -w /src "mcr.microsoft.com/dotnet/sdk:$DOTNET_VERSION" \
    sh -c 'command -v unzip >/dev/null || { apt-get -qq update && apt-get -qq install -y unzip >/dev/null; }; exec bash test/autoinstrumentation.sh'
fi

case "$(uname -m)" in
  x86_64 | amd64) arch=x64 sha256=a0c52a23366091d8cd8d62424806a6a96734aee869ee4da2fc21f7925fae5381 ;;
  aarch64 | arm64) arch=arm64 sha256=ef1b39f1379db463533f97ab5a1a3ebcb2bf92b219eed0aff8faf3a899c1ae90 ;;
  *) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac
cache="${OTEL_AUTO_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/openlog-otel-dotnet-auto}/$AUTO_VERSION-$arch"
if [ ! -f "$cache/net/OpenTelemetry.AutoInstrumentation.StartupHook.dll" ]; then
  zip="opentelemetry-dotnet-instrumentation-linux-glibc-$arch.zip"
  tmp="$(mktemp -d)"
  curl -fsSL -o "$tmp/$zip" "https://github.com/open-telemetry/opentelemetry-dotnet-instrumentation/releases/download/v$AUTO_VERSION/$zip"
  echo "$sha256  $tmp/$zip" | sha256sum -c -
  rm -rf "$cache" && mkdir -p "$cache"
  unzip -q "$tmp/$zip" -d "$cache"
  rm -rf "$tmp"
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cp -R "$cache" "$work/home"
cd "$here"
props=""
if [ "$DOTNET_VERSION" = 8.0 ]; then props="-p:TargetFrameworks=net8.0"; fi
# the plugin: OpenLog.Agent.dll (net8.0) next to the automatic instrumentation's own assemblies
dotnet build src/OpenLog.Agent/OpenLog.Agent.csproj -c Release -f net8.0 -o "$work/plugin"
cp "$work/plugin/OpenLog.Agent.dll" "$work/home/net/"
# shellcheck disable=SC2086
dotnet publish test/OpenLog.AutoInstrumentation.TestApp/OpenLog.AutoInstrumentation.TestApp.csproj -c Release \
  -f "net$DOTNET_VERSION" $props -o "$work/app"
# shellcheck disable=SC2086
OPENLOG_TEST_OTEL_AUTO_HOME="$work/home" OPENLOG_TEST_AUTO_APP="$work/app/OpenLog.AutoInstrumentation.TestApp.dll" \
  dotnet test test/OpenLog.Agent.IntegrationTests/OpenLog.Agent.IntegrationTests.csproj -c Release -f "net$DOTNET_VERSION" $props \
  --filter "FullyQualifiedName~AutoInstrumentationTests"

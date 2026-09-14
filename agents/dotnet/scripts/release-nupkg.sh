#!/usr/bin/env bash
# Builds the NuGet package OpenLog.Agent.<version>.nupkg (+ .sha256, and the .snupkg symbols package) into a release
# directory. `make release-local` lists the .nupkg in the signed manifest (component dotnet-agent, format nupkg);
# release.yml attaches it to the GitHub release, so it installs from a local NuGet source without nuget.org, and pushes
# the same file to nuget.org when NUGET_API_KEY is configured.
#
#   agents/dotnet/scripts/release-nupkg.sh 0.9.0 dist/v0.9.0             # mcr.microsoft.com/dotnet/sdk:8.0 (Docker)
#   DOTNET_BUILD=local agents/dotnet/scripts/release-nupkg.sh 0.9.0 DIR  # dotnet on the host (CI with setup-dotnet)
#   RUN_TESTS=0 …                                                       # skip the unit tests
#
# The sources are copied first, so the build never writes into the checkout.
set -euo pipefail
version="${1:?version}"
out="${2:?release directory}"
here="$(cd "$(dirname "$0")/.." && pwd)"
file="OpenLog.Agent.$version.nupkg"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
export VERSION="$version" RUN_TESTS="${RUN_TESTS:-1}" DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_NOLOGO=1
# the unit tests read the Go sampler fixtures of the Node.js agent
fixtures="$here/../node/test/interop"

# shellcheck disable=SC2016 # expanded by the inner shell
build='set -e
if [ "$RUN_TESTS" = 1 ]; then
  dotnet test test/OpenLog.Agent.Tests/OpenLog.Agent.Tests.csproj -c Release -p:OpenLogVersion="$VERSION" $TEST_PROPS
fi
# -p:PackageOutputPath instead of -o: the .NET 10 SDK passes -o to MSBuild without --property: (MSB1008)
dotnet pack src/OpenLog.Agent/OpenLog.Agent.csproj -c Release -p:OpenLogVersion="$VERSION" -p:PackageOutputPath="$OUT"'

if [ "${DOTNET_BUILD:-docker}" = local ]; then
  work="$(mktemp -d)"
  trap 'rm -rf "$work"' EXIT
  mkdir -p "$work/dotnet" "$work/node/test/interop"
  tar -C "$here" --exclude=bin --exclude=obj --exclude=artifacts -cf - . | tar -C "$work/dotnet" -xf -
  cp -R "$fixtures/." "$work/node/test/interop/"
  (cd "$work/dotnet" && OUT="$out" TEST_PROPS="" sh -c "$build")
else
  sdk="${DOTNET_SDK_VERSION:-8.0}"
  props=""
  # the 8.0 SDK cannot evaluate the net9.0 test targets
  if [ "$sdk" = 8.0 ]; then props="-p:TargetFrameworks=net8.0"; fi
  docker run --rm -v "$here":/src/dotnet:ro -v "$fixtures":/src/node/test/interop:ro -v "$out":/out \
    -v "${DOTNET_PACK_CACHE:-openlog-dotnet-pack-nuget}":/root/.nuget/packages \
    -e VERSION -e RUN_TESTS -e OUT=/out -e TEST_PROPS="$props" -e DOTNET_CLI_TELEMETRY_OPTOUT -e DOTNET_NOLOGO \
    "mcr.microsoft.com/dotnet/sdk:$sdk" sh -c '
      set -e
      mkdir -p /work/dotnet /work/node/test/interop
      tar -C /src/dotnet --exclude=bin --exclude=obj --exclude=artifacts -cf - . | tar -C /work/dotnet -xf -
      cp -R /src/node/test/interop/. /work/node/test/interop/
      cd /work/dotnet
      sh -c "$1"
      chown "$(stat -c %u:%g /out)" /out/OpenLog.Agent."$VERSION".*nupkg' release-nupkg "$build"
fi

cd "$out"
if command -v sha256sum >/dev/null 2>&1; then sha256sum "$file" > "$file.sha256"; else shasum -a 256 "$file" > "$file.sha256"; fi
cat "$file.sha256"

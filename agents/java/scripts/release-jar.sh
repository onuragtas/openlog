#!/usr/bin/env bash
# Builds openlog-javaagent-<version>.jar + .sha256 (unit tests included) and copies both into a release directory,
# where `make release-local` lists the jar in the signed manifest (component java-agent, format jar).
#
#   agents/java/scripts/release-jar.sh 0.9.0 dist/v0.9.0            # Gradle in eclipse-temurin:21-jdk (Docker)
#   JAVA_BUILD=local agents/java/scripts/release-jar.sh 0.9.0 DIR   # Gradle wrapper on the host (CI with setup-java)
set -euo pipefail
version="${1:?version}"
out="${2:?release directory}"
here="$(cd "$(dirname "$0")/.." && pwd)"
jar="openlog-javaagent-$version.jar"
tasks=(--no-daemon --console=plain "-Pversion=$version" :extension:test :agentJar :agentJarChecksum)
mkdir -p "$out"
out="$(cd "$out" && pwd)"

if [ "${JAVA_BUILD:-docker}" = local ]; then
  (cd "$here" && ./gradlew "${tasks[@]}")
  cp "$here/build/libs/$jar" "$here/build/libs/$jar.sha256" "$out/"
else
  # sources are copied into a volume first, so the build never writes into the checkout
  docker run --rm \
    -v "$here":/src/java:ro -v "$here/../node/test/interop":/src/node/test/interop:ro \
    -v "${JAVA_TEST_PROJECT:-openlog-m4-java}-gradle":/gradle -v "$out":/out -e GRADLE_USER_HOME=/gradle \
    "eclipse-temurin:${JAVA_TEST_JDK:-21}-jdk" sh -c '
      set -e
      mkdir -p /work/java /work/node/test/interop
      cp /src/node/test/interop/go-sampler-fixtures.json /work/node/test/interop/
      tar -C /src/java --exclude=build --exclude=.gradle --exclude=.kotlin -cf - . | tar -C /work/java -xf -
      cd /work/java && ./gradlew "$@"
      cp "build/libs/'"$jar"'" "build/libs/'"$jar"'.sha256" /out/
      chown "$(stat -c %u:%g /out)" "/out/'"$jar"'" "/out/'"$jar"'.sha256"' release-jar "${tasks[@]}"
fi
(cd "$out" && sha256sum -c "$jar.sha256")

import java.security.MessageDigest

plugins {
  base
}

val otelJavaagentVersion = providers.gradleProperty("otelJavaagentVersion").get()

// The upstream OpenTelemetry Java agent, unmodified.
val upstreamAgent: Configuration = configurations.create("upstreamAgent") {
  isTransitive = false
}

dependencies {
  upstreamAgent("io.opentelemetry.javaagent:opentelemetry-javaagent:$otelJavaagentVersion")
}

// openlog-javaagent-<version>.jar: the upstream agent with the openlog extension embedded in its
// extensions/ directory (loaded by the agent like -Dotel.javaagent.extensions, no shading needed).
val agentJar = tasks.register<Jar>("agentJar") {
  group = "build"
  description = "Builds build/libs/openlog-javaagent-<version>.jar"
  val extensionJar = project(":extension").tasks.named<Jar>("jar")
  dependsOn(upstreamAgent, extensionJar)
  archiveBaseName.set("openlog-javaagent")
  archiveVersion.set(project.version.toString())
  destinationDirectory.set(layout.buildDirectory.dir("libs"))
  isPreserveFileTimestamps = false
  isReproducibleFileOrder = true
  duplicatesStrategy = DuplicatesStrategy.FAIL
  from(zipTree(upstreamAgent.singleFile)) {
    exclude("META-INF/MANIFEST.MF")
  }
  from(extensionJar.flatMap { it.archiveFile }) {
    into("extensions")
    rename { "openlog-extension.jar" }
  }
  val version = project.version.toString()
  doFirst {
    manifest.from(zipTree(upstreamAgent.singleFile).matching { include("META-INF/MANIFEST.MF") }.singleFile)
    manifest.attributes(
      mapOf(
        "Openlog-Javaagent-Version" to version,
        "Openlog-Upstream-Javaagent-Version" to otelJavaagentVersion,
      ),
    )
  }
}

val agentJarChecksum = tasks.register("agentJarChecksum") {
  group = "build"
  description = "Writes openlog-javaagent-<version>.jar.sha256 (sha256sum format)"
  dependsOn(agentJar)
  val jar = agentJar.flatMap { it.archiveFile }
  inputs.file(jar)
  val out = jar.map { File(it.asFile.path + ".sha256") }
  outputs.file(out)
  doLast {
    val f = jar.get().asFile
    val digest = MessageDigest.getInstance("SHA-256")
    f.inputStream().use { input ->
      val buf = ByteArray(1 shl 16)
      while (true) {
        val n = input.read(buf)
        if (n < 0) break
        digest.update(buf, 0, n)
      }
    }
    val hex = digest.digest().joinToString("") { "%02x".format(it) }
    out.get().writeText("$hex  ${f.name}\n")
  }
}

tasks.named("assemble") {
  dependsOn(agentJar, agentJarChecksum)
}

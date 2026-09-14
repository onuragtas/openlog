// Integration tests: the sample applications run with openlog-javaagent-<version>.jar against an OTLP capture
// server and real PostgreSQL, MySQL, Redis and Kafka (test/docker-compose.yml). `./gradlew integrationTest`;
// `./gradlew bench` runs the overhead micro-benchmark.
plugins {
  java
}

val otelProtoVersion = providers.gradleProperty("otelProtoVersion").get()
val junitVersion = providers.gradleProperty("junitVersion").get()

dependencies {
  testImplementation("io.opentelemetry.proto:opentelemetry-proto:$otelProtoVersion")
  testImplementation(platform("org.junit:junit-bom:$junitVersion"))
  testImplementation("org.junit.jupiter:junit-jupiter")
  testRuntimeOnly("org.junit.platform:junit-platform-launcher")
}

tasks.compileTestJava {
  options.release.set(17)
  options.encoding = "UTF-8"
}

tasks.test {
  enabled = false
}

val agentJarFile = rootProject.layout.buildDirectory.file("libs/openlog-javaagent-${project.version}.jar")
val mvcDir = project(":testapps:mvc").layout.buildDirectory.dir("install/mvc")
val webfluxDir = project(":testapps:webflux").layout.buildDirectory.dir("install/webflux")

tasks.register<Test>("integrationTest") {
  group = "verification"
  description = "Runs the sample applications with the agent against an OTLP capture server"
  testClassesDirs = sourceSets["test"].output.classesDirs
  classpath = sourceSets["test"].runtimeClasspath
  useJUnitPlatform()
  dependsOn(":agentJar", ":testapps:mvc:installDist", ":testapps:webflux:installDist")
  systemProperty("openlog.agent.jar", agentJarFile.get().asFile.absolutePath)
  systemProperty("openlog.app.mvc", mvcDir.get().asFile.absolutePath)
  systemProperty("openlog.app.webflux", webfluxDir.get().asFile.absolutePath)
  outputs.upToDateWhen { false }
  testLogging {
    events("passed", "failed", "skipped")
    exceptionFormat = org.gradle.api.tasks.testing.logging.TestExceptionFormat.FULL
    showStandardStreams = System.getenv("OPENLOG_TEST_VERBOSE") != null
  }
}

tasks.register<JavaExec>("bench") {
  group = "verification"
  description = "Overhead micro-benchmark (no agent / sampled / not sampled)"
  classpath = sourceSets["test"].runtimeClasspath
  mainClass.set("io.github.onuragtas.openlog.it.OverheadBench")
  dependsOn(":agentJar", ":testapps:mvc:installDist", "testClasses")
  systemProperty("openlog.agent.jar", agentJarFile.get().asFile.absolutePath)
  systemProperty("openlog.app.mvc", mvcDir.get().asFile.absolutePath)
}

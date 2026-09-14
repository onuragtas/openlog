plugins {
  `java-library`
}

val otelSdkVersion = providers.gradleProperty("otelSdkVersion").get()
val otelJavaagentVersion = providers.gradleProperty("otelJavaagentVersion").get()
val junitVersion = providers.gradleProperty("junitVersion").get()
val jacksonVersion = providers.gradleProperty("jacksonVersion").get()

dependencies {
  // Provided by the javaagent at runtime.
  compileOnly(platform("io.opentelemetry:opentelemetry-bom:$otelSdkVersion"))
  compileOnly("io.opentelemetry:opentelemetry-sdk")
  compileOnly("io.opentelemetry:opentelemetry-sdk-extension-autoconfigure-spi")

  testImplementation(platform("io.opentelemetry:opentelemetry-bom:$otelSdkVersion"))
  testImplementation("io.opentelemetry:opentelemetry-sdk")
  testImplementation("io.opentelemetry:opentelemetry-sdk-testing")
  testImplementation("io.opentelemetry:opentelemetry-sdk-extension-autoconfigure")
  testImplementation("com.fasterxml.jackson.core:jackson-databind:$jacksonVersion")
  testImplementation(platform("org.junit:junit-bom:$junitVersion"))
  testImplementation("org.junit.jupiter:junit-jupiter")
  testRuntimeOnly("org.junit.platform:junit-platform-launcher")
}

tasks.compileJava {
  // The agent supports Java 8 applications; the extension runs inside them.
  options.release.set(8)
  options.compilerArgs.addAll(listOf("-Xlint:all,-options", "-Werror"))
  options.encoding = "UTF-8"
}

tasks.compileTestJava {
  options.release.set(17)
  options.encoding = "UTF-8"
}

tasks.processResources {
  val props = mapOf("version" to project.version.toString(), "upstream" to otelJavaagentVersion)
  inputs.properties(props)
  filesMatching("**/version.properties") {
    expand(props)
  }
}

tasks.jar {
  archiveBaseName.set("openlog-javaagent-extension")
  manifest {
    attributes(
      "Implementation-Title" to "openlog-javaagent-extension",
      "Implementation-Version" to project.version.toString(),
    )
  }
}

tasks.test {
  useJUnitPlatform()
  // Cross-language sampler fixtures generated from the Go agent (shared with agents/node).
  systemProperty(
    "openlog.test.fixtures",
    rootProject.layout.projectDirectory.file("../node/test/interop/go-sampler-fixtures.json").asFile.absolutePath,
  )
  testLogging {
    events("failed")
    exceptionFormat = org.gradle.api.tasks.testing.logging.TestExceptionFormat.FULL
  }
}

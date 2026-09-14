// Quarkus sample (JVM mode, build/quarkus-app/quarkus-run.jar): Quarkus REST (RESTEasy Reactive), plain JDBC on
// PostgreSQL. Built with the Quarkus Gradle plugin (quarkusBuild).
plugins {
  java
  id("io.quarkus")
}

val quarkusVersion = providers.gradleProperty("quarkusVersion").get()
val postgresqlVersion = providers.gradleProperty("postgresqlVersion").get()

dependencies {
  implementation(enforcedPlatform("io.quarkus.platform:quarkus-bom:$quarkusVersion"))
  implementation("io.quarkus:quarkus-rest")
  implementation("org.postgresql:postgresql:$postgresqlVersion")
}

tasks.withType<JavaCompile>().configureEach {
  options.release.set(17)
  options.encoding = "UTF-8"
  options.compilerArgs.add("-parameters")
}

// no tests in the sample; keep the plugin's test tasks from resolving test dependencies
tasks.named("test") {
  enabled = false
}

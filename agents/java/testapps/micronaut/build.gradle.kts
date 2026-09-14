// Micronaut 4 sample (Netty HTTP server), plain JDBC on PostgreSQL. Annotation processing without the Micronaut
// Gradle plugin; runs as a plain classpath application (installDist).
plugins {
  application
}

val micronautVersion = providers.gradleProperty("micronautVersion").get()
val postgresqlVersion = providers.gradleProperty("postgresqlVersion").get()

dependencies {
  annotationProcessor(platform("io.micronaut.platform:micronaut-platform:$micronautVersion"))
  annotationProcessor("io.micronaut:micronaut-inject-java")
  annotationProcessor("io.micronaut.serde:micronaut-serde-processor")
  implementation(platform("io.micronaut.platform:micronaut-platform:$micronautVersion"))
  implementation("io.micronaut:micronaut-http-server-netty")
  implementation("io.micronaut:micronaut-runtime")
  // JSON error responses (the server needs a JsonErrorResponseBodyProvider bean)
  implementation("io.micronaut.serde:micronaut-serde-jackson")
  runtimeOnly("org.slf4j:slf4j-simple")
  runtimeOnly("org.postgresql:postgresql:$postgresqlVersion")
}

tasks.compileJava {
  options.release.set(17)
  options.encoding = "UTF-8"
  options.compilerArgs.add("-parameters")
}

application {
  mainClass.set("io.github.onuragtas.openlog.testapp.micronaut.MicronautApp")
}

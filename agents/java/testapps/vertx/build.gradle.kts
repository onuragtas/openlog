// Vert.x Web 4.5 sample (router on the Vert.x HTTP server), plain JDBC on PostgreSQL in executeBlocking.
plugins {
  application
}

val vertxVersion = providers.gradleProperty("vertxVersion").get()
val postgresqlVersion = providers.gradleProperty("postgresqlVersion").get()

dependencies {
  implementation(platform("io.vertx:vertx-stack-depchain:$vertxVersion"))
  implementation("io.vertx:vertx-web")
  runtimeOnly("org.postgresql:postgresql:$postgresqlVersion")
}

tasks.compileJava {
  options.release.set(17)
  options.encoding = "UTF-8"
}

application {
  mainClass.set("io.github.onuragtas.openlog.testapp.vertx.VertxApp")
}

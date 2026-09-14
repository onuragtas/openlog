// JAX-RS sample: Jersey 3.1 (jakarta.ws.rs) as a servlet (ServletContainer) on embedded Jetty 12 (ee10), plain JDBC on PostgreSQL.
// With Jersey's Grizzly or Jetty handler containers the upstream agent creates the SERVER span but sets no http.route.
plugins {
  application
}

val jerseyVersion = providers.gradleProperty("jerseyVersion").get()
val jettyVersion = providers.gradleProperty("jettyVersion").get()
val postgresqlVersion = providers.gradleProperty("postgresqlVersion").get()

dependencies {
  implementation(platform("org.glassfish.jersey:jersey-bom:$jerseyVersion"))
  implementation("org.glassfish.jersey.containers:jersey-container-servlet-core")
  implementation("org.eclipse.jetty.ee10:jetty-ee10-servlet:$jettyVersion")
  implementation("org.glassfish.jersey.inject:jersey-hk2")
  runtimeOnly("org.postgresql:postgresql:$postgresqlVersion")
}

tasks.compileJava {
  options.release.set(17)
  options.encoding = "UTF-8"
}

application {
  mainClass.set("io.github.onuragtas.openlog.testapp.jaxrs.JaxrsApp")
}

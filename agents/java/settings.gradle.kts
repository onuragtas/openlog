pluginManagement {
  repositories {
    gradlePluginPortal()
    mavenCentral()
  }
  plugins {
    id("io.quarkus") version providers.gradleProperty("quarkusVersion").get()
  }
}

rootProject.name = "openlog-java-agent"

include(
  "extension",
  "testapps:mvc",
  "testapps:webflux",
  "testapps:jaxrs",
  "testapps:quarkus",
  "testapps:micronaut",
  "testapps:vertx",
  "integration-tests",
)

dependencyResolutionManagement {
  repositories {
    mavenCentral()
  }
}

rootProject.name = "openlog-java-agent"

include("extension", "testapps:mvc", "testapps:webflux", "integration-tests")

dependencyResolutionManagement {
  repositories {
    mavenCentral()
  }
}

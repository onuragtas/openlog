// Spring Boot 3 WebFlux sample with Log4j2 (instead of Logback).
plugins {
  application
}

val springBootVersion = providers.gradleProperty("springBootVersion").get()

configurations.all {
  exclude(group = "org.springframework.boot", module = "spring-boot-starter-logging")
}

dependencies {
  implementation(platform("org.springframework.boot:spring-boot-dependencies:$springBootVersion"))
  implementation("org.springframework.boot:spring-boot-starter-webflux")
  implementation("org.springframework.boot:spring-boot-starter-log4j2")
}

tasks.compileJava {
  options.release.set(17)
  options.compilerArgs.add("-parameters")
  options.encoding = "UTF-8"
}

application {
  mainClass.set("io.github.onuragtas.openlog.testapp.webflux.WebfluxApp")
}

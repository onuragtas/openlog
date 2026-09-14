// Spring Boot 3 Web MVC sample: JPA/Hibernate + JDBC on PostgreSQL, JDBC on MySQL, Redis (Lettuce + Jedis),
// Kafka client, gRPC, Logback. Run as a plain classpath application (installDist), like most deployments that
// are not fat jars; no Spring Boot Gradle plugin needed.
plugins {
  application
}

val springBootVersion = providers.gradleProperty("springBootVersion").get()
val grpcVersion = providers.gradleProperty("grpcVersion").get()

dependencies {
  implementation(platform("org.springframework.boot:spring-boot-dependencies:$springBootVersion"))
  implementation(platform("io.grpc:grpc-bom:$grpcVersion"))
  implementation("org.springframework.boot:spring-boot-starter-web")
  implementation("org.springframework.boot:spring-boot-starter-data-jpa")
  implementation("org.springframework.boot:spring-boot-starter-data-redis")
  implementation("redis.clients:jedis")
  implementation("org.apache.kafka:kafka-clients")
  implementation("io.grpc:grpc-stub")
  runtimeOnly("io.grpc:grpc-netty-shaded")
  runtimeOnly("org.postgresql:postgresql")
  runtimeOnly("com.mysql:mysql-connector-j")
}

tasks.compileJava {
  options.release.set(17)
  options.compilerArgs.add("-parameters")
  options.encoding = "UTF-8"
}

application {
  mainClass.set("io.github.onuragtas.openlog.testapp.mvc.MvcApp")
}

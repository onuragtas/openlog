# Java demo service: the Spring Boot WebFlux sample (agents/java/testapps/webflux) started with the openlog Java agent
# (-javaagent) built from agents/java. It generates its own traffic (DEMO_LOAD_RPS).
# Build context (test/localagents/docker-compose.yml): javaagent = ../../agents/java.

FROM eclipse-temurin:21-jdk AS build
WORKDIR /src
COPY --from=javaagent . .
RUN ./gradlew --no-daemon --console=plain -Pversion=0.0.0-local :agentJar :testapps:webflux:installDist \
 && mkdir -p /out \
 && cp build/libs/openlog-javaagent-0.0.0-local.jar /out/openlog-javaagent.jar \
 && cp -r testapps/webflux/build/install/webflux /out/app

FROM eclipse-temurin:21-jdk
COPY --from=build /out/ /opt/openlog/
RUN useradd --system --uid 10001 --home-dir /nonexistent app
USER app
ENV APP_PORT=8080
EXPOSE 8080
ENTRYPOINT ["java", "-javaagent:/opt/openlog/openlog-javaagent.jar", "-cp", "/opt/openlog/app/lib/*", "io.github.onuragtas.openlog.testapp.webflux.WebfluxApp"]

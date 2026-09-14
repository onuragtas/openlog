package io.github.onuragtas.openlog.it;

import static io.github.onuragtas.openlog.it.OtlpCapture.await;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import io.github.onuragtas.openlog.it.OtlpCapture.CapturedSpan;
import io.opentelemetry.proto.common.v1.KeyValue;
import io.opentelemetry.proto.trace.v1.Span;
import io.opentelemetry.proto.trace.v1.Status;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.util.HexFormat;
import java.util.List;
import java.util.Map;
import java.util.concurrent.ThreadLocalRandom;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;

/**
 * The same small application (GET /health, GET /users/{id} with a literal JDBC statement on PostgreSQL, GET /fail) on
 * another web framework: transaction naming from http.route, JDBC CLIENT spans in the request trace with the Go
 * sanitizer, error status, openlog resource.
 */
@TestInstance(TestInstance.Lifecycle.PER_CLASS)
abstract class FrameworkIT {
  static final Duration WAIT = Duration.ofSeconds(60);
  static final String SANITIZED = "SELECT ? AS answer WHERE ? = ? AND ? IN (?)";

  OtlpCapture capture;
  AppProcess app;
  Path infraDir;
  final String usersTrace = HexFormat.of().formatHex(randomBytes(16));
  final String failTrace = HexFormat.of().formatHex(randomBytes(16));

  /** Service name, e.g. java-jaxrs. */
  abstract String service();

  /** Starts the application with the agent. */
  abstract AppProcess startApp(Map<String, String> env) throws Exception;

  /** The route template the framework reports for /users/{id}; null when the upstream agent sets no http.route. */
  abstract String usersRoute();

  static byte[] randomBytes(int n) {
    byte[] b = new byte[n];
    ThreadLocalRandom.current().nextBytes(b);
    return b;
  }

  @BeforeAll
  void start() throws Exception {
    capture = OtlpCapture.start();
    infraDir = Files.createTempDirectory("openlog-infra-runtime");
    Map<String, String> env = TestEnv.agent(capture, service(), infraDir);
    env.put("OPENLOG_HTTP_IGNORE_PATHS", "/health");
    app = startApp(env);
    app.waitHealthy(Duration.ofSeconds(180));
    assertEquals(200, app.get("/users/7", "traceparent", "00-" + usersTrace + "-b7ad6b7169203331-01"));
    assertEquals(500, app.get("/fail", "traceparent", "00-" + failTrace + "-b7ad6b7169203331-01"));
  }

  @AfterAll
  void stop() throws Exception {
    if (app != null) {
      app.close();
    }
    if (capture != null) {
      capture.close();
    }
    if (infraDir != null) {
      Files.deleteIfExists(infraDir.resolve("host-id"));
      Files.deleteIfExists(infraDir);
    }
  }

  static boolean verbose() {
    return System.getenv("OPENLOG_TEST_VERBOSE") != null;
  }

  CapturedSpan serverSpan(String traceId) {
    return await(
        "SERVER span of trace " + traceId,
        WAIT,
        () -> capture.spans().stream()
            .filter(s -> s.span().getKind() == Span.SpanKind.SPAN_KIND_SERVER && s.traceId().equals(traceId))
            .findFirst()
            .orElse(null));
  }

  @Test
  void httpRouteNamesTheServerSpan() {
    CapturedSpan s = serverSpan(usersTrace);
    assertEquals(usersRoute(), s.attr("http.route"), s.toString());
    // without a route the span is named after the method only; the backend groups it by normalized url.path
    assertEquals(usersRoute() == null ? "GET" : "GET " + usersRoute(), s.span().getName());
    assertEquals("/users/7", s.attr("url.path"));
    assertEquals("200", s.attr("http.response.status_code", "http.status_code"));
    assertEquals("b7ad6b7169203331", OtlpCapture.hex(s.span().getParentSpanId()), "W3C parent extracted");
  }

  @Test
  void jdbcSpanIsInTheRequestTraceAndSanitized() {
    CapturedSpan db =
        await("JDBC span in the request trace", WAIT, () -> capture.spans().stream()
            .filter(s -> s.span().getKind() == Span.SpanKind.SPAN_KIND_CLIENT && s.traceId().equals(usersTrace))
            .filter(s -> "postgresql".equals(s.attr("db.system.name", "db.system")))
            .filter(s -> s.attr("db.query.text", "db.statement") != null)
            .findFirst()
            .orElse(null));
    assertEquals(SANITIZED, db.attr("db.query.text", "db.statement"), db.toString());
    for (CapturedSpan s : capture.spans()) {
      for (KeyValue kv : s.span().getAttributesList()) {
        assertFalse(OtlpCapture.value(kv.getValue()).contains("secret-7"), "attribute " + kv.getKey() + " of " + s);
      }
    }
  }

  @Test
  void errorsMarkTheServerSpan() {
    CapturedSpan fail = serverSpan(failTrace);
    assertEquals(Status.StatusCode.STATUS_CODE_ERROR, fail.span().getStatus().getCode(), fail.toString());
    assertEquals("500", fail.attr("http.response.status_code", "http.status_code"));
  }

  @Test
  void resourceAndIgnoredPaths() {
    CapturedSpan s = serverSpan(usersTrace);
    assertEquals(service(), s.resourceAttr("service.name"));
    assertEquals("openlog", s.resourceAttr("telemetry.distro.name"));
    assertEquals(TestEnv.HOST_ID, s.resourceAttr("host.id"));
    assertTrue(
        capture.spans().stream().noneMatch(x -> "/health".equals(x.attr("url.path", "http.target"))),
        "health check spans must be dropped");
  }

  /** Starts a classpath application (build/install/<name>/lib/*). */
  AppProcess classpathApp(String name, String mainClass, Map<String, String> env) throws Exception {
    return AppProcess.start(name, AppProcess.appDir(name), mainClass, AppProcess.agentJar(), env, List.of(), verbose());
  }
}

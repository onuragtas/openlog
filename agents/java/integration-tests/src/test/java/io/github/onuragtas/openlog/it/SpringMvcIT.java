package io.github.onuragtas.openlog.it;

import static io.github.onuragtas.openlog.it.OtlpCapture.await;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import io.github.onuragtas.openlog.it.OtlpCapture.CapturedLog;
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
import java.util.Objects;
import java.util.Set;
import java.util.UUID;
import java.util.concurrent.ThreadLocalRandom;
import java.util.stream.Collectors;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;

/** Spring Boot 3 Web MVC + JPA/Hibernate/JDBC (PostgreSQL, MySQL) + Lettuce/Jedis + Kafka + gRPC + Logback. */
@TestInstance(TestInstance.Lifecycle.PER_CLASS)
class SpringMvcIT {
  static final Duration WAIT = Duration.ofSeconds(60);

  OtlpCapture capture;
  AppProcess app;
  Path infraDir;
  final String orderItem = "order-" + UUID.randomUUID();
  final String redisKey = "k-" + UUID.randomUUID();
  final String remoteTraceId = HexFormat.of().formatHex(randomBytes(16));

  static byte[] randomBytes(int n) {
    byte[] b = new byte[n];
    ThreadLocalRandom.current().nextBytes(b);
    return b;
  }

  @BeforeAll
  void start() throws Exception {
    capture = OtlpCapture.start();
    infraDir = Files.createTempDirectory("openlog-infra-runtime");
    Map<String, String> env = TestEnv.agent(capture, "java-mvc", infraDir);
    env.put("OPENLOG_HTTP_IGNORE_PATHS", "/health");
    app =
        AppProcess.start(
            "mvc",
            AppProcess.appDir("mvc"),
            "io.github.onuragtas.openlog.testapp.mvc.MvcApp",
            AppProcess.agentJar(),
            env,
            List.of(),
            System.getenv("OPENLOG_TEST_VERBOSE") != null);
    app.waitHealthy(Duration.ofSeconds(180));
    assertEquals(200, app.get("/users/1"));
    assertEquals(200, app.get("/mysql/1"));
    assertEquals(200, app.get("/redis/" + redisKey));
    assertEquals(200, app.post("/orders?item=" + orderItem));
    assertEquals(200, app.get("/grpc/world"));
    assertEquals(500, app.get("/fail"));
    assertEquals(
        200,
        app.get("/users/2", "traceparent", "00-" + remoteTraceId + "-b7ad6b7169203331-01", "tracestate", "ot=th:c"));
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

  CapturedSpan serverSpan(String name) {
    return await(
        "SERVER span " + name,
        WAIT,
        () ->
            capture.spans().stream()
                .filter(s -> s.span().getKind() == Span.SpanKind.SPAN_KIND_SERVER && s.span().getName().equals(name))
                .findFirst()
                .orElse(null));
  }

  @Test
  void exportsOtlpHttpProtobufWithGzipAndLicenseKey() {
    serverSpan("GET /users/{id}");
    await("metrics and logs requests", WAIT, () -> capture.requests.stream().map(OtlpCapture.Request::path).collect(Collectors.toSet()).containsAll(Set.of("/v1/traces", "/v1/metrics", "/v1/logs")));
    for (OtlpCapture.Request r : capture.requests) {
      assertEquals(TestEnv.LICENSE_KEY, r.header("openlog-license-key"), r.path());
      assertEquals("gzip", r.header("Content-Encoding"), r.path());
      assertEquals("application/x-protobuf", r.header("Content-Type"), r.path());
    }
  }

  @Test
  void resourceAttributes() {
    CapturedSpan s = serverSpan("GET /users/{id}");
    assertEquals("java-mvc", s.resourceAttr("service.name"));
    assertEquals("1.2.3", s.resourceAttr("service.version"));
    assertEquals("test", s.resourceAttr("deployment.environment.name"));
    assertEquals(TestEnv.HOST_ID, s.resourceAttr("host.id"));
    assertEquals("openlog", s.resourceAttr("telemetry.distro.name"));
    assertNotNull(s.resourceAttr("telemetry.distro.version"));
    assertEquals("java", s.resourceAttr("telemetry.sdk.language"));
    assertNotNull(s.resourceAttr("process.pid"));
    assertNotNull(s.resourceAttr("process.runtime.version"));
    assertNull(s.resourceAttr("process.command_line"));
    assertNull(s.resourceAttr("process.command_args"));
    String cid = TestEnv.expectedContainerId();
    if (cid != null) {
      assertEquals(cid, s.resourceAttr("container.id"));
    }
    String arch = s.resourceAttr("host.arch");
    assertTrue(arch == null || Set.of("amd64", "arm64").contains(arch), arch);
  }

  @Test
  void httpRouteNamesTheServerSpan() {
    CapturedSpan s = serverSpan("GET /users/{id}");
    assertEquals("/users/{id}", s.attr("http.route"));
    assertEquals("200", s.attr("http.response.status_code", "http.status_code"));
    assertEquals("/mysql/{id}", serverSpan("GET /mysql/{id}").attr("http.route"));
    assertEquals("POST /orders", serverSpan("POST /orders").span().getName());
  }

  @Test
  void ignoredPathsHaveNoSpans() {
    serverSpan("GET /users/{id}");
    assertTrue(
        capture.spans().stream().noneMatch(s -> "/health".equals(s.attr("url.path", "http.target"))),
        "health check spans must be dropped");
  }

  static String statement(CapturedSpan s) {
    return s.attr("db.query.text", "db.statement");
  }

  static String dbSystem(CapturedSpan s) {
    return s.attr("db.system.name", "db.system");
  }

  List<CapturedSpan> dbSpans(String system) {
    return capture.spans().stream()
        .filter(s -> s.span().getKind() == Span.SpanKind.SPAN_KIND_CLIENT && system.equals(dbSystem(s)))
        .toList();
  }

  @Test
  void jdbcStatementsAreSanitizedLikeTheGoAgent() {
    String pg = "SELECT id, name FROM app_users WHERE name = ? AND id IN (?)";
    await("postgresql statement", WAIT, () -> dbSpans("postgresql").stream().anyMatch(s -> pg.equals(statement(s))));
    String my = "SELECT id, sku FROM items WHERE sku = ? AND qty > ?";
    await("mysql statement", WAIT, () -> dbSpans("mysql").stream().anyMatch(s -> my.equals(statement(s))));
    assertTrue(dbSpans("mysql").stream().anyMatch(s -> "INSERT IGNORE INTO items VALUES (?)".equals(statement(s))), dbSpans("mysql").toString());
    // JPA findById through Hibernate: CLIENT JDBC span below Hibernate's INTERNAL spans
    await("hibernate spans", WAIT, () -> capture.spans().stream().anyMatch(s -> s.scope().getName().contains("hibernate")));
    CapturedSpan users = serverSpan("GET /users/{id}");
    assertTrue(
        dbSpans("postgresql").stream().anyMatch(s -> s.traceId().equals(users.traceId()) && statement(s).contains("app_users")),
        "JDBC spans in the request trace");
    assertNoAttributeContains("alice", "abc-1");
  }

  @Test
  void redisCommandsAreSanitized() {
    await("lettuce and jedis spans", WAIT, () -> {
      List<CapturedSpan> r = dbSpans("redis");
      return r.stream().anyMatch(s -> s.scope().getName().contains("lettuce")) && r.stream().anyMatch(s -> s.scope().getName().contains("jedis"));
    });
    for (String scope : List.of("lettuce", "jedis")) {
      Set<String> statements =
          dbSpans("redis").stream().filter(s -> s.scope().getName().contains(scope)).map(SpringMvcIT::statement).filter(Objects::nonNull).collect(Collectors.toSet());
      assertTrue(statements.contains("SET ? ?"), scope + " " + statements);
      assertTrue(statements.contains("GET ?"), scope + " " + statements);
    }
    assertNoAttributeContains("secret-value");
    // the key is part of the request URL (url.path) but never of a Redis statement
    for (CapturedSpan s : dbSpans("redis")) {
      assertFalse(String.valueOf(statement(s)).contains(redisKey), s.toString());
    }
  }

  void assertNoAttributeContains(String... needles) {
    for (CapturedSpan s : capture.spans()) {
      for (KeyValue kv : s.span().getAttributesList()) {
        String v = OtlpCapture.value(kv.getValue());
        for (String n : needles) {
          assertFalse(v.contains(n), "attribute " + kv.getKey() + " of " + s + " contains " + n);
        }
      }
      for (String n : needles) {
        assertFalse(s.span().getName().contains(n), s.toString());
      }
    }
  }

  @Test
  void kafkaProducerAndConsumer() {
    CapturedSpan producer =
        await("kafka producer span", WAIT, () -> capture.spans().stream()
            .filter(s -> s.span().getKind() == Span.SpanKind.SPAN_KIND_PRODUCER && "kafka".equals(s.attr("messaging.system")))
            .findFirst().orElse(null));
    assertEquals("openlog-orders", producer.attr("messaging.destination.name"));
    assertEquals(serverSpan("POST /orders").traceId(), producer.traceId());
    CapturedSpan consumer =
        await("kafka consumer span", WAIT, () -> capture.spans().stream()
            .filter(s -> s.span().getKind() == Span.SpanKind.SPAN_KIND_CONSUMER && "kafka".equals(s.attr("messaging.system")))
            .filter(s -> s.traceId().equals(producer.traceId())
                || s.span().getLinksList().stream().anyMatch(l -> OtlpCapture.hex(l.getTraceId()).equals(producer.traceId())))
            .findFirst().orElse(null));
    assertEquals("openlog-orders", consumer.attr("messaging.destination.name"));
    CapturedLog log = await("consumer log record", WAIT, () -> capture.logs().stream()
        .filter(l -> l.body().equals("consumed order " + orderItem)).findFirst().orElse(null));
    assertEquals(32, log.traceId().length());
    assertFalse(log.traceId().matches("0+"));
  }

  @Test
  void grpcClientAndServerSpans() {
    CapturedSpan server =
        await("grpc server span", WAIT, () -> capture.spans().stream()
            .filter(s -> s.span().getKind() == Span.SpanKind.SPAN_KIND_SERVER && "grpc".equals(s.attr("rpc.system", "rpc.system.name")))
            .findFirst().orElse(null));
    CapturedSpan client =
        capture.spans().stream()
            .filter(s -> s.span().getKind() == Span.SpanKind.SPAN_KIND_CLIENT && "grpc".equals(s.attr("rpc.system", "rpc.system.name")))
            .findFirst().orElseThrow();
    assertEquals(client.traceId(), server.traceId());
    assertEquals(client.spanId(), OtlpCapture.hex(server.span().getParentSpanId()));
    assertTrue(server.span().getName().contains("demo.Greeter/SayHello"), server.span().getName());
    assertEquals(serverSpan("GET /grpc/{name}").traceId(), client.traceId());
  }

  @Test
  void errorsAreRecorded() {
    CapturedSpan fail = serverSpan("GET /fail");
    assertEquals(Status.StatusCode.STATUS_CODE_ERROR, fail.span().getStatus().getCode());
    assertEquals("500", fail.attr("http.response.status_code", "http.status_code"));
    boolean exception =
        capture.spans().stream()
            .filter(s -> s.traceId().equals(fail.traceId()))
            .flatMap(s -> s.span().getEventsList().stream())
            .anyMatch(e -> e.getName().equals("exception")
                && String.valueOf(OtlpCapture.attr(e.getAttributesList(), "exception.message")).contains("boom for order 42")
                && String.valueOf(OtlpCapture.attr(e.getAttributesList(), "exception.stacktrace")).contains("\tat "));
    assertTrue(exception, "exception event with message and Java stack trace");
  }

  @Test
  void remoteSamplingProbabilityIsRecorded() {
    CapturedSpan s =
        await("server span of the remote trace", WAIT, () -> capture.spans().stream()
            .filter(x -> x.span().getKind() == Span.SpanKind.SPAN_KIND_SERVER && x.traceId().equals(remoteTraceId))
            .findFirst().orElse(null));
    assertEquals("0.25", s.attr("sampling.ratio"));
    assertEquals("ot=th:c", s.span().getTraceState());
    CapturedSpan local = serverSpan("GET /users/{id}");
    assertNull(local.attr("sampling.ratio"), "ratio 1: no sampling.ratio");
    assertEquals(0x03, local.span().getFlags() & 0xff, "local root: sampled + W3C random flag");
  }

  @Test
  void logsAreCorrelatedWithTraces() {
    CapturedSpan users = capture.spans().stream().filter(s -> s.span().getName().equals("GET /users/{id}") && !s.traceId().equals(remoteTraceId)).findFirst()
        .orElseGet(() -> serverSpan("GET /users/{id}"));
    CapturedLog log = await("log record 'loading user 1'", WAIT, () -> capture.logs().stream().filter(l -> l.body().equals("loading user 1")).findFirst().orElse(null));
    assertEquals(users.traceId(), log.traceId());
    assertEquals(16, log.spanId().length());
    assertEquals("java-mvc", OtlpCapture.attr(log.resource().getAttributesList(), "service.name"));
    assertTrue(
        app.output.stream().anyMatch(l -> l.contains("loading user 1") && l.contains("trace_id=" + log.traceId())),
        "Logback MDC trace_id in the application's own output");
  }

  @Test
  void jvmRuntimeMetrics() {
    Set<String> want = Set.of("jvm.memory.used", "jvm.thread.count", "jvm.class.loaded", "jvm.cpu.time", "http.server.request.duration");
    Set<String> names = await("runtime metrics", WAIT, () -> {
      Set<String> n = capture.metrics().stream().map(m -> m.metric().getName()).collect(Collectors.toSet());
      return n.containsAll(want) ? n : null;
    });
    assertTrue(names.containsAll(want), names.toString());
  }
}

package io.github.onuragtas.openlog.it;

import static io.github.onuragtas.openlog.it.OtlpCapture.await;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import io.github.onuragtas.openlog.it.OtlpCapture.CapturedLog;
import io.github.onuragtas.openlog.it.OtlpCapture.CapturedSpan;
import io.opentelemetry.proto.trace.v1.Span;
import io.opentelemetry.proto.trace.v1.Status;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.stream.Collectors;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;

/** Spring Boot 3 WebFlux + Log4j2, head sampling ratio 0.5, runtime metrics disabled. */
@TestInstance(TestInstance.Lifecycle.PER_CLASS)
class SpringWebfluxIT {
  static final Duration WAIT = Duration.ofSeconds(60);
  static final int REQUESTS = 60;
  static final String FAIL_TRACE = "0af7651916cd43dd8448eb211c80319c";

  OtlpCapture capture;
  AppProcess app;
  Path infraDir;

  @BeforeAll
  void start() throws Exception {
    capture = OtlpCapture.start();
    infraDir = Files.createTempDirectory("openlog-infra-runtime");
    Map<String, String> env = TestEnv.agent(capture, "java-webflux", infraDir);
    env.put("OPENLOG_SAMPLING_RATIO", "0.5");
    env.put("OPENLOG_RUNTIME_METRICS", "false");
    app =
        AppProcess.start(
            "webflux",
            AppProcess.appDir("webflux"),
            "io.github.onuragtas.openlog.testapp.webflux.WebfluxApp",
            AppProcess.agentJar(),
            env,
            List.of(),
            System.getenv("OPENLOG_TEST_VERBOSE") != null);
    app.waitHealthy(Duration.ofSeconds(120));
    for (int i = 0; i < REQUESTS; i++) {
      assertEquals(200, app.get("/items/" + i));
    }
    assertEquals(500, app.get("/fail", "traceparent", "00-" + FAIL_TRACE + "-b7ad6b7169203331-01"));
    // metrics are exported every 2 s; wait for one export after the traffic so spans and logs are flushed too
    int before = capture.metrics().size();
    await("metric export after traffic", WAIT, () -> capture.metrics().size() > before
        && capture.metrics().stream().anyMatch(m -> m.metric().getName().equals("http.server.request.duration")));
    Thread.sleep(1500);
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

  List<CapturedSpan> itemSpans() {
    return capture.spans().stream()
        .filter(s -> s.span().getKind() == Span.SpanKind.SPAN_KIND_SERVER && s.span().getName().equals("GET /items/{id}"))
        .toList();
  }

  @Test
  void headSamplingWritesThresholdAndRatio() {
    List<CapturedSpan> sampled = itemSpans();
    assertTrue(sampled.size() >= 12 && sampled.size() <= 48, "sampled " + sampled.size() + " of " + REQUESTS);
    for (CapturedSpan s : sampled) {
      assertEquals("/items/{id}", s.attr("http.route"));
      assertEquals("0.5", s.attr("sampling.ratio"));
      assertEquals("ot=th:8", s.span().getTraceState());
      assertEquals(0x03, s.span().getFlags() & 0xff);
      // consistent sampling: the lower 56 bits of the trace id are >= T = 2^55
      long r = Long.parseUnsignedLong(s.traceId().substring(18), 16);
      assertTrue(r >= (1L << 55), s.traceId());
    }
    assertEquals("openlog", sampled.get(0).resourceAttr("telemetry.distro.name"));
    assertEquals(TestEnv.HOST_ID, sampled.get(0).resourceAttr("host.id"));
  }

  @Test
  void log4j2RecordsAreCorrelatedAndExported() {
    Set<String> sampledTraces = itemSpans().stream().map(CapturedSpan::traceId).collect(Collectors.toSet());
    List<CapturedLog> logs = capture.logs().stream().filter(l -> l.body().startsWith("loading item ")).toList();
    assertEquals(REQUESTS, logs.size(), "log records are exported for sampled and unsampled requests");
    CapturedLog correlated = logs.stream().filter(l -> sampledTraces.contains(l.traceId())).findFirst().orElseThrow();
    assertTrue(
        app.output.stream().anyMatch(l -> l.contains(correlated.body()) && l.contains("trace_id=" + correlated.traceId())),
        "Log4j2 context data trace_id in the application's own output");
  }

  @Test
  void runtimeMetricsCanBeDisabled() {
    Set<String> names = capture.metrics().stream().map(m -> m.metric().getName()).collect(Collectors.toSet());
    assertTrue(names.contains("http.server.request.duration"), names.toString());
    assertTrue(names.stream().noneMatch(n -> n.startsWith("jvm.")), names.toString());
  }

  @Test
  void errorOfSampledRemoteParent() {
    CapturedSpan fail =
        capture.spans().stream()
            .filter(s -> s.span().getKind() == Span.SpanKind.SPAN_KIND_SERVER && s.traceId().equals(FAIL_TRACE))
            .findFirst()
            .orElseThrow();
    assertEquals("GET /fail", fail.span().getName());
    assertEquals(Status.StatusCode.STATUS_CODE_ERROR, fail.span().getStatus().getCode());
    // remote W3C Level 1 parent (flags 01): the random flag is not invented
    assertEquals(0x01, fail.span().getFlags() & 0xff);
  }
}

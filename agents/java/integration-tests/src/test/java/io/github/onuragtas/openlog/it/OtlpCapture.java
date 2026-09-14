package io.github.onuragtas.openlog.it;

import static org.junit.jupiter.api.Assertions.fail;

import com.google.protobuf.ByteString;
import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import io.opentelemetry.proto.collector.logs.v1.ExportLogsServiceRequest;
import io.opentelemetry.proto.collector.metrics.v1.ExportMetricsServiceRequest;
import io.opentelemetry.proto.collector.trace.v1.ExportTraceServiceRequest;
import io.opentelemetry.proto.common.v1.AnyValue;
import io.opentelemetry.proto.common.v1.InstrumentationScope;
import io.opentelemetry.proto.common.v1.KeyValue;
import io.opentelemetry.proto.logs.v1.LogRecord;
import io.opentelemetry.proto.logs.v1.ResourceLogs;
import io.opentelemetry.proto.logs.v1.ScopeLogs;
import io.opentelemetry.proto.metrics.v1.Metric;
import io.opentelemetry.proto.metrics.v1.ResourceMetrics;
import io.opentelemetry.proto.metrics.v1.ScopeMetrics;
import io.opentelemetry.proto.resource.v1.Resource;
import io.opentelemetry.proto.trace.v1.ResourceSpans;
import io.opentelemetry.proto.trace.v1.ScopeSpans;
import io.opentelemetry.proto.trace.v1.Span;
import java.io.IOException;
import java.io.InputStream;
import java.net.InetSocketAddress;
import java.time.Duration;
import java.util.ArrayList;
import java.util.HexFormat;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.function.Supplier;
import java.util.zip.GZIPInputStream;

/** A minimal OTLP/HTTP protobuf receiver that records requests (headers) and decoded telemetry. */
public final class OtlpCapture implements AutoCloseable {
  public record Request(String path, Map<String, List<String>> headers, int bytes) {
    public String header(String name) {
      for (Map.Entry<String, List<String>> e : headers.entrySet()) {
        if (e.getKey().equalsIgnoreCase(name) && !e.getValue().isEmpty()) {
          return e.getValue().get(0);
        }
      }
      return null;
    }
  }

  public record CapturedSpan(Resource resource, InstrumentationScope scope, Span span) {
    public String attr(String... keys) {
      return OtlpCapture.attr(span.getAttributesList(), keys);
    }

    public String resourceAttr(String key) {
      return OtlpCapture.attr(resource.getAttributesList(), key);
    }

    public String traceId() {
      return hex(span.getTraceId());
    }

    public String spanId() {
      return hex(span.getSpanId());
    }

    @Override
    public String toString() {
      return span.getKind() + " " + span.getName() + " scope=" + scope.getName() + " trace=" + traceId() + " " + span.getAttributesList();
    }
  }

  public record CapturedMetric(Resource resource, InstrumentationScope scope, Metric metric) {}

  public record CapturedLog(Resource resource, InstrumentationScope scope, LogRecord log) {
    public String body() {
      return log.getBody().getStringValue();
    }

    public String traceId() {
      return hex(log.getTraceId());
    }

    public String spanId() {
      return hex(log.getSpanId());
    }
  }

  private final HttpServer server;
  private final ExecutorService executor = Executors.newFixedThreadPool(4);
  private final boolean decode;
  public final List<Request> requests = new CopyOnWriteArrayList<>();
  private final List<CapturedSpan> spans = new CopyOnWriteArrayList<>();
  private final List<CapturedMetric> metrics = new CopyOnWriteArrayList<>();
  private final List<CapturedLog> logs = new CopyOnWriteArrayList<>();

  private OtlpCapture(boolean decode) throws IOException {
    this.decode = decode;
    server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
    server.createContext("/", this::handle);
    server.setExecutor(executor);
    server.start();
  }

  public static OtlpCapture start() throws IOException {
    return new OtlpCapture(true);
  }

  /** Accepts and counts requests without decoding them (benchmarks). */
  public static OtlpCapture startDiscarding() throws IOException {
    return new OtlpCapture(false);
  }

  public String endpoint() {
    return "http://127.0.0.1:" + server.getAddress().getPort();
  }

  private void handle(HttpExchange ex) throws IOException {
    try (InputStream in = ex.getRequestBody()) {
      byte[] body = in.readAllBytes();
      requests.add(new Request(ex.getRequestURI().getPath(), Map.copyOf(ex.getRequestHeaders()), body.length));
      if (decode) {
        if ("gzip".equalsIgnoreCase(ex.getRequestHeaders().getFirst("Content-Encoding"))) {
          try (GZIPInputStream gz = new GZIPInputStream(new java.io.ByteArrayInputStream(body))) {
            body = gz.readAllBytes();
          }
        }
        switch (ex.getRequestURI().getPath()) {
          case "/v1/traces" -> {
            for (ResourceSpans rs : ExportTraceServiceRequest.parseFrom(body).getResourceSpansList()) {
              for (ScopeSpans ss : rs.getScopeSpansList()) {
                for (Span s : ss.getSpansList()) {
                  spans.add(new CapturedSpan(rs.getResource(), ss.getScope(), s));
                }
              }
            }
          }
          case "/v1/metrics" -> {
            for (ResourceMetrics rm : ExportMetricsServiceRequest.parseFrom(body).getResourceMetricsList()) {
              for (ScopeMetrics sm : rm.getScopeMetricsList()) {
                for (Metric m : sm.getMetricsList()) {
                  metrics.add(new CapturedMetric(rm.getResource(), sm.getScope(), m));
                }
              }
            }
          }
          case "/v1/logs" -> {
            for (ResourceLogs rl : ExportLogsServiceRequest.parseFrom(body).getResourceLogsList()) {
              for (ScopeLogs sl : rl.getScopeLogsList()) {
                for (LogRecord l : sl.getLogRecordsList()) {
                  logs.add(new CapturedLog(rl.getResource(), sl.getScope(), l));
                }
              }
            }
          }
          default -> {}
        }
      }
      ex.getResponseHeaders().add("Content-Type", "application/x-protobuf");
      ex.sendResponseHeaders(200, -1);
    } catch (RuntimeException e) {
      e.printStackTrace();
      ex.sendResponseHeaders(400, -1);
    } finally {
      ex.close();
    }
  }

  public List<CapturedSpan> spans() {
    return new ArrayList<>(spans);
  }

  public List<CapturedMetric> metrics() {
    return new ArrayList<>(metrics);
  }

  public List<CapturedLog> logs() {
    return new ArrayList<>(logs);
  }

  /** Polls until the supplier returns a non-null value that is not {@code false} or an empty list. */
  public static <T> T await(String what, Duration timeout, Supplier<T> s) {
    long deadline = System.nanoTime() + timeout.toNanos();
    while (true) {
      T v = s.get();
      if (v != null && !Boolean.FALSE.equals(v) && !(v instanceof List<?> l && l.isEmpty())) {
        return v;
      }
      if (System.nanoTime() > deadline) {
        fail("timed out waiting for " + what);
      }
      try {
        Thread.sleep(200);
      } catch (InterruptedException e) {
        Thread.currentThread().interrupt();
        fail("interrupted");
      }
    }
  }

  static String attr(List<KeyValue> attrs, String... keys) {
    for (String key : keys) {
      for (KeyValue kv : attrs) {
        if (kv.getKey().equals(key)) {
          return value(kv.getValue());
        }
      }
    }
    return null;
  }

  static String value(AnyValue v) {
    return switch (v.getValueCase()) {
      case STRING_VALUE -> v.getStringValue();
      case INT_VALUE -> Long.toString(v.getIntValue());
      case DOUBLE_VALUE -> Double.toString(v.getDoubleValue());
      case BOOL_VALUE -> Boolean.toString(v.getBoolValue());
      default -> v.toString();
    };
  }

  public static String hex(ByteString b) {
    return HexFormat.of().formatHex(b.toByteArray());
  }

  @Override
  public void close() {
    server.stop(0);
    executor.shutdownNow();
  }
}

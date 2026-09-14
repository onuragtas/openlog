package io.github.onuragtas.openlog.javaagent.sampling;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertSame;
import static org.junit.jupiter.api.Assertions.assertTrue;

import io.opentelemetry.api.common.Attributes;
import io.opentelemetry.api.trace.Span;
import io.opentelemetry.api.trace.SpanKind;
import io.opentelemetry.api.trace.TraceState;
import io.opentelemetry.api.trace.TraceStateBuilder;
import io.opentelemetry.api.trace.Tracer;
import io.opentelemetry.sdk.testing.exporter.InMemorySpanExporter;
import io.opentelemetry.sdk.trace.SdkTracerProvider;
import io.opentelemetry.sdk.trace.data.SpanData;
import io.opentelemetry.sdk.trace.export.SimpleSpanProcessor;
import io.opentelemetry.sdk.trace.samplers.SamplingDecision;
import java.util.List;
import java.util.Set;
import java.util.StringJoiner;
import org.junit.jupiter.api.Test;

class OpenlogSamplerTest {
  static TraceState ts(String s) {
    TraceStateBuilder b = TraceState.builder();
    String[] members = s.isEmpty() ? new String[0] : s.split(",");
    for (int i = members.length - 1; i >= 0; i--) {
      String[] kv = members[i].split("=", 2);
      b.put(kv[0], kv[1]);
    }
    return b.build();
  }

  static String serialize(TraceState t) {
    StringJoiner j = new StringJoiner(",");
    t.forEach((k, v) -> j.add(k + "=" + v));
    return j.toString();
  }

  @Test
  void thresholdEncoding() {
    Object[][] cases = {{0.5, "8"}, {0.25, "c"}, {0.125, "e"}, {0.75, "4"}, {0.1, "e6666666666666"}, {0.001, "ffbe76c8b43958"}};
    for (Object[] c : cases) {
      double ratio = (Double) c[0];
      long keep = Math.round(ratio * (double) OpenlogSampler.MAX_THRESHOLD);
      assertEquals(c[1], OpenlogSampler.encodeThreshold(OpenlogSampler.MAX_THRESHOLD - keep), "ratio " + ratio);
      Double p = OtValue.parse("th:" + c[1]).probability();
      assertTrue(p != null && Math.abs(p - ratio) < 1e-12, "decoded " + p);
    }
    assertEquals("0", OpenlogSampler.encodeThreshold(0));
    assertEquals(0.25, OtValue.parse("p:2").probability());
    assertEquals(0.0, OtValue.parse("p:63").probability());
    assertNull(OtValue.parse("p:64").probability());
    assertNull(OpenlogSampler.parseThreshold("xyz"));
    assertNull(OpenlogSampler.parseThreshold("123456789abcdef"));
    assertNull(OtValue.parse("rv:+0000000000001").randomness());
    OtValue o = OtValue.parse("th:c;rv:00000000000001;bad;:x");
    assertEquals("th:c;rv:00000000000001", o.toString());
  }

  @Test
  void withOTKeepsW3CLimits() {
    StringJoiner j = new StringJoiner(",");
    for (int i = 0; i < 32; i++) {
      j.add("v" + i + "=x");
    }
    String[] out = serialize(OpenlogSampler.withOT(ts(j.toString()), OtValue.parse("th:c"))).split(",");
    assertEquals(32, out.length);
    assertEquals("ot=th:c", out[0]);
    assertEquals("v30=x", out[31]); // right-most member dropped
    String longOt = "th:c;x:" + "a".repeat(200) + ";y:" + "b".repeat(100);
    assertEquals("th:c;x:" + "a".repeat(200), OpenlogSampler.withOT(TraceState.getDefault(), OtValue.parse(longOt)).get("ot"));
    assertEquals("k=v", serialize(OpenlogSampler.withOT(ts("ot=th:c,k=v"), OtValue.EMPTY)));
    TraceState bad = ts("k=v");
    assertSame(bad, OpenlogSampler.withOT(bad, OtValue.parse("x:a,b")));
    assertEquals("ot=th:8,congo=t61rcWkgMzE", serialize(OpenlogSampler.withOT(ts("congo=t61rcWkgMzE,ot=rv:x"), OtValue.parse("th:8"))));
  }

  @Test
  void ratioQuarterKeepsAboutAQuarterAndWeightsToTheTotal() {
    InMemorySpanExporter exporter = InMemorySpanExporter.create();
    try (SdkTracerProvider tp =
        SdkTracerProvider.builder()
            .setSampler(OpenlogSampler.create(0.25, false))
            .addSpanProcessor(SimpleSpanProcessor.create(exporter))
            .build()) {
      Tracer tracer = tp.get("t");
      int n = 20_000;
      for (int i = 0; i < n; i++) {
        tracer.spanBuilder("root").startSpan().end();
      }
      List<SpanData> spans = exporter.getFinishedSpanItems();
      double weighted = 0;
      for (SpanData s : spans) {
        weighted += 1 / s.getAttributes().get(OpenlogSampler.SAMPLING_RATIO);
      }
      assertTrue(Math.abs(spans.size() / (double) n - 0.25) < 0.02, "kept " + spans.size());
      assertTrue(Math.abs(weighted - n) / n < 0.08, "weighted " + weighted);
      for (SpanData s : spans.subList(0, 10)) {
        assertEquals("th:c", s.getSpanContext().getTraceState().get("ot"));
        assertEquals("03", s.getSpanContext().getTraceFlags().asHex());
      }
    }
  }

  @Test
  void ignoredServerPathsAreDropped() {
    var s = OpenlogSampler.create(1, false, OpenlogSampler.DEFAULT_RANDOMNESS, Set.of("/healthz"));
    var ctx = io.opentelemetry.context.Context.root();
    String tid = "4bf92f3577b34da65a00000000000001";
    Attributes health = Attributes.builder().put("url.path", "/healthz").build();
    assertEquals(SamplingDecision.DROP, s.shouldSample(ctx, tid, "GET", SpanKind.SERVER, health, List.of()).getDecision());
    Attributes legacy = Attributes.builder().put("http.target", "/healthz?x=1").build();
    assertEquals(SamplingDecision.DROP, s.shouldSample(ctx, tid, "GET", SpanKind.SERVER, legacy, List.of()).getDecision());
    assertEquals(
        SamplingDecision.RECORD_AND_SAMPLE,
        s.shouldSample(ctx, tid, "GET", SpanKind.CLIENT, health, List.of()).getDecision());
    Attributes other = Attributes.builder().put("url.path", "/orders").build();
    assertEquals(
        SamplingDecision.RECORD_AND_SAMPLE,
        s.shouldSample(ctx, tid, "GET", SpanKind.SERVER, other, List.of()).getDecision());
    assertTrue(s.getDescription().contains("OpenlogIgnoredPaths"));
  }

  @Test
  void ratioZeroWithRvWritesRandomness() {
    InMemorySpanExporter exporter = InMemorySpanExporter.create();
    try (SdkTracerProvider tp =
        SdkTracerProvider.builder()
            .setSampler(OpenlogSampler.create(0, true, () -> 0x0123456789abcdL, Set.of()))
            .addSpanProcessor(SimpleSpanProcessor.create(exporter))
            .build()) {
      Span span = tp.get("t").spanBuilder("root").startSpan();
      assertFalse(span.getSpanContext().isSampled());
      assertEquals("rv:0123456789abcd", span.getSpanContext().getTraceState().get("ot"));
      span.end();
      assertTrue(exporter.getFinishedSpanItems().isEmpty());
    }
  }
}

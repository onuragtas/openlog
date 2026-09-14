package io.github.onuragtas.openlog.javaagent.sampling;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.opentelemetry.api.trace.Span;
import io.opentelemetry.api.trace.SpanKind;
import io.opentelemetry.api.trace.Tracer;
import io.opentelemetry.api.trace.propagation.W3CTraceContextPropagator;
import io.opentelemetry.context.Context;
import io.opentelemetry.context.propagation.TextMapGetter;
import io.opentelemetry.sdk.testing.exporter.InMemorySpanExporter;
import io.opentelemetry.sdk.trace.IdGenerator;
import io.opentelemetry.sdk.trace.SdkTracerProvider;
import io.opentelemetry.sdk.trace.data.SpanData;
import io.opentelemetry.sdk.trace.export.SimpleSpanProcessor;
import java.io.File;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.stream.Stream;
import org.junit.jupiter.api.DynamicTest;
import org.junit.jupiter.api.TestFactory;

/** Cross-language fixtures produced by the Go agent's sampler (agents/node/test/interop/go-sampler-fixtures.json). */
class SamplerFixturesTest {
  private static final W3CTraceContextPropagator W3C = W3CTraceContextPropagator.getInstance();

  private static final TextMapGetter<Map<String, String>> GETTER =
      new TextMapGetter<>() {
        @Override
        public Iterable<String> keys(Map<String, String> carrier) {
          return carrier.keySet();
        }

        @Override
        public String get(Map<String, String> carrier, String key) {
          return carrier == null ? null : carrier.get(key);
        }
      };

  @TestFactory
  Stream<DynamicTest> goFixtures() throws Exception {
    File file = new File(System.getProperty("openlog.test.fixtures"));
    JsonNode root = new ObjectMapper().readTree(file);
    List<DynamicTest> tests = new ArrayList<>();
    for (JsonNode f : root.get("cases")) {
      tests.add(DynamicTest.dynamicTest(f.get("name").asText(), () -> run(f)));
    }
    assertTrue(tests.size() >= 17, "fixture cases: " + tests.size());
    return tests.stream();
  }

  private static void run(JsonNode f) {
    String traceId = f.get("traceId").asText();
    String spanId = f.get("spanId").asText();
    String childSpanId = f.get("childSpanId").asText();
    long rnd = f.hasNonNull("randomness") ? Long.parseLong(f.get("randomness").asText(), 16) : 0L;
    InMemorySpanExporter exporter = InMemorySpanExporter.create();
    IdGenerator ids =
        new IdGenerator() {
          int n;

          @Override
          public String generateSpanId() {
            return n++ == 0 ? spanId : childSpanId;
          }

          @Override
          public String generateTraceId() {
            return traceId;
          }

          @Override
          public boolean generatesRandomTraceIds() {
            return true;
          }
        };
    try (SdkTracerProvider tp =
        SdkTracerProvider.builder()
            .setSampler(OpenlogSampler.create(f.get("ratio").asDouble(), f.get("writeRV").asBoolean(), () -> rnd, Set.of()))
            .setIdGenerator(ids)
            .addSpanProcessor(SimpleSpanProcessor.create(exporter))
            .build()) {
      Tracer tracer = tp.get("fixtures");
      Context ctx = Context.root();
      if (f.hasNonNull("incoming")) {
        Map<String, String> in = new HashMap<>();
        in.put("traceparent", f.get("incoming").get("traceparent").asText());
        in.put("tracestate", f.get("incoming").get("tracestate").asText());
        ctx = W3C.extract(ctx, in, GETTER);
      }
      Span span = tracer.spanBuilder("span").setSpanKind(SpanKind.SERVER).setParent(ctx).startSpan();
      Context sctx = ctx.with(span);
      Span child = tracer.spanBuilder("child").setSpanKind(SpanKind.CLIENT).setParent(sctx).startSpan();
      Context cctx = sctx.with(child);
      child.end();
      span.end();
      check(exporter, span, sctx, f.get("span"), "span");
      check(exporter, child, cctx, f.get("child"), "child");
    }
  }

  private static void check(InMemorySpanExporter exporter, Span s, Context ctx, JsonNode want, String which) {
    Map<String, String> h = new HashMap<>();
    W3C.inject(ctx, h, Map::put);
    assertEquals(want.get("traceparent").asText(), h.getOrDefault("traceparent", ""), which + " traceparent");
    assertEquals(want.get("tracestate").asText(), h.getOrDefault("tracestate", ""), which + " tracestate");
    assertEquals(want.get("sampled").asBoolean(), s.getSpanContext().isSampled(), which + " sampled");
    Double ratio = null;
    for (SpanData d : exporter.getFinishedSpanItems()) {
      if (d.getSpanId().equals(s.getSpanContext().getSpanId())) {
        ratio = d.getAttributes().get(OpenlogSampler.SAMPLING_RATIO);
      }
    }
    Double wantRatio = want.get("samplingRatio").isNull() ? null : want.get("samplingRatio").asDouble();
    assertEquals(wantRatio, ratio, which + " sampling.ratio");
  }
}

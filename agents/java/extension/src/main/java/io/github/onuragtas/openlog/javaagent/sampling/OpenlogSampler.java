/*
 * Copyright The openlog Authors
 * SPDX-License-Identifier: Apache-2.0
 */
package io.github.onuragtas.openlog.javaagent.sampling;

import io.opentelemetry.api.common.AttributeKey;
import io.opentelemetry.api.common.Attributes;
import io.opentelemetry.api.trace.Span;
import io.opentelemetry.api.trace.SpanKind;
import io.opentelemetry.api.trace.TraceState;
import io.opentelemetry.api.trace.TraceStateBuilder;
import io.opentelemetry.context.Context;
import io.opentelemetry.sdk.trace.data.LinkData;
import io.opentelemetry.sdk.trace.samplers.Sampler;
import io.opentelemetry.sdk.trace.samplers.SamplingDecision;
import io.opentelemetry.sdk.trace.samplers.SamplingResult;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Locale;
import java.util.Set;
import java.util.concurrent.ThreadLocalRandom;
import java.util.regex.Pattern;

/**
 * Consistent probability sampling with the OpenTelemetry tracestate {@code ot} entry
 * (https://opentelemetry.io/docs/specs/otel/trace/tracestate-probability-sampling/), a port of the Go agent's
 * sampler.go (and agents/node/src/sampler.ts). Cross-language fixtures produced by the Go agent:
 * agents/node/test/interop/go-sampler-fixtures.json.
 *
 * <p>The W3C Trace Context Level 2 random flag (traceparent flags 0x02) is set by the OpenTelemetry Java SDK itself
 * (1.60+): root spans get it because the default id generator produces random trace ids, children inherit it from
 * their parent.
 */
public final class OpenlogSampler {
  /**
   * Span attribute carrying the head sampling probability of a trace root sampled by this agent, or of a local entry
   * span whose remote parent was sampled with a probability below 1 (tracestate ot=th). Only set when p &lt; 1. The
   * APM backend weights RED metrics by 1/p (apm.md §4).
   */
  public static final AttributeKey<Double> SAMPLING_RATIO = AttributeKey.doubleKey("sampling.ratio");

  static final String OT_KEY = "ot";
  static final int MAX_OT_VALUE_LEN = 256;
  static final int MAX_MEMBERS = 32;
  /** exclusive; T = 2^56 would mean p = 0 */
  public static final long MAX_THRESHOLD = 1L << 56;

  static final long RANDOMNESS_MASK = MAX_THRESHOLD - 1;

  private static final AttributeKey<String> URL_PATH = AttributeKey.stringKey("url.path");
  private static final AttributeKey<String> HTTP_TARGET = AttributeKey.stringKey("http.target");

  // W3C tracestate value: printable ASCII except ',' and '=', not ending with a space, at most 256 characters.
  private static final Pattern VALID_VALUE =
      Pattern.compile("[\\x20-\\x2b\\x2d-\\x3c\\x3e-\\x7e]{0,255}[\\x21-\\x2b\\x2d-\\x3c\\x3e-\\x7e]");

  /** 56 random bits. */
  public interface RandomnessSource {
    long next56();
  }

  public static final RandomnessSource DEFAULT_RANDOMNESS =
      new RandomnessSource() {
        @Override
        public long next56() {
          return ThreadLocalRandom.current().nextLong() & RANDOMNESS_MASK;
        }
      };

  private OpenlogSampler() {}

  public static Sampler create(double ratio, boolean writeRV) {
    return create(ratio, writeRV, DEFAULT_RANDOMNESS, Collections.<String>emptySet());
  }

  /**
   * Parent-based sampler with the Go agent's semantics:
   *
   * <ul>
   *   <li>new traces are sampled with probability {@code ratio} by comparing the 56-bit randomness (tracestate ot=rv,
   *       else the lower 56 bits of the trace id) against T = (1 − ratio)·2^56; sampled roots carry sampling.ratio and
   *       ot=th:&lt;T&gt;;
   *   <li>children of a sampled remote parent are sampled and get sampling.ratio = p when tracestate carries p &lt; 1;
   *   <li>other children follow their parent. With {@code writeRV}, roots without ot=rv write explicit randomness.
   * </ul>
   *
   * <p>{@code ignoredServerPaths}: SERVER spans whose {@code url.path} (or {@code http.target} without query) is in
   * the set are dropped (health checks).
   */
  public static Sampler create(
      double ratio, boolean writeRV, RandomnessSource randomness, Set<String> ignoredServerPaths) {
    Sampler root;
    boolean keepAll = ratio >= 1 || Double.isNaN(ratio);
    if (!keepAll && ratio > 0 && Math.round(ratio * (double) MAX_THRESHOLD) >= MAX_THRESHOLD) {
      keepAll = true;
    }
    if (keepAll) {
      root = Sampler.alwaysOn();
    } else if (ratio <= 0) {
      root = Sampler.alwaysOff();
    } else {
      root = new ConsistentRatioRootSampler(ratio, writeRV, randomness);
    }
    if (writeRV && !(root instanceof ConsistentRatioRootSampler)) {
      root = new RvRootSampler(root, randomness);
    }
    Sampler s = Sampler.parentBasedBuilder(root).setRemoteParentSampled(new RemoteParentSampledSampler()).build();
    if (ignoredServerPaths != null && !ignoredServerPaths.isEmpty()) {
      s = new IgnoredPathsSampler(s, ignoredServerPaths);
    }
    return s;
  }

  /** Encodes T as up to 14 hex digits without trailing zeros ("0" for T=0). */
  static String encodeThreshold(long t) {
    String s = String.format(Locale.ROOT, "%014x", t);
    int end = s.length();
    while (end > 0 && s.charAt(end - 1) == '0') {
      end--;
    }
    return end == 0 ? "0" : s.substring(0, end);
  }

  /** Decodes th:&lt;hex&gt; (1..14 hex digits, right-padded with zeros); null when invalid. */
  static Long parseThreshold(String s) {
    if (s == null || !OtValue.isThreshold(s)) {
      return null;
    }
    StringBuilder b = new StringBuilder(14).append(s);
    while (b.length() < 14) {
      b.append('0');
    }
    return Long.parseLong(b.toString(), 16);
  }

  static String rvString(long r) {
    return String.format(Locale.ROOT, "%014x", r & RANDOMNESS_MASK);
  }

  /**
   * Stores {@code o} as the {@code ot} member of {@code ts} (moved to the front), keeping W3C limits: the value is at
   * most 256 characters (sub-keys other than th and rv are dropped, last first) and the list keeps at most 32 members
   * (the right-most member is dropped). An empty {@code o} removes the member. On an invalid value {@code ts} is
   * returned unchanged.
   */
  static TraceState withOT(TraceState ts, OtValue o) {
    List<OtValue.Field> fields = new ArrayList<>(o.fields);
    while (OtValue.toString(fields).length() > MAX_OT_VALUE_LEN) {
      int i = fields.size() - 1;
      while (i >= 0 && (fields.get(i).key.equals("th") || fields.get(i).key.equals("rv"))) {
        i--;
      }
      if (i < 0) {
        return ts;
      }
      fields.remove(i);
    }
    final List<String[]> others = new ArrayList<>();
    ts.forEach(
        (k, v) -> {
          if (!k.equals(OT_KEY)) {
            others.add(new String[] {k, v});
          }
        });
    if (fields.isEmpty()) {
      if (ts.get(OT_KEY) == null) {
        return ts;
      }
      return build(others);
    }
    String value = OtValue.toString(fields);
    if (!VALID_VALUE.matcher(value).matches()) {
      return ts;
    }
    List<String[]> members = new ArrayList<>(others.size() + 1);
    members.add(new String[] {OT_KEY, value});
    members.addAll(others);
    if (members.size() > MAX_MEMBERS) {
      members = members.subList(0, MAX_MEMBERS);
    }
    return build(members);
  }

  private static TraceState build(List<String[]> members) {
    // TraceStateBuilder.put adds new keys at the front: put right-most first.
    TraceStateBuilder b = TraceState.builder();
    for (int i = members.size() - 1; i >= 0; i--) {
      b.put(members.get(i)[0], members.get(i)[1]);
    }
    return b.build();
  }

  static TraceState parentTraceState(Context ctx) {
    return Span.fromContext(ctx).getSpanContext().getTraceState();
  }

  static final class Result implements SamplingResult {
    private final SamplingDecision decision;
    private final Attributes attributes;
    private final TraceState traceState;

    Result(SamplingDecision decision, Attributes attributes, TraceState traceState) {
      this.decision = decision;
      this.attributes = attributes;
      this.traceState = traceState;
    }

    @Override
    public SamplingDecision getDecision() {
      return decision;
    }

    @Override
    public Attributes getAttributes() {
      return attributes;
    }

    @Override
    public TraceState getUpdatedTraceState(TraceState parentTraceState) {
      return traceState != null ? traceState : parentTraceState;
    }
  }

  /** Root sampler for 0 &lt; ratio &lt; 1 (threshold comparison against trace id or ot=rv randomness). */
  static final class ConsistentRatioRootSampler implements Sampler {
    final double ratio;
    final long threshold;
    final String th;
    private final boolean writeRV;
    private final RandomnessSource randomness;
    private final Attributes attrs;

    ConsistentRatioRootSampler(double ratio, boolean writeRV, RandomnessSource randomness) {
      this.ratio = ratio;
      this.writeRV = writeRV;
      this.randomness = randomness;
      // ratio·2^56 is exact in float64 (power-of-two scaling); 1-ratio would not be.
      long keep = Math.round(ratio * (double) MAX_THRESHOLD);
      if (keep == 0) {
        keep = 1;
      }
      this.threshold = MAX_THRESHOLD - keep;
      this.th = encodeThreshold(threshold);
      this.attrs = Attributes.of(SAMPLING_RATIO, ratio);
    }

    @Override
    public SamplingResult shouldSample(
        Context parentContext,
        String traceId,
        String name,
        SpanKind spanKind,
        Attributes attributes,
        List<LinkData> parentLinks) {
      TraceState ts = parentTraceState(parentContext);
      OtValue ot = OtValue.parse(ts.get(OT_KEY));
      Long r = ot.randomness();
      if (r == null) {
        if (writeRV) {
          long rnd = randomness.next56() & RANDOMNESS_MASK;
          r = rnd;
          ot = ot.withLast("rv", rvString(rnd));
        } else {
          r = Long.parseLong(traceId.substring(18, 32), 16) & RANDOMNESS_MASK;
        }
      }
      if (r < threshold) {
        return new Result(SamplingDecision.DROP, Attributes.empty(), withOT(ts, ot.without("th")));
      }
      return new Result(SamplingDecision.RECORD_AND_SAMPLE, attrs, withOT(ts, ot.with("th", th)));
    }

    @Override
    public String getDescription() {
      return "OpenlogConsistentRatio{" + ratio + "}";
    }

    @Override
    public String toString() {
      return getDescription();
    }
  }

  /** Adds ot=rv to roots decided by another sampler (ratio 0 or 1). */
  static final class RvRootSampler implements Sampler {
    private final Sampler inner;
    private final RandomnessSource randomness;

    RvRootSampler(Sampler inner, RandomnessSource randomness) {
      this.inner = inner;
      this.randomness = randomness;
    }

    @Override
    public SamplingResult shouldSample(
        Context parentContext,
        String traceId,
        String name,
        SpanKind spanKind,
        Attributes attributes,
        List<LinkData> parentLinks) {
      SamplingResult res = inner.shouldSample(parentContext, traceId, name, spanKind, attributes, parentLinks);
      TraceState ts = res.getUpdatedTraceState(parentTraceState(parentContext));
      OtValue ot = OtValue.parse(ts.get(OT_KEY));
      if (ot.randomness() != null) {
        return res;
      }
      String rv = rvString(randomness.next56());
      return new Result(res.getDecision(), res.getAttributes(), withOT(ts, ot.withLast("rv", rv)));
    }

    @Override
    public String getDescription() {
      return "OpenlogRV{" + inner.getDescription() + "}";
    }

    @Override
    public String toString() {
      return getDescription();
    }
  }

  /** Samples every span whose remote parent is sampled and records the upstream sampling probability on it. */
  static final class RemoteParentSampledSampler implements Sampler {
    @Override
    public SamplingResult shouldSample(
        Context parentContext,
        String traceId,
        String name,
        SpanKind spanKind,
        Attributes attributes,
        List<LinkData> parentLinks) {
      TraceState ts = parentTraceState(parentContext);
      Double p = OtValue.parse(ts.get(OT_KEY)).probability();
      if (p != null && p > 0 && p < 1) {
        return new Result(SamplingDecision.RECORD_AND_SAMPLE, Attributes.of(SAMPLING_RATIO, p), null);
      }
      return new Result(SamplingDecision.RECORD_AND_SAMPLE, Attributes.empty(), null);
    }

    @Override
    public String getDescription() {
      return "OpenlogRemoteParentSampled";
    }

    @Override
    public String toString() {
      return getDescription();
    }
  }

  /** Drops SERVER spans of ignored request paths (exact match), like the Node.js agent's OPENLOG_HTTP_IGNORE_PATHS. */
  static final class IgnoredPathsSampler implements Sampler {
    private final Sampler delegate;
    private final Set<String> paths;

    IgnoredPathsSampler(Sampler delegate, Set<String> paths) {
      this.delegate = delegate;
      this.paths = paths;
    }

    @Override
    public SamplingResult shouldSample(
        Context parentContext,
        String traceId,
        String name,
        SpanKind spanKind,
        Attributes attributes,
        List<LinkData> parentLinks) {
      if (spanKind == SpanKind.SERVER) {
        String path = attributes.get(URL_PATH);
        if (path == null) {
          path = attributes.get(HTTP_TARGET);
          if (path != null) {
            int q = path.indexOf('?');
            if (q >= 0) {
              path = path.substring(0, q);
            }
          }
        }
        if (path != null && paths.contains(path)) {
          return SamplingResult.drop();
        }
      }
      return delegate.shouldSample(parentContext, traceId, name, spanKind, attributes, parentLinks);
    }

    @Override
    public String getDescription() {
      return "OpenlogIgnoredPaths{" + paths + "," + delegate.getDescription() + "}";
    }

    @Override
    public String toString() {
      return getDescription();
    }
  }
}

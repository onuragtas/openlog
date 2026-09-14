/*
 * Copyright The openlog Authors
 * SPDX-License-Identifier: Apache-2.0
 */
package io.github.onuragtas.openlog.javaagent;

import io.github.onuragtas.openlog.javaagent.db.DbStatementSpanExporter;
import io.github.onuragtas.openlog.javaagent.sampling.OpenlogSampler;
import io.opentelemetry.sdk.autoconfigure.spi.ConfigProperties;
import io.opentelemetry.sdk.trace.samplers.Sampler;
import java.net.URI;
import java.util.Arrays;
import java.util.HashMap;
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Set;

/** Maps OPENLOG_* settings onto OpenTelemetry Java agent properties (precedence: OPENLOG_* > OTEL_* > defaults). */
public final class OpenlogConfigMapper {
  /** Ingest authentication header (docs/contracts/config.md). */
  public static final String LICENSE_KEY_HEADER = "openlog-license-key";

  static final String HEADERS = "otel.exporter.otlp.headers";
  static final String RESOURCE_ATTRIBUTES = "otel.resource.attributes";
  static final String SAMPLER = "otel.traces.sampler";
  static final String SAMPLER_ARG = "otel.traces.sampler.arg";

  /** Configured samplers the openlog sampler replaces (the SDK defaults and ratio samplers). */
  static final Set<String> REPLACEABLE_SAMPLERS =
      new HashSet<>(
          Arrays.asList(
              "always_on", "parentbased_always_on", "traceidratio", "parentbased_traceidratio", "openlog"));

  private OpenlogConfigMapper() {}

  /** Lowest-precedence defaults (any OTEL_* variable or otel.* system property overrides them). */
  public static Map<String, String> defaults() {
    Map<String, String> m = new HashMap<>();
    m.put("otel.exporter.otlp.protocol", "http/protobuf");
    m.put("otel.exporter.otlp.compression", "gzip");
    m.put("otel.traces.exporter", "otlp");
    m.put("otel.metrics.exporter", "otlp");
    m.put("otel.logs.exporter", "otlp");
    // The openlog exporter wrapper sanitizes db.query.text exactly like the Go agent; it needs the raw statement.
    m.put("otel.instrumentation.common.db-statement-sanitizer.enabled", "false");
    m.put("otel.instrumentation.common.db.query-sanitization.enabled", "false");
    return m;
  }

  /** Overrides from OPENLOG_* settings; {@code config} holds the OTEL_* and default values. */
  public static Map<String, String> overrides(OpenlogSettings s, ConfigProperties config, Diag diag) {
    Map<String, String> m = new LinkedHashMap<>();
    Boolean enabled = s.bool("OPENLOG_ENABLED", diag);
    if (enabled != null && !enabled) {
      m.put("otel.sdk.disabled", "true");
    }

    String protocol = s.get("OPENLOG_PROTOCOL");
    String effectiveProtocol = config.getString("otel.exporter.otlp.protocol", "http/protobuf");
    if (protocol != null) {
      switch (protocol.toLowerCase(Locale.ROOT)) {
        case "http/protobuf":
        case "http":
          effectiveProtocol = "http/protobuf";
          m.put("otel.exporter.otlp.protocol", effectiveProtocol);
          break;
        case "grpc":
          effectiveProtocol = "grpc";
          m.put("otel.exporter.otlp.protocol", effectiveProtocol);
          break;
        default:
          diag.warn("OPENLOG_PROTOCOL=\"" + protocol + "\": use http/protobuf or grpc; ignored");
      }
    }

    String compression = s.get("OPENLOG_COMPRESSION");
    if (compression != null) {
      String c = compression.toLowerCase(Locale.ROOT);
      if (c.equals("gzip") || c.equals("none")) {
        m.put("otel.exporter.otlp.compression", c);
      } else {
        diag.warn("OPENLOG_COMPRESSION=\"" + compression + "\": use gzip or none; ignored");
      }
    }

    String endpoint = s.get("OPENLOG_ENDPOINT");
    if (endpoint != null) {
      String e = normalizeEndpoint(endpoint);
      if (e == null) {
        diag.warn("OPENLOG_ENDPOINT=\"" + endpoint + "\" is not a valid http(s) URL; ignored");
      } else {
        m.put("otel.exporter.otlp.endpoint", e);
      }
    }

    String licenseKey = s.get("OPENLOG_LICENSE_KEY");
    String rawHeaders = config.getString(HEADERS);
    if (licenseKey != null) {
      Map<String, String> add = new LinkedHashMap<>();
      add.put(LICENSE_KEY_HEADER, licenseKey);
      m.put(HEADERS, mergeKV(rawHeaders, add));
    } else if (!hasHeader(rawHeaders, LICENSE_KEY_HEADER) && !hasHeader(rawHeaders, "authorization")) {
      diag.warn("no license key configured (OPENLOG_LICENSE_KEY); openlog ingest will reject the data");
    }

    String serviceName = s.get("OPENLOG_SERVICE_NAME");
    if (serviceName != null) {
      m.put("otel.service.name", serviceName);
    }

    Map<String, String> attrs = OpenlogSettings.parseKV(s.get("OPENLOG_RESOURCE_ATTRIBUTES"));
    putIfSet(attrs, "service.version", s.get("OPENLOG_SERVICE_VERSION"));
    putIfSet(attrs, "service.namespace", s.get("OPENLOG_SERVICE_NAMESPACE"));
    putIfSet(attrs, "deployment.environment.name", s.get("OPENLOG_ENVIRONMENT"));
    putIfSet(attrs, "host.id", s.get("OPENLOG_HOST_ID"));
    if (!attrs.isEmpty()) {
      m.put(RESOURCE_ATTRIBUTES, mergeKV(config.getString(RESOURCE_ATTRIBUTES), attrs));
    }

    Boolean runtime = s.bool("OPENLOG_RUNTIME_METRICS", diag);
    if (runtime != null) {
      m.put("otel.instrumentation.runtime-telemetry.enabled", runtime.toString());
    }

    String interval = s.get("OPENLOG_METRIC_EXPORT_INTERVAL");
    if (interval != null) {
      Double ms = OpenlogSettings.parseGoDurationMillis(interval);
      if (ms == null || ms < 1) {
        diag.warn("OPENLOG_METRIC_EXPORT_INTERVAL=\"" + interval + "\" is not a positive duration; ignored");
      } else {
        m.put("otel.metric.export.interval", Long.toString(Math.round(ms)));
      }
    }

    Boolean logsExport = s.bool("OPENLOG_LOGS_EXPORT", diag);
    if (logsExport != null && !logsExport) {
      m.put("otel.logs.exporter", "none");
    }

    for (String name : OpenlogSettings.list(s.get("OPENLOG_INSTRUMENTATIONS_DISABLED"))) {
      m.put("otel.instrumentation." + name + ".enabled", "false");
    }

    dbQueryTextMode(s, diag);
    s.bool("OPENLOG_SAMPLING_RV", diag);
    String ratio = s.get("OPENLOG_SAMPLING_RATIO");
    if (ratio != null && parseDouble(ratio) == null) {
      diag.warn("OPENLOG_SAMPLING_RATIO=\"" + ratio + "\" is not a number; ignored");
    }
    return m;
  }

  /** OPENLOG_DB_QUERY_TEXT: sanitized (default), raw or off. */
  public static DbStatementSpanExporter.Mode dbQueryTextMode(OpenlogSettings s, Diag diag) {
    String v = s.get("OPENLOG_DB_QUERY_TEXT");
    if (v == null) {
      return DbStatementSpanExporter.Mode.SANITIZED;
    }
    DbStatementSpanExporter.Mode mode = DbStatementSpanExporter.Mode.parse(v);
    if (mode == null) {
      diag.warn("OPENLOG_DB_QUERY_TEXT=\"" + v + "\": use sanitized, raw or off; ignored");
      return DbStatementSpanExporter.Mode.SANITIZED;
    }
    return mode;
  }

  /**
   * The openlog sampler, unless the application configured a sampler it does not replace (e.g. jaeger_remote,
   * always_off): then {@code configured} is kept. Ratio: OPENLOG_SAMPLING_RATIO, else otel.traces.sampler.arg of a
   * *traceidratio (or openlog) sampler, else 1.
   */
  public static Sampler sampler(OpenlogSettings s, Sampler configured, ConfigProperties config, Diag diag) {
    String name = config.getString(SAMPLER);
    String n = name == null ? null : name.trim().toLowerCase(Locale.ROOT);
    if (n != null && configured != null && !REPLACEABLE_SAMPLERS.contains(n)) {
      if (s.get("OPENLOG_SAMPLING_RATIO") != null) {
        diag.warn("otel.traces.sampler=" + name + " is set; OPENLOG_SAMPLING_RATIO is ignored");
      }
      return configured;
    }
    double ratio = 1.0;
    if (n != null && (n.endsWith("traceidratio") || n.equals("openlog"))) {
      String arg = config.getString(SAMPLER_ARG);
      Double d = arg == null ? null : parseDouble(arg);
      if (d != null) {
        ratio = d;
      }
    }
    String r = s.get("OPENLOG_SAMPLING_RATIO");
    if (r != null) {
      Double d = parseDouble(r);
      if (d != null) {
        ratio = d;
      }
    }
    if (Double.isNaN(ratio) || ratio < 0 || ratio > 1) {
      diag.warn("sampling ratio " + ratio + " outside 0..1; clamped");
      ratio = Double.isNaN(ratio) ? 1 : Math.min(Math.max(ratio, 0), 1);
    }
    Boolean rv = s.bool("OPENLOG_SAMPLING_RV", null);
    List<String> ignore = OpenlogSettings.list(s.get("OPENLOG_HTTP_IGNORE_PATHS"));
    return OpenlogSampler.create(
        ratio, rv != null && rv, OpenlogSampler.DEFAULT_RANDOMNESS, new HashSet<>(ignore));
  }

  static Double parseDouble(String v) {
    try {
      return Double.parseDouble(v.trim());
    } catch (NumberFormatException e) {
      return null;
    }
  }

  /** Adds http(s):// when missing (no scheme = TLS), validates, removes trailing slashes; null when invalid. */
  static String normalizeEndpoint(String endpoint) {
    String e = endpoint.trim();
    if (!e.contains("://")) {
      e = "https://" + e;
    }
    try {
      URI u = new URI(e);
      String scheme = u.getScheme() == null ? "" : u.getScheme().toLowerCase(Locale.ROOT);
      if (u.getHost() == null || !(scheme.equals("http") || scheme.equals("https"))) {
        return null;
      }
    } catch (Exception ex) {
      return null;
    }
    while (e.endsWith("/")) {
      e = e.substring(0, e.length() - 1);
    }
    return e;
  }

  static boolean hasHeader(String rawHeaders, String name) {
    if (rawHeaders == null) {
      return false;
    }
    for (String part : rawHeaders.split(",")) {
      int i = part.indexOf('=');
      if (i > 0 && part.substring(0, i).trim().equalsIgnoreCase(name)) {
        return true;
      }
    }
    return false;
  }

  /**
   * Appends {@code add} (percent-encoded) to a raw "k=v,…" list; existing entries with the same keys are removed, other
   * entries keep their raw (already encoded) form.
   */
  static String mergeKV(String raw, Map<String, String> add) {
    StringBuilder b = new StringBuilder();
    if (raw != null) {
      for (String part : raw.split(",")) {
        String p = part.trim();
        int i = p.indexOf('=');
        if (i <= 0 || add.containsKey(p.substring(0, i).trim())) {
          continue;
        }
        if (b.length() > 0) {
          b.append(',');
        }
        b.append(p);
      }
    }
    for (Map.Entry<String, String> e : add.entrySet()) {
      if (b.length() > 0) {
        b.append(',');
      }
      b.append(e.getKey()).append('=').append(OpenlogSettings.encode(e.getValue()));
    }
    return b.toString();
  }

  private static void putIfSet(Map<String, String> m, String k, String v) {
    if (v != null) {
      m.put(k, v);
    }
  }
}

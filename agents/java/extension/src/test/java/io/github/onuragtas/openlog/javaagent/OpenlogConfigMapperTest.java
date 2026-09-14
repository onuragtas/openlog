package io.github.onuragtas.openlog.javaagent;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertSame;
import static org.junit.jupiter.api.Assertions.assertTrue;

import io.github.onuragtas.openlog.javaagent.db.DbStatementSpanExporter;
import io.opentelemetry.sdk.autoconfigure.spi.internal.DefaultConfigProperties;
import io.opentelemetry.sdk.trace.samplers.Sampler;
import java.io.ByteArrayOutputStream;
import java.io.PrintStream;
import java.nio.charset.StandardCharsets;
import java.util.HashMap;
import java.util.Map;
import java.util.Properties;
import org.junit.jupiter.api.Test;

class OpenlogConfigMapperTest {
  final ByteArrayOutputStream diagOut = new ByteArrayOutputStream();
  final Diag diag = new Diag(Diag.DEBUG, new PrintStream(diagOut, true, StandardCharsets.UTF_8));

  static OpenlogSettings settings(String... kv) {
    Map<String, String> env = new HashMap<>();
    for (int i = 0; i < kv.length; i += 2) {
      env.put(kv[i], kv[i + 1]);
    }
    return new OpenlogSettings(env, new Properties());
  }

  static DefaultConfigProperties config(String... kv) {
    Map<String, String> m = new HashMap<>(OpenlogConfigMapper.defaults());
    for (int i = 0; i < kv.length; i += 2) {
      m.put(kv[i], kv[i + 1]);
    }
    return DefaultConfigProperties.createFromMap(m);
  }

  String diagText() {
    return diagOut.toString(StandardCharsets.UTF_8);
  }

  @Test
  void defaults() {
    Map<String, String> d = OpenlogConfigMapper.defaults();
    assertEquals("http/protobuf", d.get("otel.exporter.otlp.protocol"));
    assertEquals("gzip", d.get("otel.exporter.otlp.compression"));
    assertEquals("false", d.get("otel.instrumentation.common.db.query-sanitization.enabled"));
    Map<String, String> m = OpenlogConfigMapper.overrides(settings(), config(), diag);
    assertTrue(m.isEmpty(), m.toString());
    assertTrue(diagText().contains("no license key configured"));
  }

  @Test
  void mapsOpenlogVariables() {
    OpenlogSettings s =
        settings(
            "OPENLOG_LICENSE_KEY", "key,with=odd chars",
            "OPENLOG_ENDPOINT", "ingest.example.com:4318/",
            "OPENLOG_PROTOCOL", "grpc",
            "OPENLOG_COMPRESSION", "none",
            "OPENLOG_SERVICE_NAME", "checkout",
            "OPENLOG_SERVICE_VERSION", "1.4.0",
            "OPENLOG_SERVICE_NAMESPACE", "shop",
            "OPENLOG_ENVIRONMENT", "production",
            "OPENLOG_HOST_ID", "host-0000-0001",
            "OPENLOG_RESOURCE_ATTRIBUTES", "team=payments,region=eu%20west",
            "OPENLOG_RUNTIME_METRICS", "false",
            "OPENLOG_METRIC_EXPORT_INTERVAL", "1m30s",
            "OPENLOG_LOGS_EXPORT", "false",
            "OPENLOG_ENABLED", "false",
            "OPENLOG_INSTRUMENTATIONS_DISABLED", "jdbc, kafka");
    Map<String, String> m =
        OpenlogConfigMapper.overrides(
            s,
            config(
                "otel.exporter.otlp.headers", "x-extra=a%20b,openlog-license-key=old",
                "otel.resource.attributes", "service.version=0.1,deployment.environment.name=dev,k=v%2Cw"),
            diag);
    assertEquals("x-extra=a%20b,openlog-license-key=key%2Cwith%3Dodd%20chars", m.get("otel.exporter.otlp.headers"));
    assertEquals("https://ingest.example.com:4318", m.get("otel.exporter.otlp.endpoint"));
    assertEquals("grpc", m.get("otel.exporter.otlp.protocol"));
    assertEquals("none", m.get("otel.exporter.otlp.compression"));
    assertEquals("checkout", m.get("otel.service.name"));
    assertEquals(
        "k=v%2Cw,team=payments,region=eu%20west,service.version=1.4.0,service.namespace=shop,"
            + "deployment.environment.name=production,host.id=host-0000-0001",
        m.get("otel.resource.attributes"));
    assertEquals("false", m.get("otel.instrumentation.runtime-telemetry.enabled"));
    assertEquals("90000", m.get("otel.metric.export.interval"));
    assertEquals("none", m.get("otel.logs.exporter"));
    assertEquals("true", m.get("otel.sdk.disabled"));
    assertEquals("false", m.get("otel.instrumentation.jdbc.enabled"));
    assertEquals("false", m.get("otel.instrumentation.kafka.enabled"));
    assertFalse(diagText().contains("WARN"), diagText());

    // the merged values parse back like the SDK parses them
    Map<String, String> headers = OpenlogSettings.parseKV(m.get("otel.exporter.otlp.headers"));
    assertEquals("key,with=odd chars", headers.get("openlog-license-key"));
  }

  @Test
  void systemPropertiesWinOverEnvironment() {
    Properties p = new Properties();
    p.setProperty("openlog.service.name", "from-sysprop");
    OpenlogSettings s = new OpenlogSettings(Map.of("OPENLOG_SERVICE_NAME", "from-env"), p);
    assertEquals("from-sysprop", s.get("OPENLOG_SERVICE_NAME"));
    assertEquals("openlog.license.key", OpenlogSettings.propertyName("OPENLOG_LICENSE_KEY"));
  }

  @Test
  void invalidValuesAreWarnedAndIgnored() {
    Map<String, String> m =
        OpenlogConfigMapper.overrides(
            settings(
                "OPENLOG_ENDPOINT", "ftp://x",
                "OPENLOG_PROTOCOL", "thrift",
                "OPENLOG_COMPRESSION", "br",
                "OPENLOG_RUNTIME_METRICS", "maybe",
                "OPENLOG_METRIC_EXPORT_INTERVAL", "soon",
                "OPENLOG_DB_QUERY_TEXT", "masked",
                "OPENLOG_SAMPLING_RATIO", "half"),
            config("otel.exporter.otlp.headers", "Authorization=Bearer%20x"),
            diag);
    assertTrue(m.isEmpty(), m.toString());
    String t = diagText();
    for (String v : new String[] {"OPENLOG_ENDPOINT", "OPENLOG_PROTOCOL", "OPENLOG_COMPRESSION", "OPENLOG_RUNTIME_METRICS", "OPENLOG_METRIC_EXPORT_INTERVAL", "OPENLOG_DB_QUERY_TEXT", "OPENLOG_SAMPLING_RATIO"}) {
      assertTrue(t.contains(v), v + " in " + t);
    }
    assertFalse(t.contains("no license key"), t);
  }

  @Test
  void durationsAndBooleans() {
    assertEquals(500.0, OpenlogSettings.parseGoDurationMillis("500ms"));
    assertEquals(1500.0, OpenlogSettings.parseGoDurationMillis("1.5s"));
    assertEquals(3_723_000.0, OpenlogSettings.parseGoDurationMillis("1h2m3s"));
    assertEquals(0.0, OpenlogSettings.parseGoDurationMillis("0"));
    assertNull(OpenlogSettings.parseGoDurationMillis("5"));
    assertNull(OpenlogSettings.parseGoDurationMillis("1s x"));
    assertEquals(Boolean.TRUE, OpenlogSettings.parseBool("T"));
    assertNull(OpenlogSettings.parseBool("yes"));
    assertNull(OpenlogConfigMapper.normalizeEndpoint("http:///nohost"));
    assertEquals("http://localhost:4318/base", OpenlogConfigMapper.normalizeEndpoint("http://localhost:4318/base//"));
  }

  @Test
  void samplerSelection() {
    Sampler custom = Sampler.alwaysOff();
    // default: openlog sampler with ratio 1
    Sampler s = OpenlogConfigMapper.sampler(settings(), Sampler.parentBased(Sampler.alwaysOn()), config(), diag);
    assertTrue(s.getDescription().contains("ParentBased"), s.getDescription());
    assertTrue(s.getDescription().contains("OpenlogRemoteParentSampled"), s.getDescription());
    // OTEL ratio sampler arg is honoured
    s = OpenlogConfigMapper.sampler(settings(), custom, config("otel.traces.sampler", "parentbased_traceidratio", "otel.traces.sampler.arg", "0.25"), diag);
    assertTrue(s.getDescription().contains("OpenlogConsistentRatio{0.25}"), s.getDescription());
    // OPENLOG_SAMPLING_RATIO wins
    s = OpenlogConfigMapper.sampler(settings("OPENLOG_SAMPLING_RATIO", "0.5", "OPENLOG_HTTP_IGNORE_PATHS", "/healthz"), custom, config("otel.traces.sampler", "traceidratio", "otel.traces.sampler.arg", "0.25"), diag);
    assertTrue(s.getDescription().contains("OpenlogConsistentRatio{0.5}"), s.getDescription());
    assertTrue(s.getDescription().contains("/healthz"), s.getDescription());
    // an explicitly chosen other sampler is kept
    assertSame(custom, OpenlogConfigMapper.sampler(settings("OPENLOG_SAMPLING_RATIO", "0.5"), custom, config("otel.traces.sampler", "always_off"), diag));
    assertTrue(diagText().contains("OPENLOG_SAMPLING_RATIO is ignored"));
    // clamped
    s = OpenlogConfigMapper.sampler(settings("OPENLOG_SAMPLING_RATIO", "7"), custom, config(), diag);
    assertFalse(s.getDescription().contains("OpenlogConsistentRatio"), s.getDescription());
    assertEquals(DbStatementSpanExporter.Mode.RAW, OpenlogConfigMapper.dbQueryTextMode(settings("OPENLOG_DB_QUERY_TEXT", "raw"), diag));
  }
}

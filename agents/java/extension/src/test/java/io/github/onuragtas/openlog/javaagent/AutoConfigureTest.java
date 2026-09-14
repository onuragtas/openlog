package io.github.onuragtas.openlog.javaagent;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import io.opentelemetry.sdk.autoconfigure.AutoConfiguredOpenTelemetrySdk;
import io.opentelemetry.sdk.autoconfigure.internal.AutoConfigureUtil;
import io.opentelemetry.sdk.autoconfigure.spi.ConfigProperties;
import io.opentelemetry.sdk.trace.SdkTracerProvider;
import java.util.List;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;

/** The SPI registrations work with the SDK autoconfiguration the javaagent uses. */
class AutoConfigureTest {
  static final List<String> PROPS =
      List.of(
          "openlog.license.key",
          "openlog.sampling.ratio",
          "openlog.service.name",
          "openlog.host.id",
          "otel.traces.exporter",
          "otel.metrics.exporter",
          "otel.logs.exporter");

  @AfterEach
  void clear() {
    PROPS.forEach(System::clearProperty);
  }

  @Test
  void customizerProviderIsLoadedAndApplied() {
    System.setProperty("openlog.license.key", "k1");
    System.setProperty("openlog.sampling.ratio", "0.5");
    System.setProperty("openlog.service.name", "autoconf");
    System.setProperty("openlog.host.id", "autoconf-host-1");
    System.setProperty("otel.traces.exporter", "none");
    System.setProperty("otel.metrics.exporter", "none");
    System.setProperty("otel.logs.exporter", "none");
    AutoConfiguredOpenTelemetrySdk sdk = AutoConfiguredOpenTelemetrySdk.builder().build();
    try {
      ConfigProperties config = AutoConfigureUtil.getConfig(sdk);
      assertEquals("openlog-license-key=k1", config.getString("otel.exporter.otlp.headers"));
      assertEquals("gzip", config.getString("otel.exporter.otlp.compression"));
      assertEquals("autoconf", config.getString("otel.service.name"));
      SdkTracerProvider tp = sdk.getOpenTelemetrySdk().getSdkTracerProvider();
      assertTrue(tp.getSampler().getDescription().contains("OpenlogConsistentRatio{0.5}"), tp.getSampler().getDescription());
      String desc = tp.toString();
      assertTrue(desc.contains("telemetry.distro.name=\"openlog\""), desc);
      assertTrue(desc.contains("host.id=\"autoconf-host-1\""), desc);
      assertTrue(desc.contains("service.name=\"autoconf\""), desc);
    } finally {
      sdk.getOpenTelemetrySdk().close();
    }
  }
}

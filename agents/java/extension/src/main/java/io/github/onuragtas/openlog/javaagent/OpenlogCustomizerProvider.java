/*
 * Copyright The openlog Authors
 * SPDX-License-Identifier: Apache-2.0
 */
package io.github.onuragtas.openlog.javaagent;

import io.github.onuragtas.openlog.javaagent.db.DbStatementSpanExporter;
import io.github.onuragtas.openlog.javaagent.resource.OpenlogResource;
import io.opentelemetry.sdk.autoconfigure.spi.AutoConfigurationCustomizer;
import io.opentelemetry.sdk.autoconfigure.spi.AutoConfigurationCustomizerProvider;
import io.opentelemetry.sdk.autoconfigure.spi.ConfigProperties;
import io.opentelemetry.sdk.resources.Resource;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.Map;

/**
 * The openlog distribution's SDK customizations, applied by the javaagent's autoconfiguration: OPENLOG_* settings,
 * defaults, the openlog sampler, resource detection and DB statement handling.
 */
public final class OpenlogCustomizerProvider implements AutoConfigurationCustomizerProvider {
  private final OpenlogSettings settings;

  public OpenlogCustomizerProvider() {
    this(OpenlogSettings.fromEnvironment());
  }

  OpenlogCustomizerProvider(OpenlogSettings settings) {
    this.settings = settings;
  }

  @Override
  public void customize(AutoConfigurationCustomizer customizer) {
    final OpenlogSettings s = settings;
    final Diag diag = Diag.fromSettings(s);
    customizer.addPropertiesSupplier(OpenlogConfigMapper::defaults);
    customizer.addPropertiesCustomizer(config -> OpenlogConfigMapper.overrides(s, config, diag));
    customizer.addSamplerCustomizer((sampler, config) -> OpenlogConfigMapper.sampler(s, sampler, config, diag));
    customizer.addResourceCustomizer(
        (resource, config) -> customizeResource(resource, config, s, diag));
    final DbStatementSpanExporter.Mode mode = OpenlogConfigMapper.dbQueryTextMode(s, Diag.fromSettingsQuiet());
    customizer.addSpanExporterCustomizer((exporter, config) -> new DbStatementSpanExporter(exporter, mode));
    diag.debug("openlog Java agent " + OpenlogVersion.VERSION + " (OpenTelemetry Java agent " + OpenlogVersion.UPSTREAM + ")");
  }

  static Resource customizeResource(Resource resource, ConfigProperties config, OpenlogSettings s, Diag diag) {
    try {
      return OpenlogResource.customize(resource, userResourceAttributes(config), s, diag, isLinux());
    } catch (RuntimeException e) {
      diag.warn("resource detection failed: " + e);
      return resource;
    }
  }

  static Map<String, String> userResourceAttributes(ConfigProperties config) {
    String raw = config.getString(OpenlogConfigMapper.RESOURCE_ATTRIBUTES);
    if (raw == null) {
      return Collections.emptyMap();
    }
    return new LinkedHashMap<>(OpenlogSettings.parseKV(raw));
  }

  static boolean isLinux() {
    return System.getProperty("os.name", "").toLowerCase(java.util.Locale.ROOT).startsWith("linux");
  }

  @Override
  public int order() {
    // After the javaagent's own customizers, so the openlog resource and sampler are applied last.
    return 1000;
  }
}

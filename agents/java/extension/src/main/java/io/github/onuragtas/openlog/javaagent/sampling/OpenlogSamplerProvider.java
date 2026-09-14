/*
 * Copyright The openlog Authors
 * SPDX-License-Identifier: Apache-2.0
 */
package io.github.onuragtas.openlog.javaagent.sampling;

import io.github.onuragtas.openlog.javaagent.Diag;
import io.github.onuragtas.openlog.javaagent.OpenlogConfigMapper;
import io.github.onuragtas.openlog.javaagent.OpenlogSettings;
import io.opentelemetry.sdk.autoconfigure.spi.ConfigProperties;
import io.opentelemetry.sdk.autoconfigure.spi.traces.ConfigurableSamplerProvider;
import io.opentelemetry.sdk.trace.samplers.Sampler;

/** {@code otel.traces.sampler=openlog}: the openlog sampler (also installed by default, see OpenlogCustomizerProvider). */
public final class OpenlogSamplerProvider implements ConfigurableSamplerProvider {
  @Override
  public Sampler createSampler(ConfigProperties config) {
    OpenlogSettings s = OpenlogSettings.fromEnvironment();
    return OpenlogConfigMapper.sampler(s, null, config, Diag.fromSettings(s));
  }

  @Override
  public String getName() {
    return "openlog";
  }
}

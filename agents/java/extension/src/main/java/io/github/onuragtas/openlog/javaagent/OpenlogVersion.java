/*
 * Copyright The openlog Authors
 * SPDX-License-Identifier: Apache-2.0
 */
package io.github.onuragtas.openlog.javaagent;

import java.io.InputStream;
import java.util.Properties;

/** Version of the openlog distribution ({@code telemetry.distro.version}) and the upstream agent. */
public final class OpenlogVersion {
  public static final String DISTRO_NAME = "openlog";
  public static final String VERSION;
  public static final String UPSTREAM;

  static {
    Properties p = new Properties();
    try (InputStream in = OpenlogVersion.class.getResourceAsStream("version.properties")) {
      if (in != null) {
        p.load(in);
      }
    } catch (Exception ignored) {
      // defaults below
    }
    VERSION = p.getProperty("version", "0.0.0-dev");
    UPSTREAM = p.getProperty("upstream", "");
  }

  private OpenlogVersion() {}
}

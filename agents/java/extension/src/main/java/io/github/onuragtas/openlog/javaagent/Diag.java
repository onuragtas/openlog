/*
 * Copyright The openlog Authors
 * SPDX-License-Identifier: Apache-2.0
 */
package io.github.onuragtas.openlog.javaagent;

import java.io.PrintStream;
import java.util.Locale;

/**
 * Agent diagnostics on stderr ({@code OPENLOG_LOG_LEVEL}: debug, info, warn, error, off). Plain stderr instead of
 * java.util.logging: the agent starts before the application configures (or replaces) the JUL LogManager.
 */
public final class Diag {
  static final int DEBUG = 0;
  static final int INFO = 1;
  static final int WARN = 2;
  static final int ERROR = 3;
  static final int OFF = 4;

  private final int level;
  private final PrintStream out;

  Diag(int level, PrintStream out) {
    this.level = level;
    this.out = out;
  }

  public static Diag fromSettings(OpenlogSettings s) {
    String v = s.get("OPENLOG_LOG_LEVEL");
    if (v == null) {
      v = s.env("OTEL_LOG_LEVEL");
    }
    Integer l = parseLevel(v);
    Diag d = new Diag(l == null ? WARN : l, System.err);
    if (v != null && l == null) {
      d.warn("OPENLOG_LOG_LEVEL: unknown log level \"" + v + "\" (debug, info, warn, error, off)");
    }
    return d;
  }

  /** A diagnostics sink that prints nothing (for settings already validated elsewhere). */
  public static Diag fromSettingsQuiet() {
    return new Diag(OFF, System.err);
  }

  static Integer parseLevel(String v) {
    if (v == null) {
      return null;
    }
    switch (v.trim().toLowerCase(Locale.ROOT)) {
      case "debug":
      case "trace":
      case "verbose":
      case "all":
        return DEBUG;
      case "info":
        return INFO;
      case "warn":
      case "warning":
        return WARN;
      case "error":
        return ERROR;
      case "off":
      case "none":
        return OFF;
      default:
        return null;
    }
  }

  public boolean debugEnabled() {
    return level <= DEBUG;
  }

  public void debug(String msg) {
    log(DEBUG, "DEBUG", msg);
  }

  public void info(String msg) {
    log(INFO, "INFO", msg);
  }

  public void warn(String msg) {
    log(WARN, "WARN", msg);
  }

  public void error(String msg) {
    log(ERROR, "ERROR", msg);
  }

  private void log(int l, String name, String msg) {
    if (l >= level && level < OFF) {
      out.println("[openlog] " + name + " " + msg);
    }
  }
}

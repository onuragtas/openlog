/*
 * Copyright The openlog Authors
 * SPDX-License-Identifier: Apache-2.0
 */
package io.github.onuragtas.openlog.javaagent;

import java.io.UnsupportedEncodingException;
import java.net.URLDecoder;
import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Properties;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * Raw openlog settings: {@code OPENLOG_FOO_BAR} environment variables, overridden by the system property
 * {@code openlog.foo.bar} (the OpenTelemetry Java convention: system properties win over the environment).
 */
public final class OpenlogSettings {
  private final Map<String, String> env;
  private final Properties sys;

  public OpenlogSettings(Map<String, String> env, Properties sys) {
    this.env = env == null ? Collections.<String, String>emptyMap() : env;
    this.sys = sys == null ? new Properties() : sys;
  }

  public static OpenlogSettings fromEnvironment() {
    return new OpenlogSettings(System.getenv(), System.getProperties());
  }

  /** OPENLOG_LICENSE_KEY → openlog.license.key */
  public static String propertyName(String envName) {
    return envName.toLowerCase(Locale.ROOT).replace('_', '.');
  }

  /** The trimmed, non-empty value of the system property or environment variable, else null. */
  public String get(String envName) {
    String v = trimToNull(sys.getProperty(propertyName(envName)));
    return v != null ? v : trimToNull(env.get(envName));
  }

  /** A plain environment variable (trimmed, non-empty) without the system property lookup. */
  public String env(String name) {
    return trimToNull(env.get(name));
  }

  public boolean hasEnv(String name) {
    return env.containsKey(name);
  }

  static String trimToNull(String v) {
    if (v == null) {
      return null;
    }
    String t = v.trim();
    return t.isEmpty() ? null : t;
  }

  /** strconv.ParseBool */
  static Boolean parseBool(String v) {
    if (v == null) {
      return null;
    }
    switch (v) {
      case "1":
      case "t":
      case "T":
      case "true":
      case "TRUE":
      case "True":
        return Boolean.TRUE;
      case "0":
      case "f":
      case "F":
      case "false":
      case "FALSE":
      case "False":
        return Boolean.FALSE;
      default:
        return null;
    }
  }

  /** A boolean setting; invalid values are reported and ignored. */
  Boolean bool(String name, Diag diag) {
    String v = get(name);
    if (v == null) {
      return null;
    }
    Boolean b = parseBool(v);
    if (b == null && diag != null) {
      diag.warn(name + "=\"" + v + "\" is not a boolean; ignored");
    }
    return b;
  }

  private static final Pattern DURATION_PART =
      Pattern.compile("(\\d+(?:\\.\\d*)?|\\.\\d+)(ns|us|µs|μs|ms|s|m|h)");

  /** Parses a Go duration ("1m30s", "500ms", "1.5s") to milliseconds; null when invalid. */
  static Double parseGoDurationMillis(String s) {
    if (s == null) {
      return null;
    }
    String str = s.trim();
    if (str.isEmpty()) {
      return null;
    }
    if (str.equals("0")) {
      return 0.0;
    }
    Matcher m = DURATION_PART.matcher(str);
    double total = 0;
    int pos = 0;
    while (pos < str.length()) {
      m.region(pos, str.length());
      if (!m.lookingAt()) {
        return null;
      }
      double n = Double.parseDouble(m.group(1));
      switch (m.group(2)) {
        case "ns":
          total += n / 1e6;
          break;
        case "us":
        case "µs":
        case "μs":
          total += n / 1e3;
          break;
        case "ms":
          total += n;
          break;
        case "s":
          total += n * 1000;
          break;
        case "m":
          total += n * 60_000;
          break;
        default:
          total += n * 3_600_000;
          break;
      }
      pos = m.end();
    }
    return total;
  }

  /** Comma-separated list, trimmed, empty items removed. */
  static List<String> list(String v) {
    List<String> out = new ArrayList<>();
    if (v == null) {
      return out;
    }
    for (String p : v.split(",")) {
      String t = p.trim();
      if (!t.isEmpty()) {
        out.add(t);
      }
    }
    return out;
  }

  /** W3C-baggage-like "k1=v1,k2=v2" (values may be percent-encoded, like OTEL_RESOURCE_ATTRIBUTES). */
  static Map<String, String> parseKV(String s) {
    Map<String, String> out = new LinkedHashMap<>();
    if (s == null) {
      return out;
    }
    for (String part : s.split(",")) {
      int idx = part.indexOf('=');
      if (idx < 0) {
        continue;
      }
      String k = part.substring(0, idx).trim();
      if (k.isEmpty()) {
        continue;
      }
      out.put(k, decode(part.substring(idx + 1).trim()));
    }
    return out;
  }

  static String decode(String v) {
    try {
      return URLDecoder.decode(v, "UTF-8");
    } catch (UnsupportedEncodingException | IllegalArgumentException e) {
      return v;
    }
  }

  private static final String HEX = "0123456789ABCDEF";

  /** Percent-encodes everything outside A-Z a-z 0-9 - . _ ~ : / @ (safe for the URLDecoder-based OTel parsers). */
  static String encode(String v) {
    StringBuilder b = new StringBuilder(v.length());
    byte[] bytes;
    try {
      bytes = v.getBytes("UTF-8");
    } catch (UnsupportedEncodingException e) {
      return v;
    }
    for (byte x : bytes) {
      int c = x & 0xff;
      if ((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
          || c == '-' || c == '.' || c == '_' || c == '~' || c == ':' || c == '/' || c == '@') {
        b.append((char) c);
      } else {
        b.append('%').append(HEX.charAt(c >> 4)).append(HEX.charAt(c & 0xf));
      }
    }
    return b.toString();
  }
}

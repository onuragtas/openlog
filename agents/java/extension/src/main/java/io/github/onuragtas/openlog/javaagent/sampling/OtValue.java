/*
 * Copyright The openlog Authors
 * SPDX-License-Identifier: Apache-2.0
 */
package io.github.onuragtas.openlog.javaagent.sampling;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.regex.Pattern;

/** The ordered sub-keys of the tracestate {@code ot} entry ("k1:v1;k2:v2"), immutable. */
final class OtValue {
  static final class Field {
    final String key;
    final String value;

    Field(String key, String value) {
      this.key = key;
      this.value = value;
    }
  }

  private static final Pattern HEX14 = Pattern.compile("[0-9a-fA-F]{14}");
  private static final Pattern HEX1_14 = Pattern.compile("[0-9a-fA-F]{1,14}");
  private static final Pattern INT = Pattern.compile("[+-]?\\d+");

  static final OtValue EMPTY = new OtValue(Collections.<Field>emptyList());

  final List<Field> fields;

  OtValue(List<Field> fields) {
    this.fields = Collections.unmodifiableList(fields);
  }

  static OtValue parse(String s) {
    if (s == null || s.isEmpty()) {
      return EMPTY;
    }
    List<Field> out = new ArrayList<>();
    for (String part : s.split(";", -1)) {
      int i = part.indexOf(':');
      if (i > 0) {
        out.add(new Field(part.substring(0, i), part.substring(i + 1)));
      }
    }
    return new OtValue(out);
  }

  String get(String k) {
    for (Field f : fields) {
      if (f.key.equals(k)) {
        return f.value;
      }
    }
    return null;
  }

  /** Explicit 56-bit randomness ot=rv:<14 hex digits>, else null. */
  Long randomness() {
    String v = get("rv");
    if (v == null || !HEX14.matcher(v).matches()) {
      return null;
    }
    return Long.parseLong(v, 16);
  }

  /** p from th:<hex> (p = 1 − T/2^56) or legacy p:<n> (p = 2^−n), else null. */
  Double probability() {
    String th = get("th");
    if (th != null) {
      Long t = OpenlogSampler.parseThreshold(th);
      if (t != null) {
        return 1 - (double) t / (double) OpenlogSampler.MAX_THRESHOLD;
      }
    }
    String p = get("p");
    if (p != null && INT.matcher(p).matches()) {
      try {
        int n = Integer.parseInt(p);
        if (n >= 0 && n <= 63) {
          return n == 63 ? 0.0 : Math.scalb(1.0, -n);
        }
      } catch (NumberFormatException ignored) {
        // out of int range
      }
    }
    return null;
  }

  static boolean isThreshold(String s) {
    return HEX1_14.matcher(s).matches();
  }

  /** o with sub-key k set to v, moved to the front. */
  OtValue with(String k, String v) {
    List<Field> out = new ArrayList<>(fields.size() + 1);
    out.add(new Field(k, v));
    for (Field f : fields) {
      if (!f.key.equals(k)) {
        out.add(f);
      }
    }
    return new OtValue(out);
  }

  /** o with sub-key k set to v, appended at the end. */
  OtValue withLast(String k, String v) {
    List<Field> out = new ArrayList<>(without(k).fields);
    out.add(new Field(k, v));
    return new OtValue(out);
  }

  OtValue without(String k) {
    List<Field> out = new ArrayList<>(fields.size());
    for (Field f : fields) {
      if (!f.key.equals(k)) {
        out.add(f);
      }
    }
    return new OtValue(out);
  }

  boolean isEmpty() {
    return fields.isEmpty();
  }

  static String toString(List<Field> fields) {
    StringBuilder b = new StringBuilder();
    for (int i = 0; i < fields.size(); i++) {
      if (i > 0) {
        b.append(';');
      }
      b.append(fields.get(i).key).append(':').append(fields.get(i).value);
    }
    return b.toString();
  }

  @Override
  public String toString() {
    return toString(fields);
  }
}

/*
 * Copyright The openlog Authors
 * SPDX-License-Identifier: Apache-2.0
 */
package io.github.onuragtas.openlog.javaagent.db;

import io.opentelemetry.api.common.AttributeKey;
import io.opentelemetry.api.common.Attributes;
import io.opentelemetry.api.common.AttributesBuilder;
import io.opentelemetry.sdk.common.CompletableResultCode;
import io.opentelemetry.sdk.trace.data.DelegatingSpanData;
import io.opentelemetry.sdk.trace.data.SpanData;
import io.opentelemetry.sdk.trace.export.SpanExporter;
import java.util.ArrayList;
import java.util.Collection;
import java.util.List;
import java.util.Locale;

/**
 * Applies {@code OPENLOG_DB_QUERY_TEXT} to {@code db.query.text} / {@code db.statement} of every exported span:
 * {@code sanitized} (default) with the Go agent's algorithm, {@code raw} unchanged, {@code off} removed. Runs on the
 * exporter path, so attributes set at any time are covered and a statement is never exported unsanitized. The
 * upstream agent's own sanitizer is disabled by default so the statement reaches this exporter unmodified and the
 * result is identical to the Go and Node.js agents.
 */
public final class DbStatementSpanExporter implements SpanExporter {
  public enum Mode {
    SANITIZED,
    RAW,
    OFF;

    /** Parses sanitized/raw/off; null when invalid. */
    public static Mode parse(String v) {
      if (v == null) {
        return null;
      }
      switch (v.trim().toLowerCase(Locale.ROOT)) {
        case "sanitized":
          return SANITIZED;
        case "raw":
          return RAW;
        case "off":
          return OFF;
        default:
          return null;
      }
    }
  }

  static final AttributeKey<String> DB_QUERY_TEXT = AttributeKey.stringKey("db.query.text");
  static final AttributeKey<String> DB_STATEMENT = AttributeKey.stringKey("db.statement");
  static final AttributeKey<String> DB_SYSTEM_NAME = AttributeKey.stringKey("db.system.name");
  static final AttributeKey<String> DB_SYSTEM = AttributeKey.stringKey("db.system");

  private final SpanExporter delegate;
  private final Mode mode;

  public DbStatementSpanExporter(SpanExporter delegate, Mode mode) {
    this.delegate = delegate;
    this.mode = mode;
  }

  @Override
  public CompletableResultCode export(Collection<SpanData> spans) {
    List<SpanData> out = null;
    int idx = 0;
    for (SpanData s : spans) {
      SpanData t = transform(s, mode);
      if (t != s && out == null) {
        out = new ArrayList<>(spans.size());
        int k = 0;
        for (SpanData prev : spans) {
          if (k++ >= idx) {
            break;
          }
          out.add(prev);
        }
      }
      if (out != null) {
        out.add(t);
      }
      idx++;
    }
    return delegate.export(out != null ? out : spans);
  }

  /** The span with its statement attributes rewritten, or the same instance when nothing changes. */
  public static SpanData transform(SpanData span, Mode mode) {
    Attributes a = span.getAttributes();
    String q = a.get(DB_QUERY_TEXT);
    String st = a.get(DB_STATEMENT);
    if (q == null && st == null) {
      return span;
    }
    AttributesBuilder b = a.toBuilder();
    if (mode == Mode.OFF) {
      b.remove(DB_QUERY_TEXT);
      b.remove(DB_STATEMENT);
    } else {
      String system = a.get(DB_SYSTEM_NAME);
      if (system == null) {
        system = a.get(DB_SYSTEM);
      }
      boolean changed = false;
      if (q != null) {
        String v = apply(q, system, mode);
        if (!v.equals(q)) {
          b.put(DB_QUERY_TEXT, v);
          changed = true;
        }
      }
      if (st != null) {
        String v = apply(st, system, mode);
        if (!v.equals(st)) {
          b.put(DB_STATEMENT, v);
          changed = true;
        }
      }
      if (!changed) {
        return span;
      }
    }
    final Attributes attrs = b.build();
    return new DelegatingSpanData(span) {
      @Override
      public Attributes getAttributes() {
        return attrs;
      }
    };
  }

  static String apply(String text, String system, Mode mode) {
    String v = text;
    if (mode == Mode.SANITIZED) {
      v = sanitize(text, system);
    }
    return SqlSanitizer.truncate(v, SqlSanitizer.MAX_QUERY_TEXT);
  }

  /** Key/value stores → CMD ? ?; document stores keep the upstream (already sanitized) form; everything else SQL. */
  public static String sanitize(String text, String system) {
    String sys = system == null ? "" : system.toLowerCase(Locale.ROOT);
    switch (sys) {
      case "redis":
      case "valkey":
      case "memcached":
        return SqlSanitizer.sanitizeKeyValue(text);
      case "mongodb":
      case "elasticsearch":
      case "opensearch":
        return text;
      default:
        return SqlSanitizer.sanitizeSql(text, sys);
    }
  }

  @Override
  public CompletableResultCode flush() {
    return delegate.flush();
  }

  @Override
  public CompletableResultCode shutdown() {
    return delegate.shutdown();
  }

  @Override
  public void close() {
    delegate.close();
  }

  @Override
  public String toString() {
    return "OpenlogDbStatementSpanExporter{" + mode + "," + delegate + "}";
  }
}

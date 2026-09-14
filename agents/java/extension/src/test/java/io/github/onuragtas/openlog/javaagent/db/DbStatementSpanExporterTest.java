package io.github.onuragtas.openlog.javaagent.db;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertSame;

import io.opentelemetry.api.common.Attributes;
import io.opentelemetry.api.trace.SpanKind;
import io.opentelemetry.sdk.testing.exporter.InMemorySpanExporter;
import io.opentelemetry.sdk.testing.trace.TestSpanData;
import io.opentelemetry.sdk.trace.data.SpanData;
import io.opentelemetry.sdk.trace.data.StatusData;
import java.util.List;
import org.junit.jupiter.api.Test;

class DbStatementSpanExporterTest {
  static SpanData span(Attributes attrs) {
    return TestSpanData.builder()
        .setName("db")
        .setKind(SpanKind.CLIENT)
        .setStartEpochNanos(1)
        .setEndEpochNanos(2)
        .setHasEnded(true)
        .setStatus(StatusData.unset())
        .setAttributes(attrs)
        .build();
  }

  @Test
  void sanitizesByDefaultLikeTheGoAgent() {
    InMemorySpanExporter mem = InMemorySpanExporter.create();
    DbStatementSpanExporter exp = new DbStatementSpanExporter(mem, DbStatementSpanExporter.Mode.SANITIZED);
    SpanData pg =
        span(Attributes.builder()
            .put("db.system.name", "postgresql")
            .put("db.query.text", "SELECT * FROM users WHERE email = 'a@b.c' AND id IN (1, 2)")
            .build());
    SpanData legacyMysql =
        span(Attributes.builder()
            .put("db.system", "mysql")
            .put("db.statement", "SELECT * FROM t WHERE name = \"bob\"")
            .build());
    SpanData redis =
        span(Attributes.builder().put("db.system.name", "redis").put("db.query.text", "SET session:1 secret").build());
    SpanData mongo =
        span(Attributes.builder().put("db.system.name", "mongodb").put("db.query.text", "{\"find\":\"?\"}").build());
    SpanData plain = span(Attributes.builder().put("http.route", "/x").build());
    exp.export(List.of(pg, legacyMysql, redis, mongo, plain));
    List<SpanData> out = mem.getFinishedSpanItems();
    assertEquals("SELECT * FROM users WHERE email = ? AND id IN (?)", out.get(0).getAttributes().get(DbStatementSpanExporter.DB_QUERY_TEXT));
    assertEquals("SELECT * FROM t WHERE name = ?", out.get(1).getAttributes().get(DbStatementSpanExporter.DB_STATEMENT));
    assertEquals("SET ? ?", out.get(2).getAttributes().get(DbStatementSpanExporter.DB_QUERY_TEXT));
    assertSame(mongo, out.get(3));
    assertSame(plain, out.get(4));
    assertEquals("postgresql", out.get(0).getAttributes().get(DbStatementSpanExporter.DB_SYSTEM_NAME));
  }

  @Test
  void rawAndOff() {
    SpanData pg =
        span(Attributes.builder().put("db.system.name", "postgresql").put("db.query.text", "SELECT 1").put("x", "y").build());
    assertSame(pg, DbStatementSpanExporter.transform(pg, DbStatementSpanExporter.Mode.RAW));
    SpanData off = DbStatementSpanExporter.transform(pg, DbStatementSpanExporter.Mode.OFF);
    assertNull(off.getAttributes().get(DbStatementSpanExporter.DB_QUERY_TEXT));
    assertEquals("y", off.getAttributes().get(io.opentelemetry.api.common.AttributeKey.stringKey("x")));
    String longSql = "SELECT " + "a,".repeat(3000) + "b FROM t";
    SpanData big = span(Attributes.builder().put("db.system.name", "postgresql").put("db.query.text", longSql).build());
    assertEquals(
        SqlSanitizer.MAX_QUERY_TEXT,
        DbStatementSpanExporter.transform(big, DbStatementSpanExporter.Mode.RAW).getAttributes().get(DbStatementSpanExporter.DB_QUERY_TEXT).length());
    assertEquals(DbStatementSpanExporter.Mode.OFF, DbStatementSpanExporter.Mode.parse(" OFF "));
    assertNull(DbStatementSpanExporter.Mode.parse("yes"));
  }
}

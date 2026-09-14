package io.github.onuragtas.openlog.javaagent.db;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import org.junit.jupiter.api.Test;

class SqlSanitizerTest {
  // Same cases as agents/go/openlogsql/sql_test.go TestSanitize and agents/node/test/unit/sanitize.test.ts:
  // every openlog agent must send identical db.query.text.
  static final String[][] GO_CASES = {
    {"SELECT * FROM users WHERE email = 'a@b.c' AND age > 30", "postgresql", "SELECT * FROM users WHERE email = ? AND age > ?"},
    {"select  *\n from t1 where col_2 = 3.14e-2 -- trailing comment", "postgresql", "select * from t1 where col_2 = ?"},
    {"/* app=shop */ UPDATE t SET a = 'it''s', b = E'x\\'y', c = 0xFF WHERE id IN (1,2, 3)", "postgresql", "UPDATE t SET a = ?, b = ?, c = ? WHERE id IN (?)"},
    {"INSERT INTO t (\"from\", note) VALUES (1, 'a'), (2, 'b'), (3, 'c')", "postgresql", "INSERT INTO t (\"from\", note) VALUES (?)"},
    {"SELECT * FROM t WHERE name = \"bob\" # mysql comment", "mysql", "SELECT * FROM t WHERE name = ?"},
    {"SELECT * FROM `order` WHERE id = ? AND x = :name AND y = @p1 AND z = $2", "mysql", "SELECT * FROM `order` WHERE id = ? AND x = :name AND y = @p1 AND z = $2"},
    {"SELECT a::text, $$secret$$ FROM t", "postgresql", "SELECT a::text, ? FROM t"},
    {"SELECT -5, x-1 FROM t", "postgresql", "SELECT -?, x-? FROM t"},
    {"SELECT 'unterminated", "postgresql", "SELECT ?"},
    {"SELECT 'ünïcödé' AS naïve FROM tåble", "postgresql", "SELECT ? AS naïve FROM tåble"},
  };

  @Test
  void matchesTheGoAgent() {
    for (String[] c : GO_CASES) {
      assertEquals(c[2], SqlSanitizer.sanitizeSql(c[0], c[1]), c[0]);
    }
  }

  @Test
  void extraCasesOfTheNodeAgent() {
    assertEquals(
        "SELECT * FROM \"users\" WHERE \"id\" = $1 AND flag = true",
        SqlSanitizer.sanitizeSql("SELECT * FROM \"users\" WHERE \"id\" = $1 AND flag = true", "postgresql"));
    assertEquals("SELECT ?, ?, ?", SqlSanitizer.sanitizeSql("SELECT x'DEADBEEF', b'0101', N'name'", "mssql"));
    assertEquals(
        "SELECT t1.c2 FROM t1 WHERE c3 IN ($1, $2)",
        SqlSanitizer.sanitizeSql("SELECT t1.c2 FROM t1 WHERE c3 IN ($1, $2)", "postgresql"));
    assertEquals("SELECT * FROM t WHERE id IN (?)", SqlSanitizer.sanitizeSql("SELECT * FROM t WHERE id IN (?, ?, ?)", "mysql"));
    assertEquals("INSERT INTO t VALUES (?)", SqlSanitizer.sanitizeSql("INSERT INTO t VALUES ('a\\'b', 2)", "mysql"));
    assertEquals(
        "SELECT [order] FROM [dbo].[t] WHERE a = ?",
        SqlSanitizer.sanitizeSql("SELECT [order] FROM [dbo].[t] WHERE a = 1.5", "mssql"));
    for (String[] c : GO_CASES) {
      String once = SqlSanitizer.sanitizeSql(c[0], c[1]);
      assertEquals(once, SqlSanitizer.sanitizeSql(once, c[1]), "idempotent: " + c[0]);
    }
  }

  @Test
  void keyValue() {
    assertEquals("GET ?", SqlSanitizer.sanitizeKeyValue("get products:42"));
    assertEquals("HSET ? ? ?", SqlSanitizer.sanitizeKeyValue("HSET k f  v"));
    assertEquals("PING", SqlSanitizer.sanitizeKeyValue("ping"));
    StringBuilder b = new StringBuilder("MSET");
    for (int i = 0; i < 100; i++) {
      b.append(" x");
    }
    String many = SqlSanitizer.sanitizeKeyValue(b.toString());
    assertEquals(1 + 32 + 1, many.split(" ").length);
    assertTrue(many.endsWith(" …"));
  }

  @Test
  void truncateKeepsSurrogatePairs() {
    assertEquals("abc", SqlSanitizer.truncate("abc", 10));
    String s = "a".repeat(SqlSanitizer.MAX_QUERY_TEXT - 1) + "😀";
    assertEquals(SqlSanitizer.MAX_QUERY_TEXT - 1, SqlSanitizer.truncate(s, SqlSanitizer.MAX_QUERY_TEXT).length());
  }
}

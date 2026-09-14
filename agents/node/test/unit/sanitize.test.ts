import assert from 'node:assert/strict';
import { test } from 'node:test';
import { MAX_QUERY_TEXT, sanitizeKeyValue, sanitizeSQL, truncateQueryText } from '../../src/sanitize';

// Same cases as agents/go/openlogsql/sql_test.go TestSanitize: both agents must send identical db.query.text.
const GO_CASES: [string, string, string][] = [
  ["SELECT * FROM users WHERE email = 'a@b.c' AND age > 30", 'postgresql', 'SELECT * FROM users WHERE email = ? AND age > ?'],
  ['select  *\n from t1 where col_2 = 3.14e-2 -- trailing comment', 'postgresql', 'select * from t1 where col_2 = ?'],
  ["/* app=shop */ UPDATE t SET a = 'it''s', b = E'x\\'y', c = 0xFF WHERE id IN (1,2, 3)", 'postgresql', 'UPDATE t SET a = ?, b = ?, c = ? WHERE id IN (?)'],
  [`INSERT INTO t ("from", note) VALUES (1, 'a'), (2, 'b'), (3, 'c')`, 'postgresql', `INSERT INTO t ("from", note) VALUES (?)`],
  [`SELECT * FROM t WHERE name = "bob" # mysql comment`, 'mysql', `SELECT * FROM t WHERE name = ?`],
  ['SELECT * FROM `order` WHERE id = ? AND x = :name AND y = @p1 AND z = $2', 'mysql', 'SELECT * FROM `order` WHERE id = ? AND x = :name AND y = @p1 AND z = $2'],
  ['SELECT a::text, $$secret$$ FROM t', 'postgresql', 'SELECT a::text, ? FROM t'],
  ['SELECT -5, x-1 FROM t', 'postgresql', 'SELECT -?, x-? FROM t'],
  ["SELECT 'unterminated", 'postgresql', 'SELECT ?'],
  ["SELECT 'ünïcödé' AS naïve FROM tåble", 'postgresql', 'SELECT ? AS naïve FROM tåble'],
];

test('sanitizeSQL matches the Go agent', () => {
  for (const [input, system, want] of GO_CASES) {
    assert.equal(sanitizeSQL(input, system), want, `sanitizeSQL(${JSON.stringify(input)})`);
  }
});

test('sanitizeSQL extra cases', () => {
  assert.equal(sanitizeSQL('SELECT * FROM "users" WHERE "id" = $1 AND flag = true', 'postgresql'), 'SELECT * FROM "users" WHERE "id" = $1 AND flag = true');
  assert.equal(sanitizeSQL("SELECT x'DEADBEEF', b'0101', N'name'", 'mssql'), 'SELECT ?, ?, ?');
  assert.equal(sanitizeSQL('SELECT t1.c2 FROM t1 WHERE c3 IN ($1, $2)', 'postgresql'), 'SELECT t1.c2 FROM t1 WHERE c3 IN ($1, $2)');
  assert.equal(sanitizeSQL('SELECT * FROM t WHERE id IN (?, ?, ?)', 'mysql'), 'SELECT * FROM t WHERE id IN (?)');
  assert.equal(sanitizeSQL("INSERT INTO t VALUES ('a\\'b', 2)", 'mysql'), 'INSERT INTO t VALUES (?)');
  assert.equal(sanitizeSQL('SELECT [order] FROM [dbo].[t] WHERE a = 1.5', 'mssql'), 'SELECT [order] FROM [dbo].[t] WHERE a = ?');
  // idempotent
  for (const [input, system] of GO_CASES) {
    const once = sanitizeSQL(input, system);
    assert.equal(sanitizeSQL(once, system), once);
  }
});

test('sanitizeKeyValue', () => {
  assert.equal(sanitizeKeyValue('get', ['products:42']), 'GET ?');
  assert.equal(sanitizeKeyValue('HSET', ['k', 'f', 'v']), 'HSET ? ? ?');
  assert.equal(sanitizeKeyValue('ping'), 'PING');
  const many = sanitizeKeyValue('MSET', new Array(100).fill('x'));
  assert.equal(many.split(' ').length, 1 + 32 + 1);
  assert.ok(many.endsWith(' …'));
});

test('truncateQueryText keeps surrogate pairs intact', () => {
  assert.equal(truncateQueryText('abc', 10), 'abc');
  const s = 'a'.repeat(MAX_QUERY_TEXT - 1) + '😀';
  const t = truncateQueryText(s);
  assert.equal(t.length, MAX_QUERY_TEXT - 1);
});

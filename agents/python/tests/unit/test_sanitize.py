from openlog_agent.sanitize import MAX_QUERY_TEXT_BYTES, sanitize_key_value, sanitize_sql, truncate_query_text

# Same cases as agents/go/openlogsql/sql_test.go TestSanitize and agents/node/test/unit/sanitize.test.ts:
# every openlog agent must send identical db.query.text.
GO_CASES = [
    (
        "SELECT * FROM users WHERE email = 'a@b.c' AND age > 30",
        "postgresql",
        "SELECT * FROM users WHERE email = ? AND age > ?",
    ),
    ("select  *\n from t1 where col_2 = 3.14e-2 -- trailing comment", "postgresql", "select * from t1 where col_2 = ?"),
    (
        "/* app=shop */ UPDATE t SET a = 'it''s', b = E'x\\'y', c = 0xFF WHERE id IN (1,2, 3)",
        "postgresql",
        "UPDATE t SET a = ?, b = ?, c = ? WHERE id IN (?)",
    ),
    (
        "INSERT INTO t (\"from\", note) VALUES (1, 'a'), (2, 'b'), (3, 'c')",
        "postgresql",
        'INSERT INTO t ("from", note) VALUES (?)',
    ),
    ('SELECT * FROM t WHERE name = "bob" # mysql comment', "mysql", "SELECT * FROM t WHERE name = ?"),
    (
        "SELECT * FROM `order` WHERE id = ? AND x = :name AND y = @p1 AND z = $2",
        "mysql",
        "SELECT * FROM `order` WHERE id = ? AND x = :name AND y = @p1 AND z = $2",
    ),
    ("SELECT a::text, $$secret$$ FROM t", "postgresql", "SELECT a::text, ? FROM t"),
    ("SELECT -5, x-1 FROM t", "postgresql", "SELECT -?, x-? FROM t"),
    ("SELECT 'unterminated", "postgresql", "SELECT ?"),
    ("SELECT 'ünïcödé' AS naïve FROM tåble", "postgresql", "SELECT ? AS naïve FROM tåble"),
]

# Node.js agent extra cases (sanitize.test.ts).
NODE_CASES = [
    (
        'SELECT * FROM "users" WHERE "id" = $1 AND flag = true',
        "postgresql",
        'SELECT * FROM "users" WHERE "id" = $1 AND flag = true',
    ),
    ("SELECT x'DEADBEEF', b'0101', N'name'", "mssql", "SELECT ?, ?, ?"),
    ("SELECT t1.c2 FROM t1 WHERE c3 IN ($1, $2)", "postgresql", "SELECT t1.c2 FROM t1 WHERE c3 IN ($1, $2)"),
    ("SELECT * FROM t WHERE id IN (?, ?, ?)", "mysql", "SELECT * FROM t WHERE id IN (?)"),
    ("INSERT INTO t VALUES ('a\\'b', 2)", "mysql", "INSERT INTO t VALUES (?)"),
    ("SELECT [order] FROM [dbo].[t] WHERE a = 1.5", "mssql", "SELECT [order] FROM [dbo].[t] WHERE a = ?"),
]

# Python DB-API placeholders.
PYTHON_CASES = [
    ("SELECT * FROM t WHERE a = %s AND b = %(name)s", "postgresql", "SELECT * FROM t WHERE a = %s AND b = %(name)s"),
    ("INSERT INTO t (a, b) VALUES (%s, %s)", "mysql", "INSERT INTO t (a, b) VALUES (%s, %s)"),
    (
        "SELECT users.id FROM users WHERE users.id = %(id_1)s LIMIT %(param_1)s",
        "postgresql",
        "SELECT users.id FROM users WHERE users.id = %(id_1)s LIMIT %(param_1)s",
    ),
]


def test_matches_go_agent():
    for q, system, want in GO_CASES:
        assert sanitize_sql(q, system) == want, q


def test_matches_node_agent_extra_cases():
    for q, system, want in NODE_CASES:
        assert sanitize_sql(q, system) == want, q


def test_python_placeholders_kept():
    for q, system, want in PYTHON_CASES:
        assert sanitize_sql(q, system) == want, q


def test_idempotent():
    for q, system, _ in GO_CASES + NODE_CASES + PYTHON_CASES:
        once = sanitize_sql(q, system)
        assert sanitize_sql(once, system) == once


def test_mariadb_like_mysql():
    assert sanitize_sql('SELECT "x" # c', "mariadb") == "SELECT ?"
    assert sanitize_sql('SELECT "x"', "postgresql") == 'SELECT "x"'


def test_sanitize_key_value():
    assert sanitize_key_value("get", ["products:42"]) == "GET ?"
    assert sanitize_key_value("HSET", ["k", "f", "v"]) == "HSET ? ? ?"
    assert sanitize_key_value("ping") == "PING"
    many = sanitize_key_value("MSET", ["x"] * 100)
    assert len(many.split(" ")) == 1 + 32 + 1
    assert many.endswith(" …")


def test_truncate_utf8_safe():
    assert truncate_query_text("abc", 10) == "abc"
    s = "a" * (MAX_QUERY_TEXT_BYTES - 1) + "é"
    t = truncate_query_text(s)
    assert t == "a" * (MAX_QUERY_TEXT_BYTES - 1)
    assert len(t.encode()) <= MAX_QUERY_TEXT_BYTES
    assert len(truncate_query_text("ü" * 5000).encode()) == MAX_QUERY_TEXT_BYTES

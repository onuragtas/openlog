--TEST--
db.query.text: sanitized (default), raw, off
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; if (!extension_loaded('pdo_sqlite')) die('skip pdo_sqlite'); ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$queries = [
    "SELECT * FROM t WHERE a = 'it''s' AND b = \"ident\" AND c = 42 AND d = -3.5e2 AND e = 0x1F",
    "SELECT \$1, :name, ?, @p1, col2 FROM tab_9 -- trailing 7",
    "SELECT \$\$dollar 'q' 1\$\$, \$tag\$ x \$tag\$ FROM t",
    "INSERT INTO t VALUES (1, 'a'), (2, 'b')",
    "UPDATE t SET name = E'esc\\'aped', n = N'x' WHERE id=5 /* c 9 */",
    "SELECT   a,\n\t b\r\nFROM   t",
];
$code = '<?php $db = new PDO("sqlite::memory:", null, null, [PDO::ATTR_ERRMODE => PDO::ERRMODE_SILENT]);' . "\n";
foreach ($queries as $q) {
    $code .= '@$db->query(' . var_export($q, true) . ");\n";
}
foreach (['sanitized', 'raw', 'off'] as $mode) {
    echo "== $mode\n";
    $t = ol_one_trace(ol_run($code, ['ini' => ['openlog.capture_query_text' => $mode]]));
    foreach ($t['spans'] as $s) {
        if ($s['kind'] !== 3) continue;
        echo $s['name'], ' | ', array_key_exists('db.query.text', $s['attrs']) ? json_encode($s['attrs']['db.query.text'], JSON_UNESCAPED_SLASHES) : '(none)', "\n";
    }
}
--EXPECT--
== sanitized
SELECT t | "SELECT * FROM t WHERE a = ? AND b = \"ident\" AND c = ? AND d = -? AND e = ?"
SELECT tab_9 | "SELECT $1, :name, ?, @p1, col2 FROM tab_9"
SELECT t | "SELECT ?, ? FROM t"
INSERT t | "INSERT INTO t VALUES (?, ?), (?, ?)"
UPDATE t | "UPDATE t SET name = ?, n = ? WHERE id=?"
SELECT t | "SELECT a, b FROM t"
== raw
SELECT t | "SELECT * FROM t WHERE a = 'it''s' AND b = \"ident\" AND c = 42 AND d = -3.5e2 AND e = 0x1F"
SELECT tab_9 | "SELECT $1, :name, ?, @p1, col2 FROM tab_9 -- trailing 7"
SELECT t | "SELECT $$dollar 'q' 1$$, $tag$ x $tag$ FROM t"
INSERT t | "INSERT INTO t VALUES (1, 'a'), (2, 'b')"
UPDATE t | "UPDATE t SET name = E'esc\\'aped', n = N'x' WHERE id=5 /* c 9 */"
SELECT t | "SELECT   a,\n\t b\r\nFROM   t"
== off
SELECT t | (none)
SELECT tab_9 | (none)
SELECT t | (none)
INSERT t | (none)
UPDATE t | (none)
SELECT t | (none)

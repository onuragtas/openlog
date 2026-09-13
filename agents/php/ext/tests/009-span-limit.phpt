--TEST--
Span limit: more than 4096 spans per request are dropped and counted
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; if (!extension_loaded('pdo_sqlite')) die('skip pdo_sqlite'); ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$r = ol_run('<?php $db = new PDO("sqlite::memory:"); for ($i = 0; $i < 5000; $i++) { $db->exec("SELECT 1"); } echo "ok\n";');
echo $r['out'];
$t = ol_one_trace($r);
echo "spans=", count($t['spans']), " dropped=", $t['dropped_spans'], "\n";
--EXPECT--
ok
spans=4096 dropped=905

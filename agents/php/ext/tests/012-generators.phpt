--TEST--
Generators: every resume is balanced; exceptions thrown into and out of generators
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; if (!extension_loaded('pdo_sqlite')) die('skip pdo_sqlite'); ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$r = ol_run('<?php
$db = new PDO("sqlite::memory:");
function rows($db) {
    for ($i = 0; $i < 3; $i++) {
        usleep(180000);
        $db->exec("SELECT $i");
        yield $i;
    }
}
function consume($v) { usleep(2000); return $v; }
foreach (rows($db) as $v) { consume($v); }
function failing() { yield 1; throw new RuntimeException("gen"); }
try { foreach (failing() as $v) {} } catch (RuntimeException $e) { echo "caught\n"; }
$g = rows($db);
$g->current();
try { $g->throw(new LogicException("in")); } catch (LogicException $e) { echo "thrown in\n"; }
$db->exec("SELECT 99");
');
echo $r['out'];
$t = ol_one_trace($r);
$root = ol_root($t);
$byId = [];
foreach ($t['spans'] as $s) $byId[$s['id']] = $s;
echo "rows segments: ", count(ol_find($t, 'rows')) >= 3 ? '>=3' : 'fewer', "\n";
echo "consume segments: ", count(ol_find($t, 'consume')), "\n";
$q = ol_find($t, 'SELECT');
echo "queries: ", count($q), "\n";
foreach ($q as $s) {
    $p = isset($byId[$s['parent']]) ? $byId[$s['parent']]['name'] : '?';
    echo "  parent ", $p === $root['name'] ? 'root' : $p, "\n";
}
--EXPECT--
caught
thrown in
rows segments: >=3
consume segments: 3
queries: 5
  parent rows
  parent rows
  parent rows
  parent rows
  parent root

--TEST--
Fibers (PHP >= 8.1): spans inside fibers, suspend/resume, no crash, tracer frames stay balanced
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; if (PHP_VERSION_ID < 80100) die('skip PHP >= 8.1'); if (!extension_loaded('pdo_sqlite')) die('skip pdo_sqlite'); ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$r = ol_run('<?php
$db = new PDO("sqlite::memory:");
function inner($db, $n) { $db->exec("SELECT $n"); return Fiber::suspend($n); }
$fibers = [];
for ($i = 1; $i <= 3; $i++) {
    $fibers[$i] = new Fiber(function () use ($db, $i) {
        usleep(100000);
        $x = inner($db, $i);
        $db->exec("SELECT \'after$i\'");
        return $x;
    });
    $fibers[$i]->start();
}
$db->exec("SELECT \'main\'");
foreach ($fibers as $i => $f) { $f->resume($i * 10); echo $f->getReturn(), "\n"; }
$dead = new Fiber(function () { Fiber::suspend(); });
$dead->start();
unset($dead);
$db->exec("SELECT \'end\'");
usleep(300000);
');
echo $r['out'];
$t = ol_one_trace($r);
$root = ol_root($t);
$ids = [];
foreach ($t['spans'] as $s) $ids[$s['id']] = $s;
$names = [];
foreach ($t['spans'] as $s) if ($s['kind'] === 3) $names[] = $s['name'];
echo count($names), " client spans\n";
$ok = true;
foreach ($t['spans'] as $s) if ($s !== $root && !isset($ids[$s['parent']])) $ok = false;
echo "all parents present: ", $ok ? 'yes' : 'no', "\n";
$end = ol_find($t, 'SELECT');
echo "function_trace=", var_export($t['function_trace'], true), "\n";
--EXPECT--
10
20
30
8 client spans
all parents present: yes
function_trace=true

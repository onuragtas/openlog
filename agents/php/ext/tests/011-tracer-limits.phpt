--TEST--
Transaction tracer limits: max_segments, max_memory_kb, recursion deeper than the frame stack
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; if (!extension_loaded('pdo_sqlite')) die('skip pdo_sqlite'); ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
function segments($t) {
    $n = 0;
    foreach ($t['spans'] as $s) if (isset($s['attrs']['openlog.php.segment'])) $n++;
    return $n;
}
$slow = '<?php function work($i) { usleep(2000); } for ($i = 0; $i < 40; $i++) { work($i); } usleep(520000);';

$r = ol_run($slow, ['ini' => ['openlog.transaction_tracer.max_segments' => '10']]);
$t = ol_one_trace($r);
echo "max_segments=10: function segments<=10: ", segments($t) <= 10 ? 'yes' : 'no', " dropped>0: ", $t['dropped_spans'] > 0 ? 'yes' : 'no', "\n";

$r = ol_run($slow, ['ini' => ['openlog.transaction_tracer.max_memory_kb' => '2']]);
$t = ol_one_trace($r);
echo "max_memory_kb=2: segments<40: ", segments($t) < 40 ? 'yes' : 'no', " dropped>0: ", $t['dropped_spans'] > 0 ? 'yes' : 'no', "\n";

// recursion deeper than the 2048-frame stack, with a query at the bottom and after unwinding
$r = ol_run('<?php
$db = new PDO("sqlite::memory:");
function down($n, $db) { if ($n === 0) { $db->exec("SELECT 1"); usleep(510000); return 0; } return 1 + down($n - 1, $db); }
echo down(3000, $db), "\n";
$db->exec("SELECT 2");
', ['ini' => ['openlog.transaction_tracer.min_segment_ms' => '0', 'openlog.transaction_tracer.max_segments' => '100000', 'openlog.transaction_tracer.max_memory_kb' => '65536']]);
echo $r['out'];
$t = ol_one_trace($r);
$root = ol_root($t);
$last = ol_find($t, 'SELECT');
$after = end($last);
echo "recursion: spans>2000: ", count($t['spans']) > 2000 ? 'yes' : 'no', " last query parent is root: ", $after['parent'] === $root['id'] ? 'yes' : 'no', "\n";
--EXPECT--
max_segments=10: function segments<=10: yes dropped>0: yes
max_memory_kb=2: segments<40: yes dropped>0: yes
3000
recursion: spans>2000: yes last query parent is root: yes

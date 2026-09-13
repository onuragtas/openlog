--TEST--
Transaction tracer limits: max_segments, max_memory_kb, recursion deeper than the sampled path
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
// every recursion level is its own frame (and segment); usleep takes a sample in each
$slow = '<?php function level($n) { usleep(2000); if ($n > 0) { level($n - 1); } } level(40); usleep(520000);';

$r = ol_run($slow, ['ini' => ['openlog.transaction_tracer.max_segments' => '10']]);
$t = ol_one_trace($r);
echo "max_segments=10: function segments<=10: ", segments($t) <= 10 ? 'yes' : 'no (' . segments($t) . ')', " dropped>0: ", $t['dropped_spans'] > 0 ? 'yes' : 'no', "\n";

$r = ol_run($slow, ['ini' => ['openlog.transaction_tracer.max_memory_kb' => '2']]);
$t = ol_one_trace($r);
echo "max_memory_kb=2: segments<41: ", segments($t) < 41 ? 'yes' : 'no', " dropped>0: ", $t['dropped_spans'] > 0 ? 'yes' : 'no', "\n";

$r = ol_run($slow);
$t = ol_one_trace($r);
echo "defaults: 41 level segments: ", count(ol_find($t, 'level')) === 41 ? 'yes' : 'no (' . count(ol_find($t, 'level')) . ')', " dropped=", $t['dropped_spans'], "\n";

// recursion deeper than the sampled path (256 frames), with a query at the bottom and after unwinding
$r = ol_run('<?php
$db = new PDO("sqlite::memory:");
function down($n, $db) { if ($n === 0) { $db->exec("SELECT 1"); usleep(510000); return 0; } return 1 + down($n - 1, $db); }
echo down(3000, $db), "\n";
$db->exec("SELECT 2");
', ['ini' => ['openlog.transaction_tracer.max_segments' => '100000', 'openlog.transaction_tracer.max_memory_kb' => '65536']]);
echo $r['out'];
$t = ol_one_trace($r);
$root = ol_root($t);
$last = ol_find($t, 'SELECT');
$after = end($last);
echo "recursion: segments<=256: ", segments($t) <= 256 ? 'yes' : 'no (' . segments($t) . ')', " segments>=200: ", segments($t) >= 200 ? 'yes' : 'no',
    " last query parent is root: ", $after['parent'] === $root['id'] ? 'yes' : 'no', "\n";
--EXPECT--
max_segments=10: function segments<=10: yes dropped>0: yes
max_memory_kb=2: segments<41: yes dropped>0: yes
defaults: 41 level segments: yes dropped=0
3000
recursion: segments<=256: yes segments>=200: yes last query parent is root: yes

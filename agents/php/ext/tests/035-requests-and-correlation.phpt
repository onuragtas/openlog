--TEST--
Several requests in one process (php-cgi -T): independent traces, no state leak; log correlation functions
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; if (!is_executable(dirname(PHP_BINARY) . '/php-cgi')) die('skip no php-cgi'); if (!extension_loaded('pdo_sqlite')) die('skip pdo_sqlite'); ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$counter = ol_tmp('.cnt');
$r = ol_run('<?php
$f = ' . var_export($counter, true) . ';
$n = (int) @file_get_contents($f);
file_put_contents($f, $n + 1);
if ($n === 0) { \openlog\set_transaction_name("first"); }
if ($n === 1) { http_response_code(500); }
$db = new PDO("sqlite::memory:");
$db->exec("SELECT $n");
function log_line() { return \openlog\trace_id() . " " . \openlog\span_id(); }
echo log_line(), "\n";
', ['cgi' => true, 'repeat' => 3, 'server' => ['REQUEST_URI' => '/items/9']]);
@unlink($counter);
$traces = ol_traces($r['msgs']);
echo count($traces), " traces\n";
foreach ($r['invalid'] as $p) echo "INVALID $p\n";
$lines = array_values(array_filter(explode("\n", $r['out'])));
$i = 0;
foreach ($traces as $id => $t) {
    $root = ol_root($t);
    list($tid, $sid) = explode(' ', $lines[$i++]);
    echo $root['name'], " status=", $root['status'], " spans=", count($t['spans']),
        " log trace_id matches=", $tid === $id ? 'yes' : 'no', " span_id is root=", $sid === $root['id'] ? 'yes' : 'no', "\n";
}
--EXPECT--
3 traces
GET first status=0 spans=2 log trace_id matches=yes span_id is root=yes
GET /items/{id} status=2 spans=2 log trace_id matches=yes span_id is root=yes
GET /items/{id} status=0 spans=2 log trace_id matches=yes span_id is root=yes

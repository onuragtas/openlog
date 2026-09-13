--TEST--
Fail-open: missing socket, invalid transport and a receiver that never reads do not affect the application
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; if (!is_executable(dirname(PHP_BINARY) . '/php-cgi')) die('skip no php-cgi'); ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$code = '<?php $s = \openlog\stats(); echo "req=", $s["requests"], " msgs=", $s["messages"], " send_errors=", $s["send_errors"], " dropped=", $s["dropped_messages"], "\n";';

// no socket at the path
$r = ol_run($code, ['cgi' => true, 'repeat' => 3, 'socket' => false]);
echo "missing socket (exit ", $r['exit'], "):\n", $r['out'];

// invalid transport: logged once, rate limited
$r = ol_run('<?php echo "still running\n";', ['ini' => ['openlog.transport' => 'bogus://x', 'log_errors' => '1', 'error_log' => '']]);
echo $r['out'], "log lines: ", substr_count($r['err'], 'openlog: invalid openlog.transport'), "\n";

// a path that is a directory, not a socket
$r = ol_run('<?php echo "dir ok\n";', ['ini' => ['openlog.transport' => 'unix://' . sys_get_temp_dir()], 'socket' => false]);
echo $r['out'];

// receiver exists but never reads: many large requests must not block (bounded retry only)
$sock = ol_tmp('.sock');
$blackhole = stream_socket_server('udg://' . $sock, $en, $es, STREAM_SERVER_BIND);
$start = microtime(true);
$r = ol_run('<?php $db = new PDO("sqlite::memory:"); for ($i = 0; $i < 300; $i++) { $db->query("SELECT \'" . str_repeat("x", 400) . "\'"); }',
    ['cgi' => true, 'repeat' => 20, 'socket' => false, 'ini' => ['openlog.transport' => 'unix://' . $sock]]);
$elapsed = microtime(true) - $start;
echo "blackhole: exit=", $r['exit'], " fast=", $elapsed < 20 ? "yes" : "no ($elapsed s)", "\n";
fclose($blackhole);
@unlink($sock);
--EXPECT--
missing socket (exit 0):
req=1 msgs=0 send_errors=0 dropped=0
req=2 msgs=1 send_errors=1 dropped=1
req=3 msgs=2 send_errors=2 dropped=2
still running
log lines: 1
dir ok
blackhole: exit=0 fast=yes

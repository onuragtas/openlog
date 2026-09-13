--TEST--
Errors: uncaught exception (CLI and web), exception handler, fatal error, long messages, exit()
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; if (!is_executable(dirname(PHP_BINARY) . '/php-cgi')) die('skip no php-cgi'); ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
function show($label, $r) {
    $t = ol_one_trace($r);
    if (!$t) return;
    $root = ol_root($t);
    echo "$label: status=", $root['status'], " msg=", substr(isset($root['status_msg']) ? $root['status_msg'] : '', 0, 40),
        " code=", isset($root['attrs']['http.response.status_code']) ? $root['attrs']['http.response.status_code'] : '-', "\n";
    foreach (isset($root['events']) ? $root['events'] : [] as $e) {
        $st = $e['attrs']['exception.stacktrace'];
        echo "  ", $e['name'], " ", $e['attrs']['exception.type'], " msglen=", strlen($e['attrs']['exception.message']),
            " stack#0=", preg_match('/^#0 \S+\(\d+\): /m', $st) ? 'yes' : 'no', " len<=4096=", strlen($st) <= 4096 ? 'yes' : 'no', "\n";
    }
}
$uncaught = '<?php
class Repo { function find($id) { throw new DomainException("not found " . str_repeat("x", 6000) . "\xff\xfe"); } }
function controller() { (new Repo)->find(7); }
controller();
';
show('cli uncaught', ol_run($uncaught));
show('web uncaught', ol_run($uncaught, ['cgi' => true, 'ini' => ['display_errors' => '0']]));
show('exception handler', ol_run('<?php set_exception_handler(function ($e) { echo "handled\n"; }); throw new RuntimeException("to handler");'));
show('memory fatal', ol_run('<?php ini_set("memory_limit", "16M"); $a = []; while (true) { $a[] = str_repeat("x", 1048576); }'));
show('caught only', ol_run('<?php try { throw new Exception("quiet"); } catch (Exception $e) {} echo "fine";'));
show('exit in function', ol_run('<?php function stop() { exit(3); } stop();'));
$r = ol_run('<?php function stop() { exit(3); } stop();');
$root = ol_root(ol_one_trace($r));
echo "exit code attr: ", isset($root['attrs']['process.exit.code']) ? $root['attrs']['process.exit.code'] : '-', "\n";
--EXPECTF--
cli uncaught: status=2 msg=not found xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx code=-
  exception DomainException msglen=4096 stack#0=yes len<=4096=yes
web uncaught: status=2 msg=not found xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx code=500
  exception DomainException msglen=4096 stack#0=yes len<=4096=yes
exception handler: status=2 msg=to handler code=-
  exception RuntimeException msglen=10 stack#0=no len<=4096=yes
memory fatal: status=2 msg=Allowed memory size of 16777216 bytes ex code=-
  exception E_ERROR msglen=%d stack#0=yes len<=4096=yes
caught only: status=0 msg= code=-
exit in function: status=0 msg= code=-
exit code attr: 3

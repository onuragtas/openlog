--TEST--
Lean mode (openlog.userland_hooks=0): internal instrumentation only, no framework hooks, uncaught exceptions still fail the transaction
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; if (!is_executable(dirname(PHP_BINARY) . '/php-cgi')) die('skip no php-cgi'); if (!extension_loaded('pdo_sqlite')) die('skip pdo_sqlite'); ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$code = '<?php
namespace Illuminate\Routing {
    class Route { public $uri = "members/{member}"; }
    class Router { public function runRoute($request, $route) { return 1; } }
}
namespace {
    (new Illuminate\Routing\Router())->runRoute(null, new Illuminate\Routing\Route());
    $pdo = new PDO("sqlite::memory:");
    $pdo->exec("CREATE TABLE t (id INTEGER)");
    $st = $pdo->prepare("SELECT id FROM t WHERE id = ?");
    for ($i = 0; $i < 3; $i++) { $st->execute([$i]); }
    echo "ini=", ini_get("openlog.userland_hooks"), " sampled=", var_export(openlog\is_sampled(), true), "\n";
    if (getenv("BOOM")) { throw new RuntimeException("boom"); }
}';
foreach (['1', '0'] as $hooks) {
    $r = ol_run($code, ['cgi' => true, 'server' => ['REQUEST_URI' => '/u/42/x'], 'ini' => ['openlog.userland_hooks' => $hooks]]);
    echo trim($r['out']), "\n";
    ol_print_tree(ol_one_trace($r), ['db.query.text']);
}
$r = ol_run($code, ['env' => ['BOOM' => '1'], 'ini' => ['openlog.userland_hooks' => '0', 'display_errors' => '0']]);
$root = ol_root(ol_one_trace($r));
$e = isset($root['events'][0]) ? $root['events'][0]['attrs'] : [];
echo "uncaught (CLI, lean): status=", $root['status'], " message has class: ",
    isset($e['exception.message']) && strpos($e['exception.message'], 'RuntimeException: boom') !== false ? 'yes' : 'no', "\n";
--EXPECT--
ini=1 sampled=true
GET /members/{member} kind=2 status=0
  CREATE kind=3 status=0 db.query.text="CREATE TABLE t (id INTEGER)"
  SELECT t kind=3 status=0 db.query.text="SELECT id FROM t WHERE id = ?"
  SELECT t kind=3 status=0 db.query.text="SELECT id FROM t WHERE id = ?"
  SELECT t kind=3 status=0 db.query.text="SELECT id FROM t WHERE id = ?"
ini=0 sampled=true
GET /u/{id}/x kind=2 status=0
  CREATE kind=3 status=0 db.query.text="CREATE TABLE t (id INTEGER)"
  SELECT t kind=3 status=0 db.query.text="SELECT id FROM t WHERE id = ?"
  SELECT t kind=3 status=0 db.query.text="SELECT id FROM t WHERE id = ?"
  SELECT t kind=3 status=0 db.query.text="SELECT id FROM t WHERE id = ?"
uncaught (CLI, lean): status=2 message has class: yes

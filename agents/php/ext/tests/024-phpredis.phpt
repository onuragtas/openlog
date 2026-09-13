--TEST--
phpredis: commands as client spans, connection attributes, SELECT, excluded methods, raw mode
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; if (!extension_loaded('redis')) die('skip redis'); if (!getenv('OPENLOG_TEST_REDIS')) die('skip OPENLOG_TEST_REDIS not set'); ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$code = '<?php
$r = new Redis();
$r->connect(getenv("OPENLOG_TEST_REDIS"), 6379, 1.0);
$r->getOption(Redis::OPT_PREFIX);
$r->set("openlog:key", "value");
var_dump($r->get("openlog:missing"));
$r->select(1);
$r->incr("openlog:counter");
';
foreach (['sanitized', 'raw'] as $mode) {
    $res = ol_run($code, ['ini' => ['openlog.capture_query_text' => $mode]]);
    echo "== $mode\n", $res['out'];
    $t = ol_one_trace($res);
    ob_start();
    ol_print_tree($t, ['db.system.name', 'server.address', 'server.port', 'db.namespace', 'db.operation.name', 'db.query.text'], false);
    echo str_replace(getenv('OPENLOG_TEST_REDIS'), 'HOST', ob_get_clean());
}
--EXPECTF--
== sanitized
bool(false)
php %s kind=1 status=0
  SET kind=3 status=0 db.system.name="redis" server.address="HOST" server.port=6379 db.operation.name="SET" db.query.text="SET ? ?"
  GET kind=3 status=0 db.system.name="redis" server.address="HOST" server.port=6379 db.operation.name="GET" db.query.text="GET ?"
  SELECT kind=3 status=0 db.system.name="redis" server.address="HOST" server.port=6379 db.operation.name="SELECT" db.query.text="SELECT ?"
  INCR kind=3 status=0 db.system.name="redis" server.address="HOST" server.port=6379 db.namespace="1" db.operation.name="INCR" db.query.text="INCR ?"
== raw
bool(false)
php %s kind=1 status=0
  SET kind=3 status=0 db.system.name="redis" server.address="HOST" server.port=6379 db.operation.name="SET" db.query.text="SET openlog:key value"
  GET kind=3 status=0 db.system.name="redis" server.address="HOST" server.port=6379 db.operation.name="GET" db.query.text="GET openlog:missing"
  SELECT kind=3 status=0 db.system.name="redis" server.address="HOST" server.port=6379 db.operation.name="SELECT" db.query.text="SELECT 1"
  INCR kind=3 status=0 db.system.name="redis" server.address="HOST" server.port=6379 db.namespace="1" db.operation.name="INCR" db.query.text="INCR openlog:counter"

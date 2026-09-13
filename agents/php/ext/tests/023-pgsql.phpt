--TEST--
pgsql functions and PDO pgsql: queries, parameters, prepared statements, default connection, failures
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; if (!extension_loaded('pgsql') || !extension_loaded('pdo_pgsql')) die('skip pgsql'); if (!getenv('OPENLOG_TEST_PGSQL')) die('skip OPENLOG_TEST_PGSQL not set'); ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$r = ol_run('<?php
$h = getenv("OPENLOG_TEST_PGSQL");
$c = pg_connect("host=$h port=5432 dbname=test user=test password=test");
pg_query($c, "SELECT 1");
@pg_query("SELECT \'default connection\'");
pg_query_params($c, "SELECT \$1::int + 1", [5]);
pg_prepare($c, "s1", "SELECT \$1::text");
pg_execute($c, "s1", ["x"]);
@pg_query($c, "SELECT * FROM missing_table");
$pdo = new PDO("pgsql:host=$h;port=5432;dbname=test", "test", "test");
$pdo->query("SELECT 2")->fetchAll();
echo "done\n";
');
echo $r['out'];
$t = ol_one_trace($r);
ob_start();
ol_print_tree($t, ['db.system.name', 'server.address', 'server.port', 'db.namespace', 'db.query.text', 'error.type'], false);
echo str_replace(getenv('OPENLOG_TEST_PGSQL'), 'HOST', ob_get_clean());
--EXPECTF--
done
php %s kind=1 status=0
  SELECT kind=3 status=0 db.system.name="postgresql" server.address="HOST" server.port=5432 db.namespace="test" db.query.text="SELECT ?"
  SELECT kind=3 status=0 db.system.name="postgresql" server.address="HOST" server.port=5432 db.namespace="test" db.query.text="SELECT ?"
  SELECT kind=3 status=0 db.system.name="postgresql" server.address="HOST" server.port=5432 db.namespace="test" db.query.text="SELECT $1::int + ?"
  SELECT kind=3 status=0 db.system.name="postgresql" server.address="HOST" server.port=5432 db.namespace="test" db.query.text="SELECT $1::text"
  SELECT missing_table kind=3 status=2 db.system.name="postgresql" server.address="HOST" server.port=5432 db.namespace="test" db.query.text="SELECT * FROM missing_table" error.type="_OTHER"
  SELECT kind=3 status=0 db.system.name="postgresql" server.address="HOST" server.port=5432 db.namespace="test" db.query.text="SELECT ?"

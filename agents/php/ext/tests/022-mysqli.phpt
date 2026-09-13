--TEST--
mysqli: procedural and OO queries, prepared statements, connection attributes, failures
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; if (!extension_loaded('mysqli')) die('skip mysqli'); if (!getenv('OPENLOG_TEST_MYSQL')) die('skip OPENLOG_TEST_MYSQL not set'); ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$r = ol_run('<?php
mysqli_report(MYSQLI_REPORT_OFF);
$h = getenv("OPENLOG_TEST_MYSQL");
$link = mysqli_connect($h, "test", "test", "test", 3306);
mysqli_query($link, "CREATE TEMPORARY TABLE t (id INT, name VARCHAR(20))");
mysqli_query($link, "INSERT INTO t VALUES (1, \'apple\')");
mysqli_real_query($link, "SELECT name FROM t WHERE id = 1");
mysqli_free_result(mysqli_store_result($link));
$st = mysqli_prepare($link, "SELECT name FROM t WHERE id = ?");
$id = 1;
mysqli_stmt_bind_param($st, "i", $id);
mysqli_stmt_execute($st);
$m = new mysqli($h, "test", "test", "test");
$m->query("SELECT 1 FROM missing_table");
$st2 = $m->prepare("SELECT ? + 1");
$x = 41;
$st2->bind_param("i", $x);
$st2->execute();
$st2->close();
$st3 = $m->stmt_init();
$st3->prepare("SELECT 42");
$st3->execute();
$st3->close();
$m->select_db("information_schema");
$m->query("SELECT 2");
echo "done\n";
');
echo $r['out'];
$t = ol_one_trace($r);
$h = getenv('OPENLOG_TEST_MYSQL');
ob_start();
ol_print_tree($t, ['db.system.name', 'server.address', 'server.port', 'db.namespace', 'db.query.text', 'error.type'], false);
echo str_replace($h, 'HOST', ob_get_clean());
--EXPECTF--
done
php %s kind=1 status=0
  CREATE kind=3 status=0 db.system.name="mysql" server.address="HOST" server.port=3306 db.namespace="test" db.query.text="CREATE TEMPORARY TABLE t (id INT, name VARCHAR(?))"
  INSERT t kind=3 status=0 db.system.name="mysql" server.address="HOST" server.port=3306 db.namespace="test" db.query.text="INSERT INTO t VALUES (?, ?)"
  SELECT t kind=3 status=0 db.system.name="mysql" server.address="HOST" server.port=3306 db.namespace="test" db.query.text="SELECT name FROM t WHERE id = ?"
  SELECT t kind=3 status=0 db.system.name="mysql" server.address="HOST" server.port=3306 db.namespace="test" db.query.text="SELECT name FROM t WHERE id = ?"
  SELECT missing_table kind=3 status=2 db.system.name="mysql" server.address="HOST" server.port=3306 db.namespace="test" db.query.text="SELECT ? FROM missing_table" error.type="_OTHER"
  SELECT kind=3 status=0 db.system.name="mysql" server.address="HOST" server.port=3306 db.namespace="test" db.query.text="SELECT ? + ?"
  SELECT kind=3 status=0 db.system.name="mysql" server.address="HOST" server.port=3306 db.namespace="test" db.query.text="SELECT ?"
  SELECT kind=3 status=0 db.system.name="mysql" server.address="HOST" server.port=3306 db.namespace="information_schema" db.query.text="SELECT ?"

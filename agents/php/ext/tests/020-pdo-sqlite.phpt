--TEST--
PDO (sqlite): exec, query, prepare/execute, failed prepare, exception inside the hooked call
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; if (!extension_loaded('pdo_sqlite')) die('skip pdo_sqlite'); ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$r = ol_run('<?php
$db = new PDO("sqlite::memory:", null, null, [PDO::ATTR_ERRMODE => PDO::ERRMODE_SILENT]);
$db->exec("CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, price REAL)");
$db->exec("INSERT INTO items (name, price) VALUES (\'apple\', 1.5), (\'pear\', 2)");
$st = $db->prepare("SELECT id, name FROM items WHERE price > ? AND name <> :n");
$st->execute([1, "x"]);
$db->query("SELECT count(*) FROM items /* comment 42 */ WHERE name = \'apple\'")->fetchAll();
var_dump(@$db->prepare("SELEKT nope"));
$db->setAttribute(PDO::ATTR_ERRMODE, PDO::ERRMODE_EXCEPTION);
try {
    $db->exec("UPDATE missing SET x = 1");
} catch (PDOException $e) {
    echo "caught ", get_class($e), "\n";
}
echo "done\n";
');
echo $r['out'];
$t = ol_one_trace($r);
ol_print_tree($t, ['db.system.name', 'db.operation.name', 'db.collection.name', 'db.query.text', 'error.type', 'db.namespace']);
--EXPECTF--
bool(false)
caught PDOException
done
php %s kind=1 status=0
  CREATE kind=3 status=0 db.system.name="sqlite" db.operation.name="CREATE" db.query.text="CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, price REAL)" db.namespace=":memory:"
  INSERT items kind=3 status=0 db.system.name="sqlite" db.operation.name="INSERT" db.collection.name="items" db.query.text="INSERT INTO items (name, price) VALUES (?, ?), (?, ?)" db.namespace=":memory:"
  SELECT items kind=3 status=0 db.system.name="sqlite" db.operation.name="SELECT" db.collection.name="items" db.query.text="SELECT id, name FROM items WHERE price > ? AND name <> :n" db.namespace=":memory:"
  SELECT items kind=3 status=0 db.system.name="sqlite" db.operation.name="SELECT" db.collection.name="items" db.query.text="SELECT count(*) FROM items WHERE name = ?" db.namespace=":memory:"
  SELEKT kind=3 status=2 db.system.name="sqlite" db.operation.name="SELEKT" db.query.text="SELEKT nope" error.type="_OTHER" db.namespace=":memory:"
  UPDATE missing kind=3 status=2 db.system.name="sqlite" db.operation.name="UPDATE" db.collection.name="missing" db.query.text="UPDATE missing SET x = ?" error.type="PDOException" db.namespace=":memory:"
    ! exception PDOException: SQLSTATE[HY000]: General error: 1 no such table: missing

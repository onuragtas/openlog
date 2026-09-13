<?php
// mysqli procedural + object-oriented + prepared statement, and PDO mysql.
require __DIR__ . '/lib.php';

$id = isset($_GET['id']) ? ((int) $_GET['id'] % 20) + 1 : 1;
list($host, $user, $pass, $db) = demo_mysqli_params();

// procedural
$link = mysqli_connect($host, $user, $pass, $db);
$res = mysqli_query($link, 'SELECT id, name, email FROM users WHERE id = ' . $id);
$row = mysqli_fetch_assoc($res);
mysqli_free_result($res);
mysqli_close($link);

// object-oriented + prepared statement
$mysqli = new mysqli($host, $user, $pass, $db);
$count = (int) $mysqli->query('SELECT COUNT(*) AS c FROM orders WHERE user_id = ' . $id)->fetch_assoc()['c'];
$stmt = $mysqli->prepare('SELECT o.id, i.name, o.quantity FROM orders o JOIN items i ON i.id = o.item_id WHERE o.user_id = ? ORDER BY o.id DESC LIMIT 5');
$stmt->bind_param('i', $id);
$stmt->execute();
$orders = $stmt->get_result()->fetch_all(MYSQLI_ASSOC);
$stmt->close();
$mysqli->close();

// PDO
$pdo = new PDO("mysql:host=$host;dbname=$db", $user, $pass, [PDO::ATTR_ERRMODE => PDO::ERRMODE_EXCEPTION]);
$st = $pdo->prepare('SELECT SUM(quantity) FROM orders WHERE user_id = :id');
$st->execute(['id' => $id]);

demo_json(['user' => $row, 'orders' => $count, 'recent' => $orders, 'quantity' => (int) $st->fetchColumn()]);

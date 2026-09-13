<?php
// Tiny PHP shop for the php-host (test/localagents): PDO/SQLite queries, an insert and an uncaught exception.
declare(strict_types=1);

$path = parse_url($_SERVER['REQUEST_URI'] ?? '/', PHP_URL_PATH) ?: '/';
$db = new PDO('sqlite:/tmp/php-shop.sqlite');
$db->setAttribute(PDO::ATTR_ERRMODE, PDO::ERRMODE_EXCEPTION);
$db->exec('CREATE TABLE IF NOT EXISTS orders (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, total REAL NOT NULL)');
header('Content-Type: application/json');

if (preg_match('#^/users/(\d+)/orders$#', $path, $m)) {
    $st = $db->prepare('SELECT id, total FROM orders WHERE user_id = ? ORDER BY id DESC LIMIT 20');
    $st->execute([(int) $m[1]]);
    echo json_encode(['user' => (int) $m[1], 'orders' => $st->fetchAll(PDO::FETCH_ASSOC)]);
} elseif ($path === '/checkout') {
    $st = $db->prepare('INSERT INTO orders (user_id, total) VALUES (?, ?)');
    $st->execute([7, random_int(100, 10000) / 100]);
    echo json_encode(['order' => (int) $db->lastInsertId()]);
} elseif ($path === '/error') {
    throw new RuntimeException('payment gateway timeout');
} else {
    $count = (int) $db->query('SELECT COUNT(*) FROM orders')->fetchColumn();
    echo json_encode(['service' => 'php-shop', 'orders' => $count]);
}

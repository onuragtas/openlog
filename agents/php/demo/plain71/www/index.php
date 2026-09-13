<?php
require __DIR__ . '/lib.php';

$pdo = new PDO('mysql:host=' . demo_env('DB_HOST', 'mariadb') . ';dbname=demo', 'demo', 'demo', [PDO::ATTR_ERRMODE => PDO::ERRMODE_EXCEPTION]);
$items = (int) $pdo->query('SELECT COUNT(*) FROM items')->fetchColumn();

demo_json([
    'service' => demo_env('OPENLOG_SERVICE_NAME', 'php-plain-71'),
    'php' => PHP_VERSION,
    'items' => $items,
    'endpoints' => ['/users.php?id=1', '/pg.php?id=1', '/redis.php', '/http.php', '/slow.php', '/boom.php', '/fatal.php',
        '/fatal.php?type=memory', '/fatal.php?type=user'],
]);

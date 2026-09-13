<?php
// pg_connect / pg_query / pg_query_params and pdo_pgsql.
require __DIR__ . '/lib.php';

$id = isset($_GET['id']) ? ((int) $_GET['id'] % 100) + 1 : 1;

$conn = pg_connect(demo_pg_dsn());
$product = pg_fetch_assoc(pg_query($conn, 'SELECT id, name, price, stock FROM products WHERE id = ' . $id));
$reviews = pg_fetch_all(pg_query_params($conn, 'SELECT rating, body FROM reviews WHERE product_id = $1 ORDER BY id LIMIT 3', [$id]));
pg_close($conn);

$pdo = new PDO('pgsql:host=' . demo_env('PG_HOST', 'postgres') . ';dbname=demo', 'demo', 'demo', [PDO::ATTR_ERRMODE => PDO::ERRMODE_EXCEPTION]);
$st = $pdo->prepare('SELECT AVG(rating) FROM reviews WHERE product_id = ?');
$st->execute([$id]);

demo_json(['product' => $product, 'reviews' => $reviews ?: [], 'average' => round((float) $st->fetchColumn(), 2)]);

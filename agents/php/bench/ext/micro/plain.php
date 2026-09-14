<?php
/*
 * Plain PHP request for the per-request micro benchmark (bench/ext/micro/run.sh): a tiny router, 5 PDO SQLite
 * queries (5 instrumented client spans), ~2 000 userland calls and a rendered JSON response. No network.
 * PHP 7.1 compatible.
 */

final class Repo
{
    private static $pdo;

    public static function pdo()
    {
        if (self::$pdo === null) {
            self::$pdo = new PDO('sqlite::memory:');
            self::$pdo->setAttribute(PDO::ATTR_ERRMODE, PDO::ERRMODE_EXCEPTION);
            self::$pdo->exec('CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, price REAL)');
            $st = self::$pdo->prepare('INSERT INTO items (id, name, price) VALUES (?, ?, ?)');
            for ($i = 1; $i <= 50; $i++) {
                $st->execute([$i, 'item-' . $i, $i * 1.5]);
            }
        }
        return self::$pdo;
    }

    public function find($id)
    {
        $st = self::pdo()->prepare('SELECT id, name, price FROM items WHERE id = ?');
        $st->execute([$id]);
        return $st->fetch(PDO::FETCH_ASSOC);
    }
}

final class View
{
    public function esc($s)
    {
        return htmlspecialchars((string) $s, ENT_QUOTES, 'UTF-8');
    }

    public function row(array $item)
    {
        return ['id' => (int) $item['id'], 'name' => $this->esc($item['name']), 'price' => $this->money($item['price'])];
    }

    private function money($v)
    {
        return number_format((float) $v, 2);
    }
}

function route($path)
{
    if (preg_match('#^/items/(\d+)$#', $path, $m)) {
        return ['items', (int) $m[1]];
    }
    return ['home', 0];
}

function work($n)
{
    $acc = 0;
    for ($i = 0; $i < $n; $i++) {
        $acc += strlen(str_pad((string) $i, 4, '0', STR_PAD_LEFT));
    }
    return $acc;
}

$uri = isset($_SERVER['REQUEST_URI']) ? $_SERVER['REQUEST_URI'] : '/items/7';
list($action, $id) = route(parse_url($uri, PHP_URL_PATH));
$repo = new Repo();
$view = new View();
$rows = [];
for ($k = 0; $k < 5; $k++) {
    $item = $repo->find((($id + $k) % 50) + 1);
    if ($item) {
        $rows[] = $view->row($item);
    }
}
$sum = 0;
for ($k = 0; $k < 400; $k++) {
    $sum += work(5);
}
header('Content-Type: application/json');
echo json_encode(['action' => $action, 'rows' => $rows, 'sum' => $sum]);

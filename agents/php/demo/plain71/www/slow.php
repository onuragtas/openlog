<?php
// > 600 ms of nested userland functions (transaction tracer).
require __DIR__ . '/lib.php';

function slow_load_inventory(mysqli $db)
{
    $db->query('SELECT SLEEP(0.1)');
    usleep(60000);
    return $db->query('SELECT id, name, price FROM items ORDER BY id LIMIT 50')->fetch_all(MYSQLI_ASSOC);
}

function slow_price_item(array $item)
{
    return round($item['price'] * 1.19, 2);
}

function slow_price_all(array $items)
{
    $out = [];
    foreach ($items as $item) {
        $out[$item['id']] = slow_price_item($item);
    }
    usleep(50000);
    return $out;
}

function slow_checksum(array $prices)
{
    $x = 0;
    for ($i = 0; $i < 300000; $i++) {
        $x = ($x * 31 + $i + count($prices)) % 1000003;
    }
    return $x;
}

function slow_render(array $prices, $checksum)
{
    usleep(120000);
    return slow_render_footer(count($prices), $checksum);
}

function slow_render_footer($n, $checksum)
{
    usleep(240000);
    return "$n items, checksum $checksum";
}

function slow_report()
{
    list($host, $user, $pass, $db) = demo_mysqli_params();
    $mysqli = new mysqli($host, $user, $pass, $db);
    $items = slow_load_inventory($mysqli);
    $prices = slow_price_all($items);
    $checksum = slow_checksum($prices);
    usleep(150000);
    $footer = slow_render($prices, $checksum);
    $mysqli->close();
    return $footer;
}

$started = microtime(true);
$footer = slow_report();
demo_json(['footer' => $footer, 'elapsed_ms' => (int) ((microtime(true) - $started) * 1000)]);

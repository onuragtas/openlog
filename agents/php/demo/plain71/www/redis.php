<?php
// phpredis: connect, strings, hashes, pipeline.
require __DIR__ . '/lib.php';

$redis = new Redis();
$redis->connect(demo_env('REDIS_HOST', 'redis'), 6379, 0.5);
$hits = $redis->incr('plain71:hits');
$redis->hMSet('plain71:last', ['time' => (string) microtime(true), 'uri' => $_SERVER['REQUEST_URI']]);
$last = $redis->hGetAll('plain71:last');
$pipe = $redis->multi(Redis::PIPELINE);
for ($i = 0; $i < 3; $i++) {
    $pipe->set("plain71:k$i", (string) $i);
}
$pipe->exec();
$redis->close();

demo_json(['hits' => $hits, 'last' => $last]);

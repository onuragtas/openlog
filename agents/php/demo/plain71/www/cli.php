<?php
// CLI transaction: docker compose -p openlog-php exec php-plain-71 php /var/www/html/cli.php [iterations]
require __DIR__ . '/lib.php';

if (PHP_SAPI !== 'cli') {
    http_response_code(404);
    exit;
}

function cli_sync_users(mysqli $db, Redis $redis)
{
    $res = $db->query('SELECT id, name FROM users ORDER BY id');
    $n = 0;
    while ($row = $res->fetch_assoc()) {
        $redis->set('cli:user:' . $row['id'], $row['name']);
        $n++;
    }
    return $n;
}

function cli_notify($url)
{
    $ch = curl_init($url);
    curl_setopt_array($ch, [CURLOPT_RETURNTRANSFER => true, CURLOPT_TIMEOUT => 3]);
    curl_exec($ch);
    $code = curl_getinfo($ch, CURLINFO_HTTP_CODE);
    curl_close($ch);
    return $code;
}

$iterations = isset($argv[1]) ? max(1, (int) $argv[1]) : 1;
list($host, $user, $pass, $dbName) = demo_mysqli_params();
$db = new mysqli($host, $user, $pass, $dbName);
$redis = new Redis();
$redis->connect(demo_env('REDIS_HOST', 'redis'), 6379, 0.5);
for ($i = 0; $i < $iterations; $i++) {
    $synced = cli_sync_users($db, $redis);
    usleep(100000);
}
$code = cli_notify(demo_upstream() . '/health');
fwrite(STDOUT, "synced $synced users x $iterations, notify status $code\n");

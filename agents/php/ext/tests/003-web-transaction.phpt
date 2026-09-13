--TEST--
Web transaction (php-cgi): server span attributes, normalized plain PHP name, status code
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; if (!is_executable(dirname(PHP_BINARY) . '/php-cgi')) die('skip no php-cgi'); ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$r = ol_run('<?php http_response_code(201); echo "ok";', ['cgi' => true, 'server' => [
    'REQUEST_METHOD' => 'post',
    'REQUEST_URI' => '/orders/42/items/5f0e8a9b1c2d3e4f?x=1',
    'HTTPS' => 'on',
]]);
echo $r['out'], "\n";
$t = ol_one_trace($r);
$root = ol_root($t);
echo $root['name'], " kind=", $root['kind'], " status=", $root['status'], "\n";
$a = $root['attrs'];
ksort($a);
foreach ($a as $k => $v) echo "$k=", json_encode($v), "\n";
echo $t['resource']['php.sapi'], "\n";

$r = ol_run('<?php http_response_code(503);', ['cgi' => true, 'server' => ['REQUEST_URI' => '/health']]);
$root = ol_root(ol_one_trace($r));
echo $root['name'], " status=", $root['status'], " code=", $root['attrs']['http.response.status_code'], "\n";
--EXPECT--
ok
POST /orders/{id}/items/{hex} kind=2 status=0
client.address="10.1.2.3"
http.request.method="post"
http.response.status_code=201
network.protocol.version="1.1"
server.address="shop.test"
server.port=8080
url.path="\/orders\/42\/items\/5f0e8a9b1c2d3e4f"
url.scheme="https"
user_agent.original="phpt"
cgi-fcgi
GET /health status=2 code=503

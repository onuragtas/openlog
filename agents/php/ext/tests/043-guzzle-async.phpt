--TEST--
Guzzle 7 (real library): async promises and Pool on CurlMultiHandler, sync CurlHandler — one client span per request with its own traceparent
--SKIPIF--
<?php
require __DIR__ . '/inc/skipif.php';
if (!extension_loaded('curl')) die('skip curl');
if (!getenv('OPENLOG_TEST_GUZZLE') || !is_file(getenv('OPENLOG_TEST_GUZZLE'))) die('skip OPENLOG_TEST_GUZZLE (vendor/autoload.php with guzzlehttp/guzzle, see build/test-guzzle.sh) not set');
?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$srv = ol_http_server();
$code = '<?php
require getenv("OL_AUTOLOAD");
use GuzzleHttp\Client;
use GuzzleHttp\Pool;
use GuzzleHttp\Psr7\Request;
use GuzzleHttp\Promise\Utils;

$client = new Client(["base_uri" => getenv("OL_HTTP"), "http_errors" => false]);
$seen = [];
$record = function ($k, $response) use (&$seen) {
    $b = json_decode((string) $response->getBody(), true);
    $seen[$k] = [$response->getStatusCode(), $b["traceparent"], $b["method"]];
};
/* promises: three requests in flight at once */
$promises = [
    "a" => $client->getAsync("/?sleep=50&k=a"),
    "b" => $client->getAsync("/?sleep=50&status=404&k=b"),
    "c" => $client->postAsync("/?k=c", ["body" => "x"]),
];
foreach (Utils::settle($promises)->wait() as $k => $res) {
    $record($k, $res["value"]);
}
/* Pool, concurrency 2 */
$requests = function () { for ($i = 0; $i < 4; $i++) { yield new Request("GET", "/?sleep=20&k=p" . $i); } };
(new Pool($client, $requests(), ["concurrency" => 2, "fulfilled" => function ($response, $i) use ($record) { $record("p" . $i, $response); }]))
    ->promise()->wait();
/* synchronous request */
$record("s", $client->get("/?k=s"));
ksort($seen);
echo json_encode($seen), "\n";
';
$r = ol_run($code, ['env' => ['OL_HTTP' => $srv['url'], 'OL_AUTOLOAD' => getenv('OPENLOG_TEST_GUZZLE')]]);
foreach ($r['invalid'] as $p) {
    echo "INVALID: $p\n";
}
$lines = array_values(array_filter(explode("\n", trim($r['out']))));
$seen = json_decode((string) end($lines), true);
if (!is_array($seen)) {
    echo "no client output:\n", $r['out'], $r['err'];
    $seen = [];
}
$traces = ol_traces($r['msgs']);
$tid = (string) key($traces);
$t = reset($traces);
$root = ol_root($t);
$spans = [];
foreach ($t['spans'] as $s) {
    if ($s['kind'] !== 3) {
        continue;
    }
    parse_str((string) parse_url($s['attrs']['url.full'], PHP_URL_QUERY), $q);
    $spans[isset($q['k']) ? $q['k'] : '?'][] = $s;
}
foreach ($seen as $k => $v) {
    list($status, $tp, $method) = $v;
    $s = isset($spans[$k]) && count($spans[$k]) === 1 ? $spans[$k][0] : null;
    $p = $tp ? explode('-', $tp) : ['', '', ''];
    echo $k, ": ", $method, " ", $status,
        " span=", $s ? $s['name'] : (isset($spans[$k]) ? count($spans[$k]) . ' spans' : 'missing'),
        " code=", $s && isset($s['attrs']['http.response.status_code']) ? $s['attrs']['http.response.status_code'] : '-',
        " status=", $s ? $s['status'] : '-',
        " traceparent=", $s && $p[1] === $tid && $p[2] === $s['id'] ? 'own span' : 'WRONG (' . $tp . ')',
        " parent=", $s && $s['parent'] === $root['id'] ? 'root' : 'other', "\n";
}
echo count(call_user_func_array('array_merge', array_values($spans ?: [[]]))), " client spans in 1 trace (", count($traces), ")\n";
--EXPECT--
a: GET 200 span=GET code=200 status=0 traceparent=own span parent=root
b: GET 404 span=GET code=404 status=2 traceparent=own span parent=root
c: POST 200 span=POST code=200 status=0 traceparent=own span parent=root
p0: GET 200 span=GET code=200 status=0 traceparent=own span parent=root
p1: GET 200 span=GET code=200 status=0 traceparent=own span parent=root
p2: GET 200 span=GET code=200 status=0 traceparent=own span parent=root
p3: GET 200 span=GET code=200 status=0 traceparent=own span parent=root
s: GET 200 span=GET code=200 status=0 traceparent=own span parent=root
8 client spans in 1 trace (1)

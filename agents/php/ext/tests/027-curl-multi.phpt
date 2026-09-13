--TEST--
curl_multi: one client span per handle from add to remove, headers injected
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; if (!extension_loaded('curl')) die('skip curl'); ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$srv = ol_http_server();
$r = ol_run('<?php
$url = getenv("OL_HTTP");
$mh = curl_multi_init();
$hs = [];
foreach (["a" => 200, "b" => 503] as $k => $status) {
    $h = curl_init("$url/?sleep=60&status=$status&k=$k");
    curl_setopt($h, CURLOPT_RETURNTRANSFER, 1);
    curl_setopt($h, CURLOPT_HTTPHEADER, ["X-Test: $k"]);
    curl_multi_add_handle($mh, $h);
    $hs[] = $h;
}
do {
    $st = curl_multi_exec($mh, $running);
    if ($running) curl_multi_select($mh, 0.1);
} while ($running && $st == CURLM_OK);
foreach ($hs as $h) {
    $b = json_decode(curl_multi_getcontent($h), true);
    echo $b["x_test"], " ", $b["traceparent"] ? "tp" : "none", "\n";
    curl_multi_remove_handle($mh, $h);
}
$never = curl_init("$url/?k=never");
curl_multi_add_handle($mh, $never);
', ['env' => ['OL_HTTP' => $srv['url']]]);
echo $r['out'];
$t = ol_one_trace($r);
foreach ($t['spans'] as $s) {
    if ($s['kind'] !== 3) continue;
    echo $s['name'], " status=", $s['status'], " code=", isset($s['attrs']['http.response.status_code']) ? $s['attrs']['http.response.status_code'] : '-',
        " dur>=50ms=", $s['dur'] >= 50000000 ? 'yes' : 'no', " ", preg_replace('/127\.0\.0\.1:\d+/', 'srv', $s['attrs']['url.full']), "\n";
}
--EXPECT--
a tp
b tp
GET status=0 code=200 dur>=50ms=yes http://srv/?sleep=60&status=200&k=a
GET status=2 code=503 dur>=50ms=yes http://srv/?sleep=60&status=503&k=b
GET status=0 code=- dur>=50ms=no http://srv/?k=never

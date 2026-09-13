--TEST--
curl_exec: client spans, traceparent injection (keeping application headers), errors, unsampled propagation
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; if (!extension_loaded('curl')) die('skip curl'); ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$srv = ol_http_server();
$code = '<?php
$url = getenv("OL_HTTP");
$ch = curl_init($url . "/echo?status=200");
curl_setopt($ch, CURLOPT_RETURNTRANSFER, true);
curl_setopt($ch, CURLOPT_HTTPHEADER, ["X-Test: keep"]);
$b = json_decode(curl_exec($ch), true);
echo "1 ", $b["x_test"], " ", $b["traceparent"], "\n";
$b = json_decode(curl_exec($ch), true);
echo "2 again ", $b["x_test"], " ", $b["traceparent"], "\n";
$ch2 = curl_init();
curl_setopt_array($ch2, [CURLOPT_URL => $url . "/?status=404", CURLOPT_RETURNTRANSFER => true, CURLOPT_POSTFIELDS => "a=1"]);
$b = json_decode(curl_exec($ch2), true);
echo "3 ", $b["method"], " ", $b["traceparent"], "\n";
$ch3 = curl_init($url);
curl_setopt($ch3, CURLOPT_RETURNTRANSFER, 1);
curl_setopt($ch3, CURLOPT_HTTPHEADER, ["traceparent: 00-11111111111111111111111111111111-2222222222222222-01"]);
echo "4 own ", json_decode(curl_exec($ch3), true)["traceparent"], "\n";
$ch4 = curl_init("http://user:secret@127.0.0.1:1/x?y=1");
curl_setopt($ch4, CURLOPT_RETURNTRANSFER, 1);
var_dump(curl_exec($ch4));
$ch5 = curl_copy_handle($ch);
echo "5 copy ", json_decode(curl_exec($ch5), true)["x_test"], "\n";
try { @curl_exec("nope"); } catch (Throwable $e) { }
echo "6 not a handle\n";
echo "trace ", \openlog\trace_id(), "\n";
';
$r = ol_run($code, ['env' => ['OL_HTTP' => $srv['url']]]);
$out = $r['out'];
$t = ol_one_trace($r);
$spans = [];
foreach ($t['spans'] as $s) if ($s['kind'] === 3) $spans[$s['id']] = $s;
// every injected traceparent names this trace and one of its client spans
preg_match_all('/00-([0-9a-f]{32})-([0-9a-f]{16})-01/', $out, $m, PREG_SET_ORDER);
preg_match('/trace ([0-9a-f]{32})/', $out, $tid);
foreach ($m as $hdr) {
    if ($hdr[1] === '11111111111111111111111111111111') continue;
    echo "header -> ", $hdr[1] === $tid[1] ? 'this trace' : 'OTHER TRACE', ", ", isset($spans[$hdr[2]]) ? 'client span ' . $spans[$hdr[2]]['name'] : 'UNKNOWN SPAN', "\n";
}
echo preg_replace('/00-(?!1{32})[0-9a-f]{32}-[0-9a-f]{16}-01/', '<tp>', $out);
ol_print_tree($t, ['http.request.method', 'url.full', 'server.address', 'http.response.status_code', 'error.type'], false);

// unsampled: header with flags 00, nothing sent
$r = ol_run('<?php $ch = curl_init(getenv("OL_HTTP")); curl_setopt($ch, CURLOPT_RETURNTRANSFER, 1); echo json_decode(curl_exec($ch), true)["traceparent"];',
    ['env' => ['OL_HTTP' => $srv['url']], 'ini' => ['openlog.sampling_ratio' => '0']]);
echo "unsampled: ", preg_replace('/[0-9a-f]{32}-[0-9a-f]{16}/', '<ids>', $r['out']), " messages=", count($r['msgs']), "\n";
--EXPECTF--
header -> this trace, client span GET
header -> this trace, client span GET
header -> this trace, client span POST
1 keep <tp>
2 again keep <tp>
3 POST <tp>
4 own 00-11111111111111111111111111111111-2222222222222222-01
bool(false)
5 copy keep
6 not a handle
trace %s
php %s kind=1 status=0
  GET kind=3 status=0 http.request.method="GET" url.full="http://127.0.0.1:%d/echo?status=200" server.address="127.0.0.1" http.response.status_code=200
  GET kind=3 status=0 http.request.method="GET" url.full="http://127.0.0.1:%d/echo?status=200" server.address="127.0.0.1" http.response.status_code=200
  POST kind=3 status=2 http.request.method="POST" url.full="http://127.0.0.1:%d/?status=404" server.address="127.0.0.1" http.response.status_code=404 error.type="404"
  GET kind=3 status=0 http.request.method="GET" url.full="http://127.0.0.1:%d" server.address="127.0.0.1" http.response.status_code=200
  GET kind=3 status=2 http.request.method="GET" url.full="http://127.0.0.1:1/x?y=1" server.address="127.0.0.1" error.type="curl_error_7"
  GET kind=3 status=0 http.request.method="GET" url.full="http://127.0.0.1:%d/echo?status=200" server.address="127.0.0.1" http.response.status_code=200
unsampled: 00-<ids>-00 messages=0

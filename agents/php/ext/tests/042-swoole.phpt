--TEST--
Swoole HTTP server with coroutines: 4 concurrent requests in one worker, one trace each, per-request trace context for outgoing curl and openlog\traceparent(), no span of one request in another
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; if (!extension_loaded('swoole')) die('skip swoole'); if (!extension_loaded('curl')) die('skip curl'); ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$srv = ol_http_server();
$port = random_int(46000, 60000);
$code = '<?php
use Swoole\Coroutine;
$port = (int) getenv("OL_PORT");
$echo = getenv("OL_HTTP");
$server = new Swoole\Http\Server("127.0.0.1", $port, SWOOLE_BASE);
$server->set(["worker_num" => 1, "enable_coroutine" => true, "log_level" => SWOOLE_LOG_ERROR, "hook_flags" => SWOOLE_HOOK_ALL]);
$server->on("request", function ($req, $res) use ($echo) {
    $uri = $req->server["request_uri"];
    Coroutine::sleep(0.3); /* the four requests overlap */
    $ch = curl_init($echo . "/?k=" . urlencode($uri));
    curl_setopt($ch, CURLOPT_RETURNTRANSFER, 1);
    $body = json_decode((string) curl_exec($ch), true);
    $res->status(strpos($uri, "missing") !== false ? 404 : 200);
    $res->end(json_encode(["tp" => isset($body["traceparent"]) ? $body["traceparent"] : "", "mine" => openlog\traceparent()]));
});
$server->on("workerStart", function ($server) use ($port) {
    Coroutine::create(function () use ($server, $port) {
        $out = [];
        $wg = new Coroutine\WaitGroup();
        foreach (["/a/1", "/b/2", "/missing/3", "/d/4"] as $i => $uri) {
            $wg->add();
            Coroutine::create(function () use ($uri, $i, $port, &$out, $wg) {
                $c = new Coroutine\Http\Client("127.0.0.1", $port);
                $h = [];
                if ($i % 2 === 0) { $h["traceparent"] = sprintf("00-%032x-%016x-01", $i + 1, $i + 100); }
                $c->setHeaders($h);
                $c->get($uri);
                $out[$uri] = [isset($h["traceparent"]) ? $h["traceparent"] : "", json_decode($c->body, true), $c->statusCode];
                $c->close();
                $wg->done();
            });
        }
        $wg->wait(20);
        echo json_encode($out), "\n";
        $server->shutdown();
    });
});
$server->start();
';
$r = ol_run($code, ['env' => ['OL_HTTP' => $srv['url'], 'OL_PORT' => (string) $port]]);
foreach ($r['invalid'] as $p) {
    echo "INVALID: $p\n";
}
$lines = array_values(array_filter(explode("\n", trim($r['out']))));
$out = json_decode((string) end($lines), true);
if (!is_array($out)) {
    echo "no client output:\n", $r['out'], $r['err'];
}
$traces = ol_traces($r['msgs']);
$tid = function ($tp) { return $tp ? explode('-', $tp)[1] : ''; };
ksort($out);
foreach ($out as $uri => $o) {
    list($sent, $body, $status) = $o;
    $expect = $sent !== '' ? $tid($sent) : $tid($body['mine']);
    $t = isset($traces[$expect]) ? $traces[$expect] : null;
    $root = $t ? ol_root($t) : null;
    echo $uri, ": status=", $status,
        " traceparent()=", $tid($body['mine']) === $expect ? 'own' : 'WRONG',
        " outgoing=", $tid($body['tp']) === $expect ? 'own' : 'WRONG (' . $body['tp'] . ')',
        " trace=", $t ? 'sent' : 'missing',
        " name=", $root ? $root['name'] : '-',
        " code=", isset($root['attrs']['http.response.status_code']) ? $root['attrs']['http.response.status_code'] : '-',
        " spans=", $t ? count($t['spans']) : 0,
        $sent !== '' ? ' parent=' . ($root && $root['parent'] === explode('-', $sent)[2] ? 'remote' : 'WRONG') : '', "\n";
}
$concurrent = 0;
foreach ($traces as $t) {
    $root = ol_root($t);
    $concurrent += !empty($root['attrs']['openlog.php.concurrent']) ? 1 : 0;
}
echo count($traces), " traces, marked concurrent: ", $concurrent, "\n";
--EXPECT--
/a/1: status=200 traceparent()=own outgoing=own trace=sent name=GET /a/{id} code=200 spans=1 parent=remote
/b/2: status=200 traceparent()=own outgoing=own trace=sent name=GET /b/{id} code=200 spans=1
/d/4: status=200 traceparent()=own outgoing=own trace=sent name=GET /d/{id} code=200 spans=1
/missing/3: status=404 traceparent()=own outgoing=own trace=sent name=GET /missing/{id} code=404 spans=1 parent=remote
4 traces, marked concurrent: 4

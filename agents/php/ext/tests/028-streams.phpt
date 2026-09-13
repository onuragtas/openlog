--TEST--
http(s) streams: file_get_contents / fopen spans, header injection into the context, context restored
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$srv = ol_http_server();
$r = ol_run('<?php
$url = getenv("OL_HTTP");
$b = json_decode(file_get_contents("$url/?x=1"), true);
echo "default ctx: ", $b["traceparent"] ? "tp" : "none", "\n";
$ctx = stream_context_create(["http" => ["header" => "X-Test: ctx\r\n", "method" => "POST", "ignore_errors" => true]]);
$b = json_decode(file_get_contents("$url/?status=500", false, $ctx), true);
echo "string header: ", $b["x_test"], " ", $b["method"], " ", $b["traceparent"] ? "tp" : "none", "\n";
echo "restored: ", json_encode(stream_context_get_options($ctx)["http"]["header"]), "\n";
$def = stream_context_get_options(stream_context_get_default());
echo "default clean: ", isset($def["http"]["header"]) ? "no" : "yes", "\n";
$ctx2 = stream_context_create(["http" => ["header" => ["X-Test: arr"]]]);
$b = json_decode(file_get_contents($url, false, $ctx2), true);
echo "array header: ", $b["x_test"], " ", $b["traceparent"] ? "tp" : "none", "\n";
$fp = fopen("$url/?status=201", "r");
$b = json_decode(stream_get_contents($fp), true);
fclose($fp);
echo "fopen: ", $b["traceparent"] ? "tp" : "none", "\n";
var_dump(@file_get_contents("http://127.0.0.1:1/"));
echo strlen(file_get_contents(__FILE__)) > 0 ? "local file ok\n" : "";
class Controller {
    public function health($url) {
        $ctx = stream_context_create(["http" => ["timeout" => 2]]);
        $stream = @file_get_contents($url . "/?status=202", false, $ctx);
        return $stream !== false ? "method ok" : "method failed";
    }
}
echo call_user_func_array([new Controller(), "health"], [$url]), "\n";
', ['env' => ['OL_HTTP' => $srv['url']], 'ini' => ['opcache.enable_cli' => '1']]);
echo $r['out'];
$t = ol_one_trace($r);
foreach ($t['spans'] as $s) {
    if ($s['kind'] !== 3) continue;
    echo $s['name'], " status=", $s['status'], " code=", isset($s['attrs']['http.response.status_code']) ? $s['attrs']['http.response.status_code'] : '-',
        " ", preg_replace('/127\.0\.0\.1:\d{3,}/', 'srv', $s['attrs']['url.full']), "\n";
}
--EXPECT--
default ctx: tp
string header: ctx POST tp
restored: "X-Test: ctx\r\n"
default clean: yes
array header: arr tp
fopen: tp
bool(false)
local file ok
method ok
GET status=0 code=200 http://srv/?x=1
POST status=2 code=500 http://srv/?status=500
GET status=0 code=200 http://srv
GET status=0 code=201 http://srv/?status=201
GET status=2 code=- http://127.0.0.1:1/
GET status=0 code=202 http://srv/?status=202

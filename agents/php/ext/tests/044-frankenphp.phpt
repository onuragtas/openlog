--TEST--
FrankenPHP (real server): worker mode (one transaction per frankenphp_handle_request() callback, nothing between requests, per-request $_SERVER attributes, status, exception, exit(), worker restart) and classic mode
--SKIPIF--
<?php
require __DIR__ . '/inc/skipif.php';
if (!extension_loaded('pdo_sqlite')) die('skip pdo_sqlite');
$found = false;
foreach (explode(PATH_SEPARATOR, (string) getenv('PATH')) as $d) {
    if ($d !== '' && is_executable($d . '/frankenphp')) $found = true;
}
if (!$found) die('skip frankenphp binary not in PATH (build/run-matrix.sh tag 8.4-frankenphp)');
?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';

function fphp_bin()
{
    foreach (explode(PATH_SEPARATOR, (string) getenv('PATH')) as $d) {
        if ($d !== '' && is_executable($d . '/frankenphp')) {
            return $d . '/frankenphp';
        }
    }
    return '';
}

function fphp_drain($server, array &$raw)
{
    while (($d = stream_socket_recvfrom($server, 65536)) !== false && $d !== '') {
        $raw[] = $d;
    }
}

/**
 * Starts `frankenphp php-server` on $root (worker: $root/index.php as a worker script) with openlog.so loaded,
 * sends $requests ([uri, headers]) one after another, stops the server and returns the validated messages.
 */
function fphp_run($root, $worker, array $requests)
{
    $sock = ol_tmp('.sock');
    $server = stream_socket_server('udg://' . $sock, $errno, $errstr, STREAM_SERVER_BIND);
    if (!$server) {
        throw new RuntimeException("receiver: $errstr");
    }
    stream_set_blocking($server, false);
    $home = ol_tmp('.home');
    mkdir($home . '/ini', 0777, true);
    file_put_contents($home . '/ini/zz-openlog.ini', implode("\n", [
        'extension=' . ol_ext_so(),
        'openlog.transport=unix://' . $sock,
        'openlog.service_name=fphp',
        'openlog.transaction_tracer.enabled=0',
        'display_errors=0',
        'log_errors=0',
        '',
    ]));
    $env = getenv();
    unset($env['OPENLOG_SERVICE_NAME'], $env['TRACEPARENT'], $env['TRACESTATE']);
    $env['PHP_INI_SCAN_DIR'] = ':' . $home . '/ini';
    $env['XDG_CONFIG_HOME'] = $home;
    $env['XDG_DATA_HOME'] = $home;
    $raw = [];
    $out = [];
    for ($try = 0; $try < 10; $try++) {
        $port = random_int(20000, 45000);
        $cmd = escapeshellarg(fphp_bin()) . ' php-server --listen 127.0.0.1:' . $port . ' --root ' . escapeshellarg($root);
        if ($worker) {
            $cmd .= ' --worker ' . escapeshellarg($root . '/index.php') . ',1';
        }
        $proc = proc_open('exec ' . $cmd, [0 => ['pipe', 'r'], 1 => ['file', $home . '/server.log', 'w'], 2 => ['file', $home . '/server.log', 'a']], $pipes, null, $env);
        fclose($pipes[0]);
        $up = false;
        for ($i = 0; $i < 150 && !$up; $i++) {
            $fp = @fsockopen('127.0.0.1', $port, $en, $es, 0.1);
            if ($fp) {
                fclose($fp);
                $up = true;
            } else {
                $st = proc_get_status($proc);
                if (!$st['running']) {
                    break;
                }
                usleep(20000);
            }
        }
        if ($up) {
            break;
        }
        proc_terminate($proc, 9);
        proc_close($proc);
        $proc = null;
    }
    if (!$proc) {
        throw new RuntimeException('frankenphp did not start: ' . @file_get_contents($home . '/server.log'));
    }
    foreach ($requests as $req) {
        list($uri, $headers) = $req;
        $h = '';
        foreach ($headers as $k => $v) {
            $h .= "$k: $v\r\n";
        }
        $ctx = stream_context_create(['http' => ['header' => $h, 'ignore_errors' => true, 'timeout' => 20]]);
        $body = @file_get_contents('http://127.0.0.1:' . $port . $uri, false, $ctx);
        $status = isset($http_response_header[0]) ? substr($http_response_header[0], 9, 3) : '???';
        $out[] = rtrim("$uri -> $status " . trim((string) $body));
        fphp_drain($server, $raw);
    }
    usleep(200000);
    fphp_drain($server, $raw);
    proc_terminate($proc, 15);
    $deadline = microtime(true) + 15;
    while (proc_get_status($proc)['running'] && microtime(true) < $deadline) {
        fphp_drain($server, $raw);
        usleep(20000);
    }
    if (proc_get_status($proc)['running']) {
        proc_terminate($proc, 9);
        $out[] = 'frankenphp did not stop on SIGTERM';
    }
    proc_close($proc);
    usleep(50000);
    fphp_drain($server, $raw);
    fclose($server);
    @unlink($sock);

    $msgs = [];
    foreach ($raw as $i => $d) {
        $m = json_decode($d, true);
        if (!is_array($m)) {
            $out[] = "INVALID: datagram $i: invalid JSON";
            continue;
        }
        foreach (ol_validate($d, $m) as $problem) {
            $out[] = "INVALID: datagram $i: $problem";
        }
        $msgs[] = $m;
    }
    return ['out' => $out, 'msgs' => $msgs, 'home' => $home];
}

function fphp_show(array $r)
{
    foreach ($r['out'] as $l) {
        echo $l, "\n";
    }
    $traces = ol_traces($r['msgs']);
    uasort($traces, function ($a, $b) {
        return ol_root($a)['start'] <=> ol_root($b)['start'];
    });
    echo count($traces), " traces\n";
    foreach ($traces as $id => $t) {
        ol_print_tree($t, ['http.response.status_code', 'url.path', 'server.address', 'client.address',
            'user_agent.original', 'db.query.text']);
        $root = ol_root($t);
        echo '  trace=', $id === '4bf92f3577b34da6a3ce929d0e0e4736' ? 'continued' : 'new', ' parent=',
            $root['parent'] === '' ? '-' : $root['parent'], ' port=', isset($root['attrs']['server.port']) ? 'yes' : 'no', "\n";
    }
}

$tp = ['traceparent' => '00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01', 'User-Agent' => 'fphp-client'];
$base = ol_tmp('.apps');

/* worker: 4 requests per worker script run (then the script returns and FrankenPHP restarts it); exit() in a request
 * also ends the script. The script's own run (boot query, queries between requests) is never sent. */
mkdir($base . '/worker', 0777, true);
$between = $base . '/between.log';
file_put_contents($base . '/worker/index.php', '<?php
$pdo = new PDO("sqlite::memory:");
$pdo->exec("CREATE TABLE boot (id INTEGER)");
$handler = static function () use ($pdo) {
    $path = parse_url($_SERVER["REQUEST_URI"], PHP_URL_PATH);
    $pdo->query("SELECT 1");
    if ($path === "/missing") {
        http_response_code(404);
    } elseif ($path === "/boom") {
        throw new RuntimeException("boom in worker");
    } elseif ($path === "/exit") {
        echo "bye";
        exit(0);
    }
    echo "trace=", openlog\trace_id() === "" ? "none" : "set", " sampled=", var_export(openlog\is_sampled(), true);
};
for ($n = 0; $n < 4; $n++) {
    $keep = frankenphp_handle_request($handler);
    $pdo->query("SELECT 99");
    file_put_contents(' . var_export($between, true) . ', "between: trace_id=\"" . openlog\trace_id() . "\" sampled=" . var_export(openlog\is_sampled(), true) . "\n", FILE_APPEND);
    if (!$keep) {
        break;
    }
}
');
echo "== worker\n";
$r = fphp_run($base . '/worker', true, [
    ['/users/7?x=1', $tp],
    ['/missing', []],
    ['/boom', []],
    ['/users/8', []],
    ['/exit', []],
    ['/users/9', []],
]);
fphp_show($r);
echo implode('', array_unique(file($between))), "\n";

echo "== classic\n";
mkdir($base . '/classic', 0777, true);
file_put_contents($base . '/classic/index.php', '<?php
$path = parse_url($_SERVER["REQUEST_URI"], PHP_URL_PATH);
$pdo = new PDO("sqlite::memory:");
$pdo->query("SELECT 1");
if ($path === "/missing") {
    http_response_code(404);
} elseif ($path === "/boom") {
    throw new RuntimeException("boom in classic");
}
echo "trace=", openlog\trace_id() === "" ? "none" : "set";
');
$r = fphp_run($base . '/classic', false, [
    ['/users/7?x=1', $tp],
    ['/missing', []],
    ['/boom', []],
]);
fphp_show($r);
?>
--EXPECT--
== worker
/users/7?x=1 -> 200 trace=set sampled=true
/missing -> 404 trace=set sampled=true
/boom -> 500
/users/8 -> 200 trace=set sampled=true
/exit -> 200 bye
/users/9 -> 200 trace=set sampled=true
6 traces
GET /users/{id} kind=2 status=0 http.response.status_code=200 url.path="/users/7" server.address="127.0.0.1" client.address="127.0.0.1" user_agent.original="fphp-client"
  SELECT kind=3 status=0 db.query.text="SELECT ?"
  trace=continued parent=00f067aa0ba902b7 port=yes
GET /missing kind=2 status=0 http.response.status_code=404 url.path="/missing" server.address="127.0.0.1" client.address="127.0.0.1"
  SELECT kind=3 status=0 db.query.text="SELECT ?"
  trace=new parent=- port=yes
GET /boom kind=2 status=2 http.response.status_code=500 url.path="/boom" server.address="127.0.0.1" client.address="127.0.0.1"
  ! exception RuntimeException: boom in worker
  SELECT kind=3 status=0 db.query.text="SELECT ?"
  trace=new parent=- port=yes
GET /users/{id} kind=2 status=0 http.response.status_code=200 url.path="/users/8" server.address="127.0.0.1" client.address="127.0.0.1"
  SELECT kind=3 status=0 db.query.text="SELECT ?"
  trace=new parent=- port=yes
GET /exit kind=2 status=0 http.response.status_code=200 url.path="/exit" server.address="127.0.0.1" client.address="127.0.0.1"
  SELECT kind=3 status=0 db.query.text="SELECT ?"
  trace=new parent=- port=yes
GET /users/{id} kind=2 status=0 http.response.status_code=200 url.path="/users/9" server.address="127.0.0.1" client.address="127.0.0.1"
  SELECT kind=3 status=0 db.query.text="SELECT ?"
  trace=new parent=- port=yes
between: trace_id="" sampled=false

== classic
/users/7?x=1 -> 200 trace=set
/missing -> 404 trace=set
/boom -> 500
3 traces
GET /users/{id} kind=2 status=0 http.response.status_code=200 url.path="/users/7" server.address="127.0.0.1" client.address="127.0.0.1" user_agent.original="fphp-client"
  SELECT kind=3 status=0 db.query.text="SELECT ?"
  trace=continued parent=00f067aa0ba902b7 port=yes
GET /missing kind=2 status=0 http.response.status_code=404 url.path="/missing" server.address="127.0.0.1" client.address="127.0.0.1"
  SELECT kind=3 status=0 db.query.text="SELECT ?"
  trace=new parent=- port=yes
GET /boom kind=2 status=2 http.response.status_code=500 url.path="/boom" server.address="127.0.0.1" client.address="127.0.0.1"
  ! exception RuntimeException: boom in classic
  SELECT kind=3 status=0 db.query.text="SELECT ?"
  trace=new parent=- port=yes

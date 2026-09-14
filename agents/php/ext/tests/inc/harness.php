<?php
/*
 * phpt harness: runs PHP code in a child process (CLI or php-cgi) with openlog.so loaded, receives the extension's
 * datagrams on a unix datagram socket (the test receiver) and validates them against the forwarder's input rules
 * (docs/contracts/php-agent.md §2, §6). PHP 7.1 compatible syntax.
 */

function ol_ext_so()
{
    $p = getenv('OPENLOG_EXT_SO');
    if ($p && is_file($p)) {
        return $p;
    }
    $p = realpath(__DIR__ . '/../../modules/openlog.so');
    return $p ?: '';
}

function ol_tmp($suffix)
{
    return sys_get_temp_dir() . '/ol-' . getmypid() . '-' . bin2hex(random_bytes(4)) . $suffix;
}

/**
 * Runs $code. Options:
 *   ini     => [k => v]      extra -d settings
 *   cgi     => true          run with php-cgi (web SAPI); server => [VAR => value] request environment
 *   repeat  => n             php-cgi -T n (n requests in one process)
 *   env     => [k => v]      process environment
 *   args    => [..]          script arguments (CLI)
 *   socket  => false         no receiver (transport points to a missing socket)
 */
function ol_run($code, array $opts = [])
{
    $sock = ol_tmp('.sock');
    $server = null;
    if (!isset($opts['socket']) || $opts['socket'] !== false) {
        $server = stream_socket_server('udg://' . $sock, $errno, $errstr, STREAM_SERVER_BIND);
        if (!$server) {
            throw new RuntimeException("receiver: $errstr");
        }
        stream_set_blocking($server, false);
    }
    $script = ol_tmp('.php');
    file_put_contents($script, $code);

    $ini = [
        'openlog.transport' => 'unix://' . $sock,
        'display_errors' => '1',
        'log_errors' => '0',
        'error_reporting' => (string) E_ALL,
        'html_errors' => '0',
        'opcache.enable_cli' => '0',
    ];
    /* compatibility runs (build/Dockerfile.compat, e.g. JIT): "key=value;key=value", below the per-test settings */
    $extra = getenv('OPENLOG_TEST_INI');
    if ($extra) {
        foreach (explode(';', $extra) as $kv) {
            if (strpos($kv, '=') !== false) {
                list($k, $v) = explode('=', $kv, 2);
                $ini[trim($k)] = trim($v);
            }
        }
    }
    if (isset($opts['ini'])) {
        foreach ($opts['ini'] as $k => $v) {
            $ini[$k] = $v;
        }
    }
    $cgi = !empty($opts['cgi']);
    $bin = $cgi ? dirname(PHP_BINARY) . '/php-cgi' : PHP_BINARY;
    $cmd = [$bin];
    if ($cgi) {
        $cmd[] = '-q';
        if (!empty($opts['repeat'])) {
            $cmd[] = '-T';
            $cmd[] = (string) $opts['repeat'];
        }
    }
    $cmd[] = '-d';
    $cmd[] = 'extension=' . ol_ext_so();
    foreach ($ini as $k => $v) {
        $cmd[] = '-d';
        $cmd[] = $k . '=' . $v;
    }
    $cmd[] = $script;
    if (!$cgi && isset($opts['args'])) {
        foreach ($opts['args'] as $a) {
            $cmd[] = $a;
        }
    }

    $env = getenv();
    unset($env['OPENLOG_SERVICE_NAME'], $env['TRACEPARENT'], $env['TRACESTATE']);
    if ($cgi) {
        $srv = [
            'REDIRECT_STATUS' => '200',
            'GATEWAY_INTERFACE' => 'CGI/1.1',
            'REQUEST_METHOD' => 'GET',
            'REQUEST_URI' => '/',
            'SCRIPT_FILENAME' => $script,
            'SCRIPT_NAME' => '/index.php',
            'SERVER_PROTOCOL' => 'HTTP/1.1',
            'HTTP_HOST' => 'shop.test:8080',
            'REMOTE_ADDR' => '10.1.2.3',
            'HTTP_USER_AGENT' => 'phpt',
        ];
        if (isset($opts['server'])) {
            foreach ($opts['server'] as $k => $v) {
                $srv[$k] = $v;
            }
        }
        foreach ($srv as $k => $v) {
            $env[$k] = $v;
        }
        if (!isset($srv['QUERY_STRING']) && strpos($srv['REQUEST_URI'], '?') !== false) {
            $env['QUERY_STRING'] = substr($srv['REQUEST_URI'], strpos($srv['REQUEST_URI'], '?') + 1);
        }
    }
    if (isset($opts['env'])) {
        foreach ($opts['env'] as $k => $v) {
            $env[$k] = $v;
        }
    }

    $outf = ol_tmp('.out');
    $errf = ol_tmp('.err');
    $cmdline = implode(' ', array_map('escapeshellarg', $cmd));
    $proc = proc_open($cmdline, [0 => ['pipe', 'r'], 1 => ['file', $outf, 'w'], 2 => ['file', $errf, 'w']], $pipes, null, $env);
    fclose($pipes[0]);
    $raw = [];
    $deadline = microtime(true) + (isset($opts['timeout']) ? $opts['timeout'] : 60);
    $exit = -1;
    while (true) {
        $st = proc_get_status($proc);
        if ($server) {
            $r = [$server];
            $w = null;
            $e = null;
            if (@stream_select($r, $w, $e, 0, 2000) > 0) {
                while (($d = stream_socket_recvfrom($server, 65536)) !== false && $d !== '') {
                    $raw[] = $d;
                }
            }
        } else {
            usleep(2000);
        }
        if (!$st['running']) {
            $exit = $st['exitcode'];
            break;
        }
        if (microtime(true) > $deadline) {
            proc_terminate($proc, 9);
            break;
        }
    }
    proc_close($proc);
    if ($server) {
        usleep(20000);
        while (($d = stream_socket_recvfrom($server, 65536)) !== false && $d !== '') {
            $raw[] = $d;
        }
        fclose($server);
        @unlink($sock);
    }
    $out = (string) @file_get_contents($outf);
    if ($cgi) {
        /* php-cgi prints the response headers of every request */
        $out = preg_replace('/(^|\n)(?:(?:Status|X-Powered-By|Content-type|Content-Type|Location|Set-Cookie|X-[A-Za-z-]+): [^\n]*\r?\n)+\r?\n/', '$1', $out);
    }
    $err = (string) @file_get_contents($errf);
    @unlink($outf);
    @unlink($errf);
    @unlink($script);

    $msgs = [];
    $invalid = [];
    foreach ($raw as $i => $d) {
        $m = json_decode($d, true);
        if (!is_array($m)) {
            $invalid[] = "datagram $i: invalid JSON";
            continue;
        }
        foreach (ol_validate($d, $m) as $problem) {
            $invalid[] = "datagram $i: $problem";
        }
        $msgs[] = $m;
    }
    return ['raw' => $raw, 'msgs' => $msgs, 'out' => $out, 'err' => $err, 'exit' => $exit, 'invalid' => $invalid];
}

/* The forwarder's validation rules (php-agent.md §6). */
function ol_validate($d, array $m)
{
    $p = [];
    if (strlen($d) > 60000) $p[] = 'datagram longer than 60000 bytes';
    if (!isset($m['v']) || $m['v'] !== 1) $p[] = 'v must be 1';
    if (!isset($m['pid']) || !is_int($m['pid']) || $m['pid'] <= 0) $p[] = 'pid';
    if (!isset($m['trace_id']) || !preg_match('/^[0-9a-f]{32}$/', $m['trace_id']) || $m['trace_id'] === str_repeat('0', 32)) $p[] = 'trace_id';
    if (!isset($m['seq']) || !is_int($m['seq']) || $m['seq'] < 0 || $m['seq'] > 255) $p[] = 'seq';
    if (!isset($m['last']) || !is_bool($m['last'])) $p[] = 'last';
    if (!isset($m['resource']) || !is_array($m['resource'])) {
        $p[] = 'resource';
    } else {
        foreach ($m['resource'] as $k => $v) {
            if (!is_string($v) || strlen($v) > 4096) $p[] = "resource $k";
        }
    }
    if (isset($m['sampling_ratio']) && !(is_float($m['sampling_ratio']) || is_int($m['sampling_ratio']))) $p[] = 'sampling_ratio';
    $ids = [];
    foreach (isset($m['spans']) ? $m['spans'] : [] as $i => $s) {
        if (!preg_match('/^[0-9a-f]{16}$/', isset($s['id']) ? $s['id'] : '')) $p[] = "span $i id";
        if (isset($ids[$s['id']])) $p[] = "span $i duplicate id";
        $ids[$s['id']] = true;
        if (!isset($s['parent']) || ($s['parent'] !== '' && !preg_match('/^[0-9a-f]{16}$/', $s['parent']))) $p[] = "span $i parent";
        if (!isset($s['name']) || !is_string($s['name']) || $s['name'] === '' || strlen($s['name']) > 4096) $p[] = "span $i name";
        if (!isset($s['kind']) || $s['kind'] < 1 || $s['kind'] > 5) $p[] = "span $i kind";
        if (!isset($s['status']) || $s['status'] < 0 || $s['status'] > 2) $p[] = "span $i status";
        if (!isset($s['start']) || !is_int($s['start']) || $s['start'] <= 0) $p[] = "span $i start";
        if (!isset($s['dur']) || !is_int($s['dur']) || $s['dur'] < 0) $p[] = "span $i dur";
        if (isset($s['status_msg']) && strlen($s['status_msg']) > 4096) $p[] = "span $i status_msg";
        $attrsets = [isset($s['attrs']) ? $s['attrs'] : []];
        foreach (isset($s['events']) ? $s['events'] : [] as $e) {
            $attrsets[] = isset($e['attrs']) ? $e['attrs'] : [];
            if (!isset($e['name']) || $e['name'] === '') $p[] = "span $i event name";
        }
        foreach ($attrsets as $attrs) {
            if (count($attrs) > 128) $p[] = "span $i more than 128 attributes";
            foreach ($attrs as $k => $v) {
                if (strlen($k) < 1 || strlen($k) > 256) $p[] = "span $i attr key";
                if ($v === null || (is_array($v) && !array_key_exists(0, $v) && $v !== [])) $p[] = "span $i attr $k type";
                if (is_string($v) && strlen($v) > 4096) $p[] = "span $i attr $k longer than 4 KiB";
                if (is_string($v) && !preg_match('//u', $v)) $p[] = "span $i attr $k invalid UTF-8";
            }
        }
    }
    return $p;
}

/* Reassembles messages into traces: [trace_id => [resource, spans, parts, last, function_trace, dropped_spans, sampling_ratio]] */
function ol_traces(array $msgs)
{
    $t = [];
    foreach ($msgs as $m) {
        $id = $m['trace_id'];
        if (!isset($t[$id])) {
            $t[$id] = ['resource' => $m['resource'], 'spans' => [], 'parts' => [], 'last' => false,
                'function_trace' => $m['function_trace'], 'dropped_spans' => 0, 'sampling_ratio' => $m['sampling_ratio']];
        }
        $t[$id]['parts'][] = $m['seq'];
        $t[$id]['last'] = $t[$id]['last'] || $m['last'];
        $t[$id]['dropped_spans'] = max($t[$id]['dropped_spans'], $m['dropped_spans']);
        foreach ($m['spans'] as $s) {
            $t[$id]['spans'][] = $s;
        }
    }
    return $t;
}

function ol_one_trace(array $r)
{
    foreach ($r['invalid'] as $problem) {
        echo "INVALID: $problem\n";
    }
    $t = ol_traces($r['msgs']);
    if (count($t) !== 1) {
        echo 'expected 1 trace, got ' . count($t) . "\n" . $r['out'] . $r['err'];
        return null;
    }
    return reset($t);
}

function ol_root(array $trace)
{
    $ids = [];
    foreach ($trace['spans'] as $s) {
        $ids[$s['id']] = true;
    }
    foreach ($trace['spans'] as $s) {
        if ($s['parent'] === '' || !isset($ids[$s['parent']])) {
            return $s;
        }
    }
    return null;
}

function ol_find(array $trace, $name)
{
    $found = [];
    foreach ($trace['spans'] as $s) {
        if ($s['name'] === $name) {
            $found[] = $s;
        }
    }
    return $found;
}

/* Prints the span tree: name, kind, status and the listed attributes (sorted children by start). */
function ol_print_tree(array $trace, array $attrs = [], $events = true)
{
    $children = [];
    $ids = [];
    foreach ($trace['spans'] as $s) {
        $ids[$s['id']] = true;
    }
    $roots = [];
    foreach ($trace['spans'] as $s) {
        if ($s['parent'] === '' || !isset($ids[$s['parent']])) {
            $roots[] = $s;
        } else {
            $children[$s['parent']][] = $s;
        }
    }
    $print = function ($s, $depth) use (&$print, &$children, $attrs, $events) {
        $line = str_repeat('  ', $depth) . $s['name'] . ' kind=' . $s['kind'] . ' status=' . $s['status'];
        foreach ($attrs as $a) {
            if (array_key_exists($a, $s['attrs'])) {
                $line .= ' ' . $a . '=' . json_encode($s['attrs'][$a], JSON_UNESCAPED_SLASHES);
            }
        }
        echo $line, "\n";
        if ($events && !empty($s['events'])) {
            foreach ($s['events'] as $e) {
                echo str_repeat('  ', $depth + 1), '! ', $e['name'], ' ', isset($e['attrs']['exception.type']) ? $e['attrs']['exception.type'] : '',
                    ': ', isset($e['attrs']['exception.message']) ? $e['attrs']['exception.message'] : '', "\n";
            }
        }
        $kids = isset($children[$s['id']]) ? $children[$s['id']] : [];
        usort($kids, function ($a, $b) { return $a['start'] <=> $b['start'] ?: strcmp($a['name'], $b['name']); });
        foreach ($kids as $k) {
            $print($k, $depth + 1);
        }
    };
    foreach ($roots as $r) {
        $print($r, 0);
    }
}

/* Built-in web server echoing the trace headers it received (tests/inc/http_router.php). */
function ol_http_server()
{
    static $srv = null;
    if ($srv !== null) {
        return $srv;
    }
    for ($try = 0; $try < 20; $try++) {
        $port = random_int(20000, 45000);
        $cmd = escapeshellarg(PHP_BINARY) . ' -n -S 127.0.0.1:' . $port . ' ' . escapeshellarg(__DIR__ . '/http_router.php');
        $proc = proc_open($cmd, [0 => ['pipe', 'r'], 1 => ['file', '/dev/null', 'w'], 2 => ['file', '/dev/null', 'w']], $pipes);
        for ($i = 0; $i < 100; $i++) {
            $fp = @fsockopen('127.0.0.1', $port, $en, $es, 0.1);
            if ($fp) {
                fclose($fp);
                $srv = ['proc' => $proc, 'port' => $port, 'url' => 'http://127.0.0.1:' . $port];
                register_shutdown_function(function () use ($proc) {
                    proc_terminate($proc, 9);
                });
                return $srv;
            }
            usleep(20000);
        }
        proc_terminate($proc, 9);
    }
    throw new RuntimeException('cannot start http server');
}

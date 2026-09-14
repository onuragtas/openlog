--TEST--
Long-running workers (stubbed Laravel Octane and RoadRunner classes): one trace per request, no process transaction, nothing between requests, traceparent, status codes, connection attributes kept
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; if (!extension_loaded('pdo_sqlite')) die('skip pdo_sqlite'); ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';

function show(array $r, array $attrs)
{
    foreach ($r['invalid'] as $p) {
        echo "INVALID: $p\n";
    }
    $traces = ol_traces($r['msgs']);
    echo count($traces), " traces\n";
    foreach ($traces as $id => $t) {
        ol_print_tree($t, $attrs);
        $root = ol_root($t);
        echo "  trace=", preg_match('/^(1{32}|4bf92f3577b34da6a3ce929d0e0e4736)$/', $id) ? 'continued' : 'new',
            " parent=", $root['parent'] === '' ? '-' : $root['parent'], "\n";
    }
}

$octane = '<?php
namespace Symfony\Component\HttpFoundation {
    class ParameterBag { protected $parameters; public function __construct(array $p = []) { $this->parameters = $p; } }
    class ServerBag extends ParameterBag {}
    class HeaderBag {
        protected $headers = [];
        public function __construct(array $h = []) { foreach ($h as $k => $v) { $this->headers[strtolower($k)] = [$v]; } }
    }
    class Request {
        public $server; public $headers; public $uri;
        public function __construct(array $server, array $headers) {
            $this->server = new ServerBag($server); $this->headers = new HeaderBag($headers); $this->uri = $server["REQUEST_URI"];
        }
    }
    class Response { protected $statusCode; public function __construct($code) { $this->statusCode = $code; } }
}
namespace Illuminate\Routing {
    class Route { public $uri; public function __construct($uri) { $this->uri = $uri; } }
    class Router { public function runRoute($request, $route) { return 1; } }
}
namespace Laravel\Octane {
    class RequestContext {}
    class OctaneResponse { public $response; public function __construct($r) { $this->response = $r; } }
    class Worker {
        public $client; public $pdo;
        public function handle($request, RequestContext $context) {
            if (strpos($request->uri, "/users/") === 0) {
                (new \Illuminate\Routing\Router())->runRoute($request, new \Illuminate\Routing\Route("users/{id}"));
            }
            $this->pdo->query("SELECT 1");
            $code = strpos($request->uri, "missing") ? 404 : 200;
            $this->client->respond($context, new OctaneResponse(new \Symfony\Component\HttpFoundation\Response($code)));
        }
    }
}
namespace Laravel\Octane\Swoole {
    class SwooleClient { public function respond($context, $octaneResponse) {} }
}
namespace {
    $pdo = new PDO("sqlite::memory:");
    $pdo->exec("CREATE TABLE t (id INTEGER)"); /* worker boot: part of the process transaction, never sent */
    $worker = new Laravel\Octane\Worker();
    $worker->client = new Laravel\Octane\Swoole\SwooleClient();
    $worker->pdo = $pdo;
    $requests = [
        ["/users/7?x=1", []],
        ["/missing", ["traceparent" => "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"]],
        ["/users/8", []],
    ];
    foreach ($requests as list($uri, $headers)) {
        $server = ["REQUEST_METHOD" => "GET", "REQUEST_URI" => $uri, "HTTP_HOST" => "octane.test:8000", "REMOTE_ADDR" => "10.0.0.9",
            "SERVER_PROTOCOL" => "HTTP/1.1"];
        foreach ($headers as $k => $v) { $server["HTTP_" . strtoupper($k)] = $v; }
        $worker->handle(new Symfony\Component\HttpFoundation\Request($server, $headers), new Laravel\Octane\RequestContext());
        $pdo->query("SELECT 99"); /* between requests: not recorded */
        echo "between: trace_id=\"", openlog\trace_id(), "\" sampled=", var_export(openlog\is_sampled(), true), "\n";
    }
}
';
echo "== Octane\n";
$r = ol_run($octane);
echo $r['out'], $r['err'];
show($r, ['http.response.status_code', 'url.path', 'server.address', 'server.port', 'db.namespace']);

$rr = '<?php
namespace Spiral\RoadRunner\Http {
    class Request { public $remoteAddr = "127.0.0.1"; public $protocol = "HTTP/1.1"; public $method; public $uri; public $headers = []; }
    class HttpWorker {
        private $queue;
        public function __construct(array $q) { $this->queue = $q; }
        public function waitRequest() {
            if (!$this->queue) { return null; }
            list($m, $u, $h) = array_shift($this->queue);
            $r = new Request(); $r->method = $m; $r->uri = $u; $r->headers = $h;
            return $r;
        }
        public function respond($status, $body = "", $headers = [], $endOfStream = true) {}
    }
}
namespace {
    $pdo = new PDO("sqlite::memory:");
    $w = new Spiral\RoadRunner\Http\HttpWorker([
        ["POST", "https://rr.test/orders/123?z=1", ["Traceparent" => ["00-11111111111111111111111111111111-2222222222222222-01"], "User-Agent" => ["rr-client"]]],
        ["GET", "http://rr.test:8080/health", []],
        ["GET", "http://rr.test:8080/unanswered", []],
    ]);
    while ($req = $w->waitRequest()) {
        $pdo->query("SELECT 2");
        if ($req->uri !== "http://rr.test:8080/unanswered") {
            $w->respond($req->method === "POST" ? 201 : 200, "ok");
        }
        $pdo->query("SELECT 3"); /* after respond(): not part of the request */
    }
}
';
echo "== RoadRunner\n";
$r = ol_run($rr);
echo $r['out'], $r['err'];
show($r, ['http.response.status_code', 'url.path', 'url.scheme', 'server.address', 'server.port', 'user_agent.original', 'db.query.text']);
--EXPECT--
== Octane
between: trace_id="" sampled=false
between: trace_id="" sampled=false
between: trace_id="" sampled=false
3 traces
GET /users/{id} kind=2 status=0 http.response.status_code=200 url.path="/users/7" server.address="octane.test" server.port=8000
  SELECT kind=3 status=0 db.namespace=":memory:"
  trace=new parent=-
GET /missing kind=2 status=0 http.response.status_code=404 url.path="/missing" server.address="octane.test" server.port=8000
  SELECT kind=3 status=0 db.namespace=":memory:"
  trace=continued parent=00f067aa0ba902b7
GET /users/{id} kind=2 status=0 http.response.status_code=200 url.path="/users/8" server.address="octane.test" server.port=8000
  SELECT kind=3 status=0 db.namespace=":memory:"
  trace=new parent=-
== RoadRunner
3 traces
POST /orders/{id} kind=2 status=0 http.response.status_code=201 url.path="/orders/123" url.scheme="https" server.address="rr.test" user_agent.original="rr-client"
  SELECT kind=3 status=0 db.query.text="SELECT ?"
  trace=continued parent=2222222222222222
GET /health kind=2 status=0 http.response.status_code=200 url.path="/health" url.scheme="http" server.address="rr.test" server.port=8080
  SELECT kind=3 status=0 db.query.text="SELECT ?"
  trace=new parent=-
GET /unanswered kind=2 status=0 url.path="/unanswered" url.scheme="http" server.address="rr.test" server.port=8080
  SELECT kind=3 status=0 db.query.text="SELECT ?"
  SELECT kind=3 status=0 db.query.text="SELECT ?"
  trace=new parent=-

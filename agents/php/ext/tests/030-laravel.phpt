--TEST--
Laravel (stubbed classes): route template naming, report()/reportThrowable(), internal dontReport, no magic __get
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; if (!is_executable(dirname(PHP_BINARY) . '/php-cgi')) die('skip no php-cgi'); ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$stubs = '<?php
namespace Illuminate\Routing {
    class Route { public $uri; public function __construct($uri) { $this->uri = $uri; } }
    class Router { public function runRoute($request, $route) { return "ran"; } }
}
namespace Illuminate\Validation { class ValidationException extends \Exception {} }
';
$modern = $stubs . '
namespace Illuminate\Foundation\Exceptions {
    class Handler {
        public function report(\Throwable $e) { if (!($e instanceof \Illuminate\Validation\ValidationException)) { $this->reportThrowable($e); } }
        protected function reportThrowable(\Throwable $e) {}
    }
}
';
$legacy = $stubs . '
namespace Illuminate\Foundation\Exceptions { class Handler { public function report(\Exception $e) {} } }
';
$app = '
namespace App {
    class MagicRoute { public function __get($n) { echo "MAGIC CALLED\n"; return "magic"; } }
    class H extends \Illuminate\Foundation\Exceptions\Handler {}
    $router = new \Illuminate\Routing\Router();
    echo $router->runRoute(null, new MagicRoute()), "\n";
    echo $router->runRoute(null, new \Illuminate\Routing\Route("users/{id}/orders")), "\n";
    $h = new H();
    $h->report(new \Illuminate\Validation\ValidationException("invalid"));
    $h->report(new \RuntimeException("reported"));
    $h->report(new \RuntimeException("reported"));
}
';
foreach (['modern' => $modern, 'legacy' => $legacy] as $label => $code) {
    $r = ol_run($code . $app, ['cgi' => true, 'server' => ['REQUEST_URI' => '/users/42/orders?page=2']]);
    echo "== $label\n", $r['out'];
    $t = ol_one_trace($r);
    $root = ol_root($t);
    echo $root['name'], " route=", $root['attrs']['http.route'], " status=", $root['status'], "\n";
    foreach ($root['events'] as $e) echo "  ", $e['attrs']['exception.type'], ": ", $e['attrs']['exception.message'], "\n";
}
--EXPECT--
== modern
ran
ran
GET /users/{id}/orders route=/users/{id}/orders status=2
  RuntimeException: reported
  RuntimeException: reported
== legacy
ran
ran
GET /users/{id}/orders route=/users/{id}/orders status=2
  RuntimeException: reported
  RuntimeException: reported

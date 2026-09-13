--TEST--
Symfony (stubbed classes): path template from route params, sub-request ignored, kernel exception (5xx only)
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; if (!is_executable(dirname(PHP_BINARY) . '/php-cgi')) die('skip no php-cgi'); ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$code = '<?php
namespace Symfony\Component\HttpFoundation {
    class ParameterBag { protected $parameters; public function __construct(array $p = []) { $this->parameters = $p; } }
    class Request {
        public $attributes; protected $pathInfo;
        public function __construct($path, array $attrs) { $this->pathInfo = $path; $this->attributes = new ParameterBag($attrs); }
    }
}
namespace Symfony\Component\HttpKernel\Controller {
    class ControllerResolver { public function getController($request) { return "controller"; } }
    class ContainerControllerResolver extends ControllerResolver {}
}
namespace Symfony\Component\HttpKernel\Exception {
    interface HttpExceptionInterface {}
    class HttpException extends \RuntimeException implements HttpExceptionInterface {
        private $statusCode;
        public function __construct($statusCode, $message = "") { $this->statusCode = $statusCode; parent::__construct($message); }
    }
    class NotFoundHttpException extends HttpException { public function __construct() { parent::__construct(404, "no route"); } }
}
namespace Symfony\Component\HttpKernel {
    class HttpKernel { public function handleThrowable(\Throwable $e, $request, $type) { return null; } }
}
namespace {
    use Symfony\Component\HttpFoundation\Request;
    use Symfony\Component\HttpKernel\Controller\ContainerControllerResolver;
    $resolver = new ContainerControllerResolver();
    echo $resolver->getController(new Request("/api/products/42/reviews/7-x", [
        "_route" => "product_review",
        "_route_params" => ["id" => "42", "rid" => 7, "_locale" => "en"],
        "_controller" => "App\\\\Controller\\\\ReviewController::show",
    ])), "\n";
    $resolver->getController(new Request("/_fragment", ["_route" => "fragment", "_route_params" => []]));
    $kernel = new Symfony\Component\HttpKernel\HttpKernel();
    $kernel->handleThrowable(new Symfony\Component\HttpKernel\Exception\NotFoundHttpException(), null, 1);
    $kernel->handleThrowable(new Symfony\Component\HttpKernel\Exception\HttpException(502, "bad gateway"), null, 1);
    $kernel->handleThrowable(new LogicException("boom"), null, 1);
}
';
$r = ol_run($code, ['cgi' => true, 'server' => ['REQUEST_URI' => '/api/products/42/reviews/7-x']]);
echo $r['out'];
$t = ol_one_trace($r);
$root = ol_root($t);
echo $root['name'], " route=", $root['attrs']['http.route'], " name=", $root['attrs']['openlog.php.route_name'], "\n";
foreach ($root['events'] as $e) echo "  ", $e['attrs']['exception.type'], ": ", $e['attrs']['exception.message'], "\n";
--EXPECT--
controller
GET /api/products/{id}/reviews/{rid}-x route=/api/products/{id}/reviews/{rid}-x name=product_review
  Symfony\Component\HttpKernel\Exception\HttpException: bad gateway
  LogicException: boom

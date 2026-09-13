<?php

// Minimal Symfony 7.4 HttpKernel application (no FrameworkBundle: several Symfony packages the full skeleton
// needs are currently blocked by Packagist security advisories). The request path is the real
// Symfony\Component\HttpKernel\HttpKernel::handle/handleRaw with RouterListener, which is what both agents hook.

use App\Controller\ProductController;
use Symfony\Component\EventDispatcher\EventDispatcher;
use Symfony\Component\HttpClient\HttpClient;
use Symfony\Component\HttpFoundation\JsonResponse;
use Symfony\Component\HttpFoundation\Request;
use Symfony\Component\HttpFoundation\RequestStack;
use Symfony\Component\HttpKernel\Controller\ArgumentResolver;
use Symfony\Component\HttpKernel\Controller\ContainerControllerResolver;
use Symfony\Component\HttpKernel\Event\ExceptionEvent;
use Symfony\Component\HttpKernel\EventListener\RouterListener;
use Symfony\Component\HttpKernel\HttpKernel;
use Symfony\Component\HttpKernel\KernelEvents;
use Symfony\Component\Routing\Matcher\UrlMatcher;
use Symfony\Component\Routing\RequestContext;
use Symfony\Component\Routing\Route;
use Symfony\Component\Routing\RouteCollection;

require __DIR__ . '/../vendor/autoload.php';

$routes = new RouteCollection();
$routes->add('product_show', new Route('/api/products/{id}', ['_controller' => 'App\Controller\ProductController::show'], ['id' => '\d+'], methods: ['GET']));
$routes->add('product_external', new Route('/api/external', ['_controller' => 'App\Controller\ProductController::external'], methods: ['GET']));
$routes->add('product_boom', new Route('/api/boom', ['_controller' => 'App\Controller\ProductController::boom'], methods: ['GET']));
$routes->add('health', new Route('/health', ['_controller' => 'App\Controller\ProductController::health']));

$requestStack = new RequestStack();
$dispatcher = new EventDispatcher();
$dispatcher->addSubscriber(new RouterListener(new UrlMatcher($routes, new RequestContext()), $requestStack));
$dispatcher->addListener(KernelEvents::EXCEPTION, function (ExceptionEvent $event) {
    $e = $event->getThrowable();
    error_log('[symfony] ' . $e::class . ': ' . $e->getMessage());
    $code = $e instanceof \Symfony\Component\HttpKernel\Exception\HttpExceptionInterface ? $e->getStatusCode() : 500;
    $event->setResponse(new JsonResponse(['error' => $e::class], $code));
});

// Tiny container: the controller is built once per request with a shared HTTP client.
$container = new class implements \Psr\Container\ContainerInterface {
    private array $services = [];
    public function get(string $id): mixed
    {
        return $this->services[$id] ??= new ProductController(HttpClient::create(['timeout' => 2]));
    }
    public function has(string $id): bool
    {
        return $id === ProductController::class;
    }
};

$kernel = new HttpKernel($dispatcher, new ContainerControllerResolver($container), $requestStack, new ArgumentResolver());
$request = Request::createFromGlobals();
$response = $kernel->handle($request);
$response->send();
$kernel->terminate($request, $response);

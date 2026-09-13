<?php

declare(strict_types=1);

use App\Catalog;
use App\RedisCache;
use OpenTelemetry\API\Trace\LocalRootSpan;
use OpenTelemetry\API\Trace\StatusCode;
use Psr\Http\Message\ResponseInterface as Response;
use Psr\Http\Message\ServerRequestInterface as Request;
use Slim\Exception\HttpException;
use Slim\Factory\AppFactory;

require __DIR__ . '/../vendor/autoload.php';

$json = static function (Response $response, mixed $data, int $status = 200): Response {
    $response->getBody()->write(json_encode($data, JSON_THROW_ON_ERROR));

    return $response->withStatus($status)->withHeader('Content-Type', 'application/json');
};

$catalog = new Catalog(new RedisCache(getenv('REDIS_HOST') ?: 'redis', (int) (getenv('REDIS_PORT') ?: 6379)));

$app = AppFactory::create();
$app->addRoutingMiddleware();

$errorMiddleware = $app->addErrorMiddleware(false, false, false);
$errorMiddleware->setDefaultErrorHandler(
    static function (Request $request, Throwable $exception) use ($app, $json): Response {
        $status = $exception instanceof HttpException ? $exception->getCode() : 500;
        if ($status >= 500) {
            // Slim's error middleware swallows the exception before the auto-instrumentation
            // hook sees it: record it on the SERVER span explicitly.
            $span = LocalRootSpan::current();
            $span->recordException($exception);
            $span->setStatus(StatusCode::STATUS_ERROR, $exception->getMessage());
            error_log(sprintf('%s %s failed: %s', $request->getMethod(), $request->getUri()->getPath(), $exception));
        }

        return $json($app->getResponseFactory()->createResponse(), ['error' => $exception->getMessage()], $status);
    }
);

$app->get('/healthz', static fn (Request $request, Response $response): Response => $json($response, ['status' => 'ok']));

$app->get('/products', static fn (Request $request, Response $response): Response => $json($response, $catalog->all()));

$app->get('/products/{id}', static function (Request $request, Response $response, array $args) use ($catalog, $json): Response {
    $product = $catalog->find((int) $args['id']);
    if ($product === null) {
        return $json($response, ['error' => 'product not found'], 404);
    }

    return $json($response, $product);
});

$app->run();

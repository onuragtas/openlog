<?php

declare(strict_types=1);

namespace App;

use OpenTelemetry\API\Globals;
use OpenTelemetry\API\Trace\SpanKind;
use OpenTelemetry\API\Trace\StatusCode;
use Redis;
use Throwable;

/**
 * ext-redis wrapper with manual CLIENT spans: there is no official OpenTelemetry
 * auto-instrumentation for ext-redis/predis on Packagist.
 */
final class RedisCache
{
    private ?Redis $redis = null;

    public function __construct(
        private readonly string $host,
        private readonly int $port,
    ) {
    }

    public function get(string $key): ?string
    {
        $value = $this->traced('GET', "GET {$key}", fn (Redis $r) => $r->get($key));

        return is_string($value) ? $value : null;
    }

    public function setex(string $key, int $ttl, string $value): void
    {
        // The value is not part of the statement (size, and it is data, not the command shape).
        $this->traced('SETEX', "SETEX {$key} {$ttl} ?", fn (Redis $r) => $r->setex($key, $ttl, $value));
    }

    private function connection(): Redis
    {
        if ($this->redis === null) {
            $redis = new Redis();
            $redis->connect($this->host, $this->port, 1.0);
            $this->redis = $redis;
        }

        return $this->redis;
    }

    private function traced(string $operation, string $statement, callable $fn): mixed
    {
        $span = Globals::tracerProvider()
            ->getTracer('openlog-apmdemo/catalog-redis', '1.0.0', 'https://opentelemetry.io/schemas/1.30.0')
            ->spanBuilder($operation)
            ->setSpanKind(SpanKind::KIND_CLIENT)
            ->setAttribute('db.system', 'redis')
            ->setAttribute('db.system.name', 'redis')
            ->setAttribute('db.statement', $statement)
            ->setAttribute('db.query.text', $statement)
            ->setAttribute('db.operation', $operation)
            ->setAttribute('db.operation.name', $operation)
            ->setAttribute('server.address', $this->host)
            ->setAttribute('server.port', $this->port)
            ->startSpan();
        try {
            return $fn($this->connection());
        } catch (Throwable $e) {
            $span->recordException($e);
            $span->setStatus(StatusCode::STATUS_ERROR, $e->getMessage());
            throw $e;
        } finally {
            $span->end();
        }
    }
}

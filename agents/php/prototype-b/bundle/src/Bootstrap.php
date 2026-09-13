<?php

// SPDX-License-Identifier: Apache-2.0

declare(strict_types=1);

namespace Openlog\Php;

use Nyholm\Psr7\Factory\Psr17Factory;
use OpenTelemetry\API\Trace\Propagation\TraceContextPropagator;
use OpenTelemetry\Contrib\Otlp\SpanExporter;
use OpenTelemetry\SDK\Common\Attribute\Attributes;
use OpenTelemetry\SDK\Common\Export\Http\PsrTransportFactory;
use OpenTelemetry\SDK\Resource\ResourceInfo;
use OpenTelemetry\SDK\Resource\ResourceInfoFactory;
use OpenTelemetry\SDK\Sdk;
use OpenTelemetry\SDK\Trace\Sampler\AlwaysOnSampler;
use OpenTelemetry\SDK\Trace\Sampler\ParentBased;
use OpenTelemetry\SDK\Trace\SpanProcessor\BatchSpanProcessor;
use OpenTelemetry\SDK\Trace\TracerProvider;

/**
 * openlog "distro" bootstrap for the OpenTelemetry PHP SDK (spike, option B).
 *
 * Runs once per request from auto_prepend_file. Configuration comes from OPENLOG_* env vars so users never touch
 * composer.json. The exporter uses a tiny curl PSR-18 client so the bundle does not ship Guzzle/Symfony HttpClient,
 * which would collide with the application's own copies.
 */
final class Bootstrap
{
    private static mixed $scope = null;

    public static function start(): void
    {
        if (self::$scope !== null) {
            return;
        }
        $endpoint = rtrim((string) (getenv('OPENLOG_ENDPOINT') ?: 'http://localhost:4318'), '/') . '/v1/traces';
        $key = (string) (getenv('OPENLOG_LICENSE_KEY') ?: '');
        $service = (string) (getenv('OPENLOG_SERVICE_NAME') ?: 'php-app');

        $factory = new Psr17Factory();
        $transport = (new PsrTransportFactory(new CurlClient($factory), $factory, $factory))
            ->create($endpoint, 'application/x-protobuf', ['openlog-license-key' => $key], null, 2.0, 100, 1);

        $resource = ResourceInfoFactory::defaultResource()->merge(ResourceInfo::create(Attributes::create([
            'service.name' => $service,
            'telemetry.distro.name' => 'openlog-php-agent',
            'telemetry.distro.version' => '0.0.1-spike',
            'openlog.agent.name' => 'openlog-php-agent',
        ])));

        $tracerProvider = TracerProvider::builder()
            ->addSpanProcessor(BatchSpanProcessor::builder(new SpanExporter($transport))->build())
            ->setResource($resource)
            ->setSampler(new ParentBased(new AlwaysOnSampler()))
            ->build();

        self::$scope = Sdk::builder()
            ->setTracerProvider($tracerProvider)
            ->setPropagator(TraceContextPropagator::getInstance())
            ->setAutoShutdown(true)
            ->buildAndRegisterGlobal();
    }
}

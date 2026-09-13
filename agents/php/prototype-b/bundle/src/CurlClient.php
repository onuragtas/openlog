<?php

// SPDX-License-Identifier: Apache-2.0

declare(strict_types=1);

namespace Openlog\Php;

use Psr\Http\Client\ClientExceptionInterface;
use Psr\Http\Client\ClientInterface;
use Psr\Http\Message\RequestInterface;
use Psr\Http\Message\ResponseFactoryInterface;
use Psr\Http\Message\ResponseInterface;
use Psr\Http\Message\StreamFactoryInterface;

/** Minimal PSR-18 client on ext-curl, used only by the OTLP exporter. Short timeouts: exporting must not hang a worker. */
final class CurlClient implements ClientInterface
{
    public function __construct(private readonly ResponseFactoryInterface&StreamFactoryInterface $factory)
    {
    }

    public function sendRequest(RequestInterface $request): ResponseInterface
    {
        $headers = [];
        foreach ($request->getHeaders() as $name => $values) {
            foreach ($values as $value) {
                $headers[] = $name . ': ' . $value;
            }
        }
        $ch = curl_init((string) $request->getUri());
        curl_setopt_array($ch, [
            CURLOPT_CUSTOMREQUEST => $request->getMethod(),
            CURLOPT_POSTFIELDS => (string) $request->getBody(),
            CURLOPT_HTTPHEADER => $headers,
            CURLOPT_RETURNTRANSFER => true,
            CURLOPT_CONNECTTIMEOUT_MS => 500,
            CURLOPT_TIMEOUT_MS => 2000,
        ]);
        $body = curl_exec($ch);
        if ($body === false) {
            $error = curl_error($ch);
            curl_close($ch);
            throw new class ($error) extends \RuntimeException implements ClientExceptionInterface {
            };
        }
        $status = (int) curl_getinfo($ch, CURLINFO_RESPONSE_CODE);
        curl_close($ch);

        return $this->factory->createResponse($status)->withBody($this->factory->createStream((string) $body));
    }
}

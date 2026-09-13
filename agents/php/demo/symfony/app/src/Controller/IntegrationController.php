<?php

namespace App\Controller;

use App\Service\Reports\InventoryReport;
use Symfony\Bundle\FrameworkBundle\Controller\AbstractController;
use Symfony\Component\HttpFoundation\JsonResponse;
use Symfony\Component\Routing\Attribute\Route;
use Symfony\Contracts\HttpClient\HttpClientInterface;

final class IntegrationController extends AbstractController
{
    public function __construct(private readonly HttpClientInterface $http)
    {
    }

    // Symfony HttpClient (CurlHttpClient = curl_multi): concurrent requests to both Laravel demos.
    #[Route('/api/external', name: 'external', methods: ['GET'])]
    public function external(): JsonResponse
    {
        $targets = [
            'laravel-83' => (getenv('DEMO_LARAVEL83_URL') ?: 'http://nginx-laravel-83') . '/health',
            'laravel-74' => (getenv('DEMO_LARAVEL74_URL') ?: 'http://nginx-laravel-74') . '/health',
        ];
        $responses = [];
        foreach ($targets as $name => $url) {
            $responses[$name] = $this->http->request('GET', $url, ['timeout' => 3]);
        }
        $statuses = [];
        foreach ($this->http->stream($responses) as $response => $chunk) {
            try {
                if ($chunk->isLast()) {
                    $statuses[array_search($response, $responses, true)] = $response->getStatusCode();
                }
            } catch (\Throwable $e) {
                $statuses[array_search($response, $responses, true)] = $e::class;
            }
        }

        return $this->json(['statuses' => $statuses]);
    }

    // > 600 ms of nested service calls with PostgreSQL queries: function-level transaction trace.
    #[Route('/api/reports/{type}', name: 'report', requirements: ['type' => 'slow|inventory'], methods: ['GET'])]
    public function report(string $type, InventoryReport $report): JsonResponse
    {
        $started = microtime(true);
        $result = $report->build($type === 'slow' ? 30 : 10);

        return $this->json($result + ['elapsed_ms' => (int) ((microtime(true) - $started) * 1000)]);
    }
}

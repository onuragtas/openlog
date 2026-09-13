<?php

namespace App\Controller;

use Symfony\Component\HttpFoundation\JsonResponse;
use Symfony\Contracts\HttpClient\HttpClientInterface;

// Spike controller: PDO + Redis + outbound HTTP + an exception route. Routes are declared in public/index.php.
class ProductController
{
    private static ?\PDO $pdo = null;

    public function __construct(private HttpClientInterface $http)
    {
    }

    private function pdo(): \PDO
    {
        return self::$pdo ??= new \PDO('mysql:host=mariadb;dbname=spike', 'spike', 'spike', [\PDO::ATTR_ERRMODE => \PDO::ERRMODE_EXCEPTION]);
    }

    public function show(int $id): JsonResponse
    {
        $st = $this->pdo()->prepare('SELECT id, name, price FROM items WHERE id = ?');
        $st->execute([($id % 100) + 1]);
        $row = $st->fetch(\PDO::FETCH_ASSOC);

        $redis = new \Redis();
        $redis->connect('redis', 6379, 0.5);
        $redis->incr('products:hits');

        return new JsonResponse(['product' => $row, 'hits' => (int) $redis->get('products:hits')]);
    }

    public function external(): JsonResponse
    {
        $r = $this->http->request('GET', 'http://nginx-base/health');
        return new JsonResponse(['status' => $r->getStatusCode()]);
    }

    public function boom(): JsonResponse
    {
        throw new \LogicException('spike: symfony intentional failure');
    }

    public function health(): JsonResponse
    {
        return new JsonResponse('ok');
    }
}

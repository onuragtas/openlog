<?php

namespace App\Controller;

use App\Service\Cache\RedisStore;
use App\Service\Catalog\ProductRepository;
use Symfony\Bundle\FrameworkBundle\Controller\AbstractController;
use Symfony\Component\HttpFoundation\JsonResponse;
use Symfony\Component\HttpKernel\Exception\NotFoundHttpException;
use Symfony\Component\Routing\Attribute\Route;

final class ProductController extends AbstractController
{
    public function __construct(private readonly ProductRepository $products, private readonly RedisStore $redis)
    {
    }

    // PDO pgsql prepared statement + phpredis.
    #[Route('/api/products/{id}', name: 'product_show', requirements: ['id' => '\d+'], methods: ['GET'])]
    public function show(int $id): JsonResponse
    {
        $product = $this->products->find(($id % 100) + 1) ?? throw new NotFoundHttpException("product $id not found");
        $hits = $this->redis->hit('products:hits');

        return $this->json(['product' => $product, 'hits' => $hits]);
    }

    // pg_connect / pg_query_params (native pgsql functions).
    #[Route('/api/products/{id}/reviews', name: 'product_reviews', requirements: ['id' => '\d+'], methods: ['GET'])]
    public function reviews(int $id): JsonResponse
    {
        $productId = ($id % 100) + 1;

        return $this->json([
            'product' => $productId,
            'reviews' => $this->products->reviews($productId),
            'average' => $this->products->averageRating($productId),
        ]);
    }

    #[Route('/api/boom', name: 'boom', methods: ['GET'])]
    public function boom(): JsonResponse
    {
        throw new \LogicException('demo: intentional failure in ProductController::boom (kernel.exception -> 500)');
    }

    #[Route('/health', name: 'health', methods: ['GET'])]
    public function health(): JsonResponse
    {
        return $this->json('ok');
    }
}

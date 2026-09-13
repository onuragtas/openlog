<?php

namespace App\Service\Reports;

use App\Service\Cache\RedisStore;
use App\Service\Catalog\ProductRepository;

// Deliberately slow (> 600 ms for the "slow" report), nested userland calls for the transaction tracer.
final class InventoryReport
{
    public function __construct(
        private readonly ProductRepository $products,
        private readonly StockForecaster $forecaster,
        private readonly RedisStore $redis,
    ) {
    }

    public function build(int $size): array
    {
        $products = $this->loadProducts($size);
        $scored = $this->scoreProducts($products);
        $forecast = $this->forecaster->forecast($scored);
        $summary = $this->redis->remember('report:summary:' . intdiv(time(), 5), 5, fn () => $this->summarize($forecast));

        return ['products' => count($products), 'reorder' => $summary['reorder'], 'value' => $summary['value']];
    }

    private function loadProducts(int $size): array
    {
        $this->products->slowQuery(0.15);

        return $this->products->all($size);
    }

    private function scoreProducts(array $products): array
    {
        usleep(50000);
        foreach ($products as &$p) {
            $p['score'] = $this->score($p);
        }

        return $products;
    }

    private function score(array $product): float
    {
        return ((float) $product['price'] * 0.3) + ((int) $product['stock'] * 0.7);
    }

    private function summarize(array $forecast): array
    {
        usleep(60000);
        $value = 0.0;
        $reorder = 0;
        foreach ($forecast as $row) {
            $value += (float) $row['price'] * (int) $row['stock'];
            $reorder += $row['reorder'] ? 1 : 0;
        }

        return ['value' => round($value, 2), 'reorder' => $reorder];
    }
}

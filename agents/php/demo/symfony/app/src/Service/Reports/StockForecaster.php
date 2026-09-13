<?php

namespace App\Service\Reports;

final class StockForecaster
{
    public function forecast(array $products): array
    {
        $season = $this->seasonality();
        foreach ($products as &$p) {
            $p['reorder'] = $this->needsReorder($p, $season);
        }
        $this->simulateDemand($products);

        return $products;
    }

    private function seasonality(): float
    {
        usleep(120000);

        return 1.15;
    }

    private function needsReorder(array $product, float $season): bool
    {
        return (int) $product['stock'] * $season < 20;
    }

    private function simulateDemand(array $products): void
    {
        usleep(100000);
        $x = 0;
        for ($i = 0; $i < 400000; $i++) {
            $x = ($x * 31 + $i + count($products)) % 1000003;
        }
        $this->persistScenario($x);
    }

    private function persistScenario(int $seed): void
    {
        usleep(150000);
    }
}

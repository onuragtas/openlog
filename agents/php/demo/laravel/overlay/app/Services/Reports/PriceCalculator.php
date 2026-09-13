<?php

namespace App\Services\Reports;

class PriceCalculator
{
    // Fast (sub-millisecond) calls: aggregated into the parent's fast_calls counters by the tracer.
    public function lineTotal(float $price, int $quantity): float
    {
        return $price * $quantity * $this->taxRate();
    }

    public function applyDiscounts(float $sum, int $orders): float
    {
        usleep(30000);
        $discount = $this->loyaltyDiscount($orders);

        return $sum * (1 - $discount);
    }

    private function loyaltyDiscount(int $orders): float
    {
        // CPU-bound loop so the segment has real self time.
        $x = 0;
        for ($i = 0; $i < 150000; $i++) {
            $x = ($x + $i * $orders) % 9973;
        }

        return $orders > 15 ? 0.1 : 0.02;
    }

    private function taxRate(): float
    {
        return 1.19;
    }
}

<?php

namespace App\Services\Reports;

use Illuminate\Support\Facades\DB;

class OrderAggregator
{
    /** @var PriceCalculator */
    private $prices;

    public function __construct(PriceCalculator $prices)
    {
        $this->prices = $prices;
    }

    public function summarizeCustomer($user): array
    {
        $orders = DB::table('orders')
            ->join('items', 'items.id', '=', 'orders.item_id')
            ->where('orders.user_id', $user->id)
            ->select('orders.quantity', 'items.price')
            ->get();
        $sum = 0.0;
        foreach ($orders as $order) {
            $sum += $this->prices->lineTotal((float) $order->price, (int) $order->quantity);
        }
        $sum = $this->prices->applyDiscounts($sum, count($orders));

        return ['user' => $user->name, 'orders' => count($orders), 'sum' => round($sum, 2)];
    }

    public function grandTotal(array $rows): float
    {
        usleep(40000);
        $total = 0.0;
        foreach ($rows as $row) {
            $total += $row['sum'];
        }

        return round($total, 2);
    }
}

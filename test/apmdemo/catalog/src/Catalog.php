<?php

declare(strict_types=1);

namespace App;

use RuntimeException;

final class Catalog
{
    public const PRODUCT_COUNT = 20;

    private const NAMES = [
        'Espresso Beans', 'Pour-over Kettle', 'Ceramic Mug', 'French Press', 'Burr Grinder',
        'Milk Frother', 'Drip Scale', 'Paper Filters', 'Travel Tumbler', 'Cold Brew Jar',
        'Moka Pot', 'Tamper', 'Decaf Blend', 'Knock Box', 'Latte Glasses',
        'Chemex', 'AeroPress', 'Barista Apron', 'Descaler', 'Gift Card',
    ];

    public function __construct(private readonly RedisCache $cache)
    {
    }

    /** @return list<array<string, mixed>> */
    public function all(): array
    {
        $cached = $this->cache->get('catalog:all');
        if ($cached !== null) {
            return json_decode($cached, true, 512, JSON_THROW_ON_ERROR);
        }
        $products = array_map(self::build(...), range(1, self::PRODUCT_COUNT));
        $this->cache->setex('catalog:all', 30, json_encode($products, JSON_THROW_ON_ERROR));

        return $products;
    }

    /** @return array<string, mixed>|null */
    public function find(int $id): ?array
    {
        if ($id === 13) {
            throw new RuntimeException('Product 13 is discontinued');
        }
        if ($id < 1 || $id > self::PRODUCT_COUNT) {
            return null;
        }
        $key = "product:{$id}";
        $cached = $this->cache->get($key);
        if ($cached !== null) {
            return json_decode($cached, true, 512, JSON_THROW_ON_ERROR);
        }
        $product = self::build($id);
        $this->cache->setex($key, 60, json_encode($product, JSON_THROW_ON_ERROR));

        return $product;
    }

    /** @return array<string, mixed> */
    private static function build(int $id): array
    {
        return [
            'id' => $id,
            'name' => self::NAMES[($id - 1) % count(self::NAMES)],
            'price' => round(4.5 + (($id * 37) % 90) + ($id % 3) * 0.49, 2),
            'stock' => ($id * 11) % 40,
        ];
    }
}

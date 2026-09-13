<?php

namespace App\Service\Catalog;

use App\Service\Database\Postgres;

final class ProductRepository
{
    public function __construct(private readonly Postgres $db)
    {
    }

    public function find(int $id): ?array
    {
        $st = $this->db->pdo()->prepare('SELECT id, name, price, stock FROM products WHERE id = :id');
        $st->execute(['id' => $id]);

        return $st->fetch(\PDO::FETCH_ASSOC) ?: null;
    }

    public function all(int $limit): array
    {
        return $this->db->pdo()->query('SELECT id, name, price, stock FROM products ORDER BY id LIMIT ' . (int) $limit)
            ->fetchAll(\PDO::FETCH_ASSOC);
    }

    public function reviews(int $productId): array
    {
        $res = pg_query_params($this->db->native(), 'SELECT id, rating, body FROM reviews WHERE product_id = $1 ORDER BY id LIMIT 5', [$productId]);

        return pg_fetch_all($res) ?: [];
    }

    public function averageRating(int $productId): float
    {
        $res = pg_query($this->db->native(), 'SELECT AVG(rating) AS avg FROM reviews WHERE product_id = ' . $productId);

        return round((float) pg_fetch_result($res, 0, 0), 2);
    }

    public function slowQuery(float $seconds): void
    {
        pg_query($this->db->native(), sprintf('SELECT pg_sleep(%.2f)', $seconds));
    }
}

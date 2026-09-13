<?php

namespace App\Service\Database;

// One PDO and one native pgsql connection per FPM worker.
final class Postgres
{
    private static ?\PDO $pdo = null;
    /** @var resource|\PgSql\Connection|null */
    private static $native = null;

    public function pdo(): \PDO
    {
        return self::$pdo ??= new \PDO(
            sprintf('pgsql:host=%s;port=5432;dbname=%s', getenv('PG_HOST') ?: 'postgres', getenv('PG_DATABASE') ?: 'demo'),
            getenv('PG_USER') ?: 'demo',
            getenv('PG_PASSWORD') ?: 'demo',
            [\PDO::ATTR_ERRMODE => \PDO::ERRMODE_EXCEPTION, \PDO::ATTR_PERSISTENT => false],
        );
    }

    /** @return resource|\PgSql\Connection */
    public function native()
    {
        if (self::$native === null) {
            $dsn = sprintf('host=%s port=5432 dbname=%s user=%s password=%s connect_timeout=3',
                getenv('PG_HOST') ?: 'postgres', getenv('PG_DATABASE') ?: 'demo', getenv('PG_USER') ?: 'demo', getenv('PG_PASSWORD') ?: 'demo');
            self::$native = pg_connect($dsn) ?: throw new \RuntimeException('pg_connect failed');
        }

        return self::$native;
    }
}

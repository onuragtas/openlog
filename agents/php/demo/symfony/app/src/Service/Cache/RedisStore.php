<?php

namespace App\Service\Cache;

final class RedisStore
{
    private static ?\Redis $redis = null;

    private function redis(): \Redis
    {
        if (self::$redis === null) {
            $r = new \Redis();
            $r->pconnect(getenv('REDIS_HOST') ?: 'redis', 6379, 0.5);
            self::$redis = $r;
        }

        return self::$redis;
    }

    public function hit(string $key): int
    {
        $count = $this->redis()->incr($key);
        $this->redis()->expire($key, 3600);

        return (int) $count;
    }

    public function remember(string $key, int $ttl, callable $compute): mixed
    {
        $cached = $this->redis()->get($key);
        if ($cached !== false) {
            return unserialize($cached);
        }
        $value = $compute();
        $this->redis()->setex($key, $ttl, serialize($value));

        return $value;
    }
}

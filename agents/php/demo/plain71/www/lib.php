<?php
// Shared helpers of the plain PHP 7.1 demo (PHP 7.1 syntax).

function demo_env($name, $default)
{
    $v = getenv($name);
    return $v === false || $v === '' ? $default : $v;
}

function demo_json($data, $status = 200)
{
    http_response_code($status);
    header('Content-Type: application/json');
    echo json_encode($data), "\n";
}

function demo_mysqli_params()
{
    return [demo_env('DB_HOST', 'mariadb'), demo_env('DB_USER', 'demo'), demo_env('DB_PASSWORD', 'demo'), demo_env('DB_NAME', 'demo')];
}

function demo_pg_dsn()
{
    return sprintf('host=%s port=5432 dbname=%s user=%s password=%s connect_timeout=3',
        demo_env('PG_HOST', 'postgres'), demo_env('PG_DATABASE', 'demo'), demo_env('PG_USER', 'demo'), demo_env('PG_PASSWORD', 'demo'));
}

function demo_upstream()
{
    return rtrim(demo_env('DEMO_UPSTREAM_URL', 'http://nginx-laravel-74'), '/');
}

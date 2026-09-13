<?php

// Spike routes: exercise routing, Eloquent/PDO, Redis, outbound HTTP and exceptions.

use App\Models\Item;
use Illuminate\Support\Facades\Http;
use Illuminate\Support\Facades\Redis;
use Illuminate\Support\Facades\Route;

// Benchmark endpoint: one Eloquent query + one Redis round trip, no outbound HTTP.
Route::get('/bench/{id}', function (int $id) {
    $item = Item::query()->where('id', ($id % 100) + 1)->first();
    Redis::set('last_item', $item?->id);
    return response()->json(['id' => $item?->id, 'name' => $item?->name, 'cached' => Redis::get('last_item')]);
})->name('bench');

// Functional endpoint: DB + Redis + outbound HTTP (to the Symfony app).
Route::get('/users/{id}/orders', function (int $id) {
    $items = Item::query()->where('id', '<=', 5)->orderBy('id')->get();
    Redis::incr('orders:hits');
    // getenv, not env(): env() returns null once the config is cached.
    $resp = Http::timeout(2)->get((getenv('SPIKE_SYMFONY_URL') ?: 'http://nginx-base:8081') . '/api/products/' . $id);
    return response()->json([
        'user' => $id,
        'items' => $items->count(),
        'hits' => (int) Redis::get('orders:hits'),
        'upstream_status' => $resp->status(),
    ]);
})->name('orders');

Route::get('/boom', function () {
    throw new RuntimeException('spike: intentional failure');
})->name('boom');

Route::get('/health', fn () => 'ok');

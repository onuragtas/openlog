<?php

// openlog PHP demo routes (Laravel 12 on PHP 8.3 and Laravel 8 on PHP 7.4 share this file: PHP 7.4 syntax,
// controller actions so `php artisan optimize` can cache the routes on Laravel 8).

use App\Http\Controllers\DemoController;
use Illuminate\Support\Facades\Route;

Route::get('/health', [DemoController::class, 'health']);
// Overhead benchmark endpoint: one Eloquent query + two Redis calls, no outbound HTTP.
Route::get('/bench/{id}', [DemoController::class, 'bench'])->where('id', '[0-9]+');
// Eloquent + Redis + Http:: + Http::pool (curl_multi) + file_get_contents('http://...').
Route::get('/users/{id}/orders', [DemoController::class, 'orders'])->where('id', '[0-9]+');
// > 600 ms of nested userland service calls: triggers the function-level transaction tracer (500 ms threshold).
Route::get('/slow/report', [DemoController::class, 'slowReport']);
Route::get('/boom', [DemoController::class, 'boom']);
Route::get('/reported', [DemoController::class, 'reported']);
Route::get('/fatal', [DemoController::class, 'fatal']);
Route::get('/fatal/user-error', [DemoController::class, 'fatalUserError']);
Route::get('/fatal/memory', [DemoController::class, 'fatalMemory']);
Route::get('/predis', [DemoController::class, 'predis']);

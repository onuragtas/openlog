<?php

namespace App\Http\Controllers;

use App\Models\DemoItem;
use App\Models\DemoOrder;
use App\Services\Billing\PaymentGateway;
use App\Services\Reports\SlowReportService;
use Illuminate\Http\Client\Pool;
use Illuminate\Http\Client\Response as ClientResponse;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Http;
use Illuminate\Support\Facades\Redis;

// PHP 7.4 compatible on purpose (shared by the Laravel 8 / PHP 7.4 demo).
class DemoController extends Controller
{
    public function health()
    {
        return response('ok');
    }

    public function bench($id)
    {
        $item = DemoItem::query()->where('id', ((int) $id % 100) + 1)->first();
        Redis::set('last_item', $item ? $item->id : 0);

        return response()->json([
            'id' => $item ? $item->id : null,
            'name' => $item ? $item->name : null,
            'cached' => Redis::get('last_item'),
        ]);
    }

    public function orders($id)
    {
        $userId = ((int) $id % 20) + 1;
        $user = DB::table('users')->where('id', $userId)->first();
        $orders = DemoOrder::query()->where('user_id', $userId)->orderByDesc('id')->limit(10)->get();
        Redis::incr('orders:hits');
        Redis::setex("user:{$userId}:orders", 30, (string) $orders->count());

        $symfony = rtrim(getenv('DEMO_SYMFONY_URL') ?: 'http://nginx-symfony-83', '/');
        $peer = rtrim(getenv('DEMO_PEER_URL') ?: $symfony, '/');

        // Http:: (Guzzle -> curl)
        $product = Http::timeout(3)->get($symfony . '/api/products/' . $userId);
        // Http::pool (Guzzle -> curl_multi)
        $pooled = Http::pool(function (Pool $pool) use ($symfony, $peer, $userId) {
            return [
                $pool->timeout(3)->get($symfony . '/api/products/' . ($userId + 1)),
                $pool->timeout(3)->get($peer . '/health'),
            ];
        });
        // PHP http stream wrapper
        $ctx = stream_context_create(['http' => ['timeout' => 3, 'ignore_errors' => true]]);
        $stream = @file_get_contents($symfony . '/health', false, $ctx);

        return response()->json([
            'user' => $user ? $user->name : null,
            'orders' => $orders->count(),
            'hits' => (int) Redis::get('orders:hits'),
            'product_status' => $product->status(),
            'pool_statuses' => array_map(function ($r) {
                return $r instanceof ClientResponse ? $r->status() : get_class($r);
            }, $pooled),
            'stream_ok' => $stream !== false,
        ]);
    }

    public function slowReport(SlowReportService $reports)
    {
        $started = microtime(true);
        $report = $reports->build((int) request('customers', 8));

        return response()->json([
            'rows' => count($report['rows']),
            'total' => $report['total'],
            'elapsed_ms' => (int) ((microtime(true) - $started) * 1000),
        ]);
    }

    public function boom()
    {
        throw new \RuntimeException('demo: intentional failure in DemoController::boom');
    }

    public function reported(PaymentGateway $gateway)
    {
        try {
            $gateway->charge(4200, 'card_declined');
        } catch (\Throwable $e) {
            report($e);

            return response()->json(['reported' => get_class($e), 'message' => $e->getMessage()]);
        }

        return response()->json(['reported' => false]);
    }

    public function fatal()
    {
        // Uncaught Error: Call to undefined function.
        return openlog_demo_undefined_function();
    }

    public function fatalUserError()
    {
        trigger_error('demo: E_USER_ERROR raised by DemoController::fatalUserError', E_USER_ERROR);

        return response('unreachable');
    }

    public function fatalMemory()
    {
        // Real engine fatal error ("Allowed memory size ... exhausted"), not an exception.
        ini_set('memory_limit', '64M');
        $chunks = [];
        while (true) {
            $chunks[] = str_repeat('x', 1024 * 1024);
        }
    }

    public function predis()
    {
        $client = new \Predis\Client(['host' => getenv('REDIS_HOST') ?: 'redis', 'port' => 6379]);
        $hits = $client->incr('predis:hits');
        $client->set('predis:last', (string) microtime(true));

        return response()->json(['hits' => $hits, 'last' => $client->get('predis:last')]);
    }
}

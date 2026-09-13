<?php

namespace App\Services\Reports;

use Illuminate\Support\Facades\DB;

// Deliberately slow (> 600 ms), deeply nested userland code for the function-level transaction tracer.
class SlowReportService
{
    /** @var OrderAggregator */
    private $aggregator;
    /** @var ReportRenderer */
    private $renderer;

    public function __construct(OrderAggregator $aggregator, ReportRenderer $renderer)
    {
        $this->aggregator = $aggregator;
        $this->renderer = $renderer;
    }

    public function build(int $customers): array
    {
        $customers = max(1, min($customers, 20));
        $users = $this->loadCustomers($customers);
        $rows = [];
        foreach ($users as $user) {
            $rows[] = $this->aggregator->summarizeCustomer($user);
        }
        $total = $this->aggregator->grandTotal($rows);
        $this->renderer->render($rows, $total);

        return ['rows' => $rows, 'total' => $total];
    }

    private function loadCustomers(int $limit): array
    {
        usleep(60000);
        // A slow query shows up as a long datastore span inside the trace.
        DB::select('SELECT SLEEP(0.12) AS slept');

        return DB::table('users')->orderBy('id')->limit($limit)->get()->all();
    }
}

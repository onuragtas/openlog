<?php

// Summarizes k6 JSON summaries + cpu-mem.csv into a markdown table (median of runs, min..max spread).

$dir = $argv[1] ?? '/r';
// Memory comes from mem.csv (bench/mem.sh) when present, keyed by variant+run.
$mem = [];
if (is_file("$dir/mem.csv")) {
    $m = array_map('str_getcsv', file("$dir/mem.csv", FILE_IGNORE_NEW_LINES));
    $mh = array_shift($m);
    foreach ($m as $line) {
        $r = array_combine($mh, $line);
        $mem[$r['variant']][$r['run']] = (int) $r['rss_kb'];
    }
}
$csv = array_map('str_getcsv', file("$dir/cpu-mem.csv", FILE_IGNORE_NEW_LINES));
$head = array_shift($csv);
$rows = [];
foreach ($csv as $line) {
    // Tolerate header/row column count differences (older result files).
    $line = array_slice(array_pad($line, count($head), ''), 0, count($head));
    $row = array_combine($head, $line);
    $k6 = json_decode(file_get_contents("$dir/{$row['variant']}-run{$row['run']}.json"), true);
    $m = $k6['metrics'];
    $count = $m['http_reqs']['values']['count'];
    $rows[$row['variant']][] = [
        'rps' => $m['http_reqs']['values']['rate'],
        'p50' => $m['http_req_duration']['values']['med'],
        'p95' => $m['http_req_duration']['values']['p(95)'],
        'p99' => $m['http_req_duration']['values']['p(99)'],
        'fail' => $m['http_req_failed']['values']['rate'] * 100,
        'cpu_ms_req' => $row['php_cpu_usec'] / 1000 / max(1, $count),
        'fwd_ms_req' => $row['forwarder_cpu_usec'] / 1000 / max(1, $count),
        'rss_mb' => ($mem[$row['variant']][$row['run']] ?? (int) ($row['rss_kb'] ?? 0)) / 1024,
    ];
}

function med(array $v): float { sort($v); $n = count($v); return $n % 2 ? $v[intdiv($n, 2)] : ($v[$n / 2 - 1] + $v[$n / 2]) / 2; }
function cell(array $runs, string $k, int $dec = 1): string {
    $v = array_column($runs, $k);
    return sprintf("%.{$dec}f (%.{$dec}f–%.{$dec}f)", med($v), min($v), max($v));
}

$metrics = [
    'rps' => ['RPS', 0], 'p50' => ['p50 ms', 2], 'p95' => ['p95 ms', 2], 'p99' => ['p99 ms', 2], 'fail' => ['failed %', 2],
    'cpu_ms_req' => ['PHP CPU ms/req', 3], 'fwd_ms_req' => ['forwarder CPU ms/req', 3],
    'rss_mb' => ['VmRSS MB/worker (Laravel pool)', 1],
];
$variants = array_values(array_intersect(['base', 'a', 'b'], array_keys($rows)));
echo "| metric (median, min–max of " . count($rows[$variants[0]]) . " runs) | " . implode(' | ', $variants) . " |\n";
echo '|---' . str_repeat('|---', count($variants)) . "|\n";
foreach ($metrics as $k => [$label, $dec]) {
    echo "| $label | " . implode(' | ', array_map(fn ($v) => cell($rows[$v], $k, $dec), $variants)) . " |\n";
}
$base = med(array_column($rows['base'], 'rps'));
foreach ($variants as $v) {
    if ($v === 'base') continue;
    printf("\n- %s: RPS %+.1f%%, p50 %+.1f%%, p95 %+.1f%%, PHP CPU/req %+.1f%% vs baseline", $v,
        (med(array_column($rows[$v], 'rps')) / $base - 1) * 100,
        (med(array_column($rows[$v], 'p50')) / med(array_column($rows['base'], 'p50')) - 1) * 100,
        (med(array_column($rows[$v], 'p95')) / med(array_column($rows['base'], 'p95')) - 1) * 100,
        (med(array_column($rows[$v], 'cpu_ms_req')) / med(array_column($rows['base'], 'cpu_ms_req')) - 1) * 100);
}
echo "\n";

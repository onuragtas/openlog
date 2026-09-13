<?php

// Summarizes bench/ext results (k6 JSON summaries + cpu.csv) into a markdown table: median of runs (min–max) per
// variant and the % delta of each variant vs base. Usage: php summarize.php <results-dir>

$dir = $argv[1] ?? '/r';
$csv = array_map('str_getcsv', file("$dir/cpu.csv", FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES));
$head = array_shift($csv);
$rows = [];
foreach ($csv as $line) {
    $row = array_combine($head, $line);
    $file = "$dir/{$row['variant']}-run{$row['run']}.json";
    if (!is_file($file)) {
        fwrite(STDERR, "missing $file\n");
        continue;
    }
    $m = json_decode(file_get_contents($file), true)['metrics'];
    $count = $m['http_reqs']['values']['count'];
    $rows[$row['variant']][] = [
        'rps' => $m['http_reqs']['values']['rate'],
        'p50' => $m['http_req_duration']['values']['med'],
        'p95' => $m['http_req_duration']['values']['p(95)'],
        'p99' => $m['http_req_duration']['values']['p(99)'],
        'fail' => $m['http_req_failed']['values']['rate'] * 100,
        'cpu_ms_req' => $row['php_cpu_usec'] / 1000 / max(1, $count),
        'fwd_ms_req' => $row['forwarder_cpu_usec'] / 1000 / max(1, $count),
    ];
}

function med(array $v): float
{
    sort($v);
    $n = count($v);
    return $n % 2 ? $v[intdiv($n, 2)] : ($v[$n / 2 - 1] + $v[$n / 2]) / 2;
}

function cell(array $runs, string $k, int $dec): string
{
    $v = array_column($runs, $k);
    return count($v) === 1 ? sprintf("%.{$dec}f", $v[0]) : sprintf("%.{$dec}f (%.{$dec}f–%.{$dec}f)", med($v), min($v), max($v));
}

$metrics = [
    'rps' => ['RPS', 0, true], 'p50' => ['p50 ms', 2, true], 'p95' => ['p95 ms', 2, true], 'p99' => ['p99 ms', 2, true],
    'fail' => ['failed %', 2, false], 'cpu_ms_req' => ['PHP CPU ms/req', 3, true], 'fwd_ms_req' => ['forwarder CPU ms/req', 3, false],
];
$variants = array_values(array_intersect(['base', 'off', 'on'], array_keys($rows)));
if (!$variants) {
    fwrite(STDERR, "no results\n");
    exit(1);
}
$runs = count($rows[$variants[0]]);
echo "| metric (median" . ($runs > 1 ? ", min–max" : '') . " of $runs run" . ($runs > 1 ? 's' : '') . ") | " . implode(' | ', $variants) . " |\n";
echo '|---' . str_repeat('|---', count($variants)) . "|\n";
foreach ($metrics as $k => [$label, $dec]) {
    echo "| $label | " . implode(' | ', array_map(fn ($v) => cell($rows[$v], $k, $dec), $variants)) . " |\n";
}
if (isset($rows['base'])) {
    echo "\n| delta vs base | " . implode(' | ', array_diff($variants, ['base'])) . " |\n";
    echo '|---' . str_repeat('|---', count($variants) - 1) . "|\n";
    foreach ($metrics as $k => [$label, $dec, $relative]) {
        if (!$relative) {
            continue;
        }
        $b = med(array_column($rows['base'], $k));
        $cells = [];
        foreach ($variants as $v) {
            if ($v !== 'base') {
                $cells[] = $b > 0 ? sprintf('%+.1f%%', (med(array_column($rows[$v], $k)) / $b - 1) * 100) : 'n/a';
            }
        }
        echo "| $label | " . implode(' | ', $cells) . " |\n";
    }
    echo "\nBudget (php-agent.md §3): off ≤ 3 % RPS loss, on ≤ 7 % RPS loss vs base.\n";
}

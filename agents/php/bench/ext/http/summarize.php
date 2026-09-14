<?php
/* Summary of bench/ext/http/run.sh: medians with spread per app and variant, deltas against base. */
$rows = file($argv[1], FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES);
$head = explode("\t", array_shift($rows));
$data = [];
foreach ($rows as $l) {
    $r = array_combine($head, explode("\t", $l));
    foreach (['rps', 'p50_ms', 'p99_ms', 'cpu_us_per_req', 'non2xx'] as $k) {
        $data[$r['app']][$r['variant']][$k][] = (float) $r[$k];
    }
}

function pct(array $v, $p)
{
    sort($v);
    $i = ($p / 100) * (count($v) - 1);
    $lo = (int) floor($i);
    $hi = (int) ceil($i);
    return $v[$lo] + ($v[$hi] - $v[$lo]) * ($i - $lo);
}

echo "# openlog.so HTTP benchmark (PHP-FPM 8 workers / 4 CPUs, nginx, wrk, datagram sink)\n\n";
echo "Medians over rounds; spread p25–p75 (min–max). Δ against the base median.\n\n";
foreach ($data as $app => $vs) {
    $b = isset($vs['base']) ? $vs['base'] : null;
    echo "## $app\n\n";
    echo "| variant | RPS | Δ RPS | PHP CPU µs/req | Δ CPU | p50 ms | p99 ms | non-2xx |\n|---|---|---|---|---|---|---|---|\n";
    foreach ($vs as $v => $m) {
        $rps = pct($m['rps'], 50);
        $cpu = pct($m['cpu_us_per_req'], 50);
        $drps = $b && $v !== 'base' ? sprintf('%+.1f %%', ($rps / pct($b['rps'], 50) - 1) * 100) : '—';
        $dcpu = $b && $v !== 'base' ? sprintf('%+.0f µs (%+.1f %%)', $cpu - pct($b['cpu_us_per_req'], 50), ($cpu / pct($b['cpu_us_per_req'], 50) - 1) * 100) : '—';
        printf("| %s | %.0f (%.0f–%.0f, %.0f–%.0f) | %s | %.0f (%.0f–%.0f) | %s | %.2f | %.2f | %d |\n", $v, $rps, pct($m['rps'], 25),
            pct($m['rps'], 75), min($m['rps']), max($m['rps']), $drps, $cpu, pct($m['cpu_us_per_req'], 25),
            pct($m['cpu_us_per_req'], 75), $dcpu, pct($m['p50_ms'], 50), pct($m['p99_ms'], 50), array_sum($m['non2xx']));
    }
    echo "\n";
}

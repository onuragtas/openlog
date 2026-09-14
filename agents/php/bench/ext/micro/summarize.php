<?php
/* Summary of bench/ext/micro results: time.tsv (ns/request per round) and callgrind.tsv (instructions/request). */
$dir = $argv[1];

function tsv($file)
{
    $rows = [];
    if (!is_file($file)) {
        return $rows;
    }
    $lines = file($file, FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES);
    $head = explode("\t", array_shift($lines));
    foreach ($lines as $l) {
        $rows[] = array_combine($head, explode("\t", $l));
    }
    return $rows;
}

function pct(array $v, $p)
{
    sort($v);
    $i = ($p / 100) * (count($v) - 1);
    $lo = (int) floor($i);
    $hi = (int) ceil($i);
    return $v[$lo] + ($v[$hi] - $v[$lo]) * ($i - $lo);
}

$time = [];
foreach (tsv("$dir/time.tsv") as $r) {
    $time[$r['app']][$r['variant']][] = (int) $r['ns_per_req'];
}
$ir = [];
foreach (tsv("$dir/callgrind.tsv") as $r) {
    $ir[$r['app']][$r['variant']] = (int) $r['ir_per_req'];
}

echo "# openlog.so micro benchmark (php-cgi -T, one pinned CPU)\n\n";
foreach ($time as $app => $vs) {
    $base = isset($vs['base']) ? pct($vs['base'], 50) : null;
    $baseIr = isset($ir[$app]['base']) ? $ir[$app]['base'] : null;
    echo "## $app\n\n";
    echo "| variant | µs/req median | p25–p75 | min–max | Δ vs base (median) | Δ µs | instr/req | Δ instr vs base |\n";
    echo "|---|---|---|---|---|---|---|---|\n";
    foreach ($vs as $v => $vals) {
        $m = pct($vals, 50);
        $d = $base ? sprintf('%+.1f %%', ($m - $base) / $base * 100) : '—';
        $du = $base ? sprintf('%+.0f', ($m - $base) / 1000) : '—';
        $i = isset($ir[$app][$v]) ? $ir[$app][$v] : null;
        $di = ($i !== null && $baseIr) ? sprintf('%+d (%+.1f %%)', $i - $baseIr, ($i - $baseIr) / $baseIr * 100) : '—';
        printf("| %s | %.0f | %.0f–%.0f | %.0f–%.0f | %s | %s | %s | %s |\n", $v, $m / 1000, pct($vals, 25) / 1000,
            pct($vals, 75) / 1000, min($vals) / 1000, max($vals) / 1000, $v === 'base' ? '—' : $d, $v === 'base' ? '—' : $du,
            $i === null ? '—' : number_format($i), $v === 'base' ? '—' : $di);
    }
    echo "\n";
}

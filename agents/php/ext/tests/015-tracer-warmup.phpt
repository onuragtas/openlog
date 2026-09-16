--TEST--
Transaction tracer warm-up: warmup_ms / warmup_segment_ms control the sampling interval at request start
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
/*
 * ~40 ms of pure userland CPU, well inside the default 100 ms warm-up window and above the 10 ms tracer threshold.
 * No instrumented internal call is made (microtime is not hooked), so the two 20 ms functions are found by the timer
 * samples alone: they appear as segments whenever the effective interval is min_segment_ms, not the 10 ms warm-up one.
 */
$code = '<?php
function burn_ms($ms) { $end = microtime(true) + $ms / 1000; $n = 0; while (microtime(true) < $end) { $n++; } return $n; }
function slow_part() { return burn_ms(20); }
function other_part() { return burn_ms(20); }
function main() { slow_part(); other_part(); }
main();
';

/* Assertions stay coarse on purpose: presence of the two clearly-longer functions, never exact sample counts. */
function warmup_run($label, array $ini)
{
    global $code;
    $t = ol_one_trace(ol_run($code, ['ini' => $ini]));
    if ($t === null) {
        return;
    }
    $slow = ol_find($t, 'slow_part');
    echo $label, ': function_trace=', var_export($t['function_trace'], true),
        ' slow_part=', count($slow) >= 1 ? 'yes' : 'no',
        ' other_part=', count(ol_find($t, 'other_part')) >= 1 ? 'yes' : 'no',
        ' samples>=3: ', ($slow && $slow[0]['attrs']['openlog.php.samples'] >= 3) ? 'yes' : 'no', "\n";
}

$fine = ['openlog.transaction_tracer.min_segment_ms' => '1'];
warmup_run('warm-up off', $fine + ['openlog.transaction_tracer.warmup_ms' => '0']);
warmup_run('warm-up off (negative)', $fine + ['openlog.transaction_tracer.warmup_ms' => '-5']);
warmup_run('1 ms warm-up interval', $fine + ['openlog.transaction_tracer.warmup_ms' => '1000',
    'openlog.transaction_tracer.warmup_segment_ms' => '1']);
warmup_run('warm-up interval clamped to min_segment_ms', $fine + ['openlog.transaction_tracer.warmup_ms' => '1000',
    'openlog.transaction_tracer.warmup_segment_ms' => '0']);

/* The default warm-up (100 ms / 10 ms) covers this request: it still works, only with fewer samples. */
$r = ol_run($code);
$t = ol_one_trace($r);
$root = ol_root($t);
echo 'default warm-up: function_trace=', var_export($t['function_trace'], true),
    ' root=', ($root !== null && $root['name'] !== '') ? 'yes' : 'no', ' exit=', $r['exit'], "\n";
--EXPECT--
warm-up off: function_trace=true slow_part=yes other_part=yes samples>=3: yes
warm-up off (negative): function_trace=true slow_part=yes other_part=yes samples>=3: yes
1 ms warm-up interval: function_trace=true slow_part=yes other_part=yes samples>=3: yes
warm-up interval clamped to min_segment_ms: function_trace=true slow_part=yes other_part=yes samples>=3: yes
default warm-up: function_trace=true root=yes exit=0

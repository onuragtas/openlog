--TEST--
traceparent: continued, unsampled parent, malformed headers start a new trace; tracestate propagated
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; if (!is_executable(dirname(PHP_BINARY) . '/php-cgi')) die('skip no php-cgi'); ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$code = '<?php echo \openlog\traceparent(), "|", \openlog\trace_id(), "|", var_export(\openlog\is_sampled(), true);';

// valid, sampled
$r = ol_run($code, ['cgi' => true, 'server' => [
    'HTTP_TRACEPARENT' => '00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01',
    'HTTP_TRACESTATE' => 'vendor=abc',
]]);
$t = ol_traces($r['msgs']);
$root = ol_root(reset($t));
echo "sampled: trace=", key($t), " parent=", $root['parent'], " out=", preg_replace('/-[0-9a-f]{16}-/', '-<span>-', $r['out']), "\n";

// valid, not sampled: nothing is sent, context still available
$r = ol_run($code, ['cgi' => true, 'server' => ['HTTP_TRACEPARENT' => '00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00']]);
echo "unsampled: messages=", count($r['msgs']), " out=", $r['out'], "\n";

// future version with extra fields
$r = ol_run($code, ['cgi' => true, 'server' => ['HTTP_TRACEPARENT' => '01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra']]);
$t = ol_traces($r['msgs']);
echo "future version: continued=", key($t) === '4bf92f3577b34da6a3ce929d0e0e4736' ? 'yes' : 'no', "\n";

$bad = [
    'uppercase' => '00-4BF92F3577B34DA6A3CE929D0E0E4736-00F067AA0BA902B7-01',
    'zero trace' => '00-00000000000000000000000000000000-00f067aa0ba902b7-01',
    'zero parent' => '00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01',
    'short' => '00-4bf92f3577b34da6a3ce929d0e0e473-00f067aa0ba902b7-01',
    'version ff' => 'ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01',
    'v00 trailing' => '00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-x',
    'garbage' => str_repeat('z', 300),
    'bad flags' => '00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-0g',
];
foreach ($bad as $label => $tp) {
    $r = ol_run($code, ['cgi' => true, 'server' => ['HTTP_TRACEPARENT' => $tp]]);
    $t = ol_traces($r['msgs']);
    $root = $t ? ol_root(reset($t)) : null;
    echo "$label: traces=", count($t), " new=", ($t && key($t) !== '4bf92f3577b34da6a3ce929d0e0e4736') ? 'yes' : 'no',
        " parent='", $root ? $root['parent'] : '-', "'", $r['invalid'] ? ' INVALID' : '', "\n";
}

// CLI: TRACEPARENT environment variable
$r = ol_run($code, ['env' => ['TRACEPARENT' => '00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01']]);
$t = ol_traces($r['msgs']);
echo "cli env: ", key($t), " parent=", ol_root(reset($t))['parent'], "\n";
--EXPECT--
sampled: trace=4bf92f3577b34da6a3ce929d0e0e4736 parent=00f067aa0ba902b7 out=00-4bf92f3577b34da6a3ce929d0e0e4736-<span>-01|4bf92f3577b34da6a3ce929d0e0e4736|true
unsampled: messages=0 out=00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00|4bf92f3577b34da6a3ce929d0e0e4736|false
future version: continued=yes
uppercase: traces=1 new=yes parent=''
zero trace: traces=1 new=yes parent=''
zero parent: traces=1 new=yes parent=''
short: traces=1 new=yes parent=''
version ff: traces=1 new=yes parent=''
v00 trailing: traces=1 new=yes parent=''
garbage: traces=1 new=yes parent=''
bad flags: traces=1 new=yes parent=''
cli env: 4bf92f3577b34da6a3ce929d0e0e4736 parent=00f067aa0ba902b7

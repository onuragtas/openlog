--TEST--
Head sampling: ratio 0 sends nothing but propagates flags 00; ratio 0.5 samples about half and reports the ratio
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; if (!is_executable(dirname(PHP_BINARY) . '/php-cgi')) die('skip no php-cgi'); ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$r = ol_run('<?php echo substr(\openlog\traceparent(), -2), " ", var_export(\openlog\is_sampled(), true), "\n";', ['ini' => ['openlog.sampling_ratio' => '0']]);
echo "ratio 0: messages=", count($r['msgs']), " out=", $r['out'];

$r = ol_run('<?php echo "x";', ['cgi' => true, 'repeat' => 200, 'ini' => ['openlog.sampling_ratio' => '0.5']]);
$traces = ol_traces($r['msgs']);
$n = count($traces);
echo "ratio 0.5: sampled between 50 and 150: ", ($n >= 50 && $n <= 150) ? "yes" : "no ($n)", "\n";
$ratios = array_unique(array_map(function ($t) { return $t['sampling_ratio']; }, $traces));
echo "reported ratio: ", implode(',', $ratios), "\n";
echo $r['invalid'] ? "INVALID\n" : "valid\n";

// sampled parent wins over ratio 0; the entry span keeps the remote parent
$r = ol_run('<?php echo "x";', ['cgi' => true, 'ini' => ['openlog.sampling_ratio' => '0'],
    'server' => ['HTTP_TRACEPARENT' => '00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01']]);
$t = ol_one_trace($r);
echo "parent-based: ratio=", $t['sampling_ratio'], " parent=", ol_root($t)['parent'], "\n";
--EXPECT--
ratio 0: messages=0 out=00 false
ratio 0.5: sampled between 50 and 150: yes
reported ratio: 0.5
valid
parent-based: ratio=1 parent=b7ad6b7169203331

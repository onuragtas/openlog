--TEST--
Splitting: large traces are split into datagrams <= 60000 bytes with increasing seq, last only on the final part
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; if (!extension_loaded('pdo_sqlite')) die('skip pdo_sqlite'); ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
foreach ([400, 2000] as $queries) {
    $r = ol_run('<?php $db = new PDO("sqlite::memory:");
    for ($i = 0; $i < ' . $queries . '; $i++) { $db->query("SELECT \'" . str_repeat("a", 300) . "\' AS v$i"); }');
    foreach ($r['invalid'] as $p) echo "INVALID: $p\n";
    $t = ol_one_trace($r);
    $seqs = $t['parts'];
    sort($seqs);
    $lastFlags = [];
    $max = 0;
    $resources = [];
    foreach ($r['msgs'] as $i => $m) {
        $lastFlags[$m['seq']] = $m['last'];
        $max = max($max, strlen($r['raw'][$i]));
        $resources[json_encode($m['resource'])] = true;
    }
    ksort($lastFlags);
    $onlyFinal = true;
    foreach ($lastFlags as $seq => $last) {
        if ($last !== ($seq === count($lastFlags) - 1)) $onlyFinal = false;
    }
    $ids = [];
    foreach ($t['spans'] as $s) $ids[$s['id']] = true;
    $orphans = 0;
    foreach ($t['spans'] as $s) if ($s['parent'] !== '' && !isset($ids[$s['parent']])) $orphans++;
    echo "$queries queries: parts>1=", count($seqs) > 1 ? 'yes' : 'no',
        " contiguous=", $seqs === range(0, count($seqs) - 1) ? 'yes' : 'no',
        " last-only-final=", $onlyFinal ? 'yes' : 'no',
        " max<=60000=", $max <= 60000 ? 'yes' : 'no',
        " same-resource=", count($resources) === 1 ? 'yes' : 'no',
        " spans=", count($t['spans']), " unique=", count($ids), " orphans=", $orphans,
        " dropped=", $t['dropped_spans'], "\n";
}
--EXPECT--
400 queries: parts>1=yes contiguous=yes last-only-final=yes max<=60000=yes same-resource=yes spans=401 unique=401 orphans=0 dropped=0
2000 queries: parts>1=yes contiguous=yes last-only-final=yes max<=60000=yes same-resource=yes spans=2001 unique=2001 orphans=0 dropped=0

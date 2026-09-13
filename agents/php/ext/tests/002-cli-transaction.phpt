--TEST--
CLI transaction: kind 1, "php <script>", resource, openlog.php.cli
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$r = ol_run('<?php echo "hello\n";', [
    'env' => ['OPENLOG_SERVICE_NAME' => 'cli-svc', 'OPENLOG_ENVIRONMENT' => 'test'],
    'ini' => ['openlog.service_version' => '1.2.3'],
]);
echo $r['out'];
$t = ol_one_trace($r);
$root = ol_root($t);
echo preg_replace('/^php .*\.php$/', 'php <script>', $root['name']), "\n";
echo "kind=", $root['kind'], " status=", $root['status'], " parent='", $root['parent'], "'\n";
var_dump($root['attrs']['openlog.php.cli']);
$res = $t['resource'];
ksort($res);
foreach ($res as $k => $v) {
    if ($k === 'process.runtime.version') { $v = $v === PHP_VERSION ? 'PHP_VERSION' : $v; }
    if ($k === 'telemetry.distro.version' || $k === 'container.id') { $v = '*'; }
    echo "$k=$v\n";
}
echo "last=", var_export($t['last'], true), " parts=", implode(',', $t['parts']), " ratio=", $t['sampling_ratio'], " ft=", var_export($t['function_trace'], true), "\n";
echo "dur>0: ", $root['dur'] > 0 ? 'yes' : 'no', "\n";
echo "start~now: ", abs($root['start'] / 1e9 - microtime(true)) < 60 ? 'yes' : 'no', "\n";
--EXPECTF--
hello
php <script>
kind=1 status=0 parent=''
bool(true)
%Adeployment.environment.name=test
php.sapi=cli
process.runtime.name=php
process.runtime.version=PHP_VERSION
service.name=cli-svc
service.version=1.2.3
telemetry.distro.name=openlog-php
telemetry.distro.version=*
last=true parts=0 ratio=1 ft=false
dur>0: yes
start~now: yes

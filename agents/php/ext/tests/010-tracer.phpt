--TEST--
Transaction tracer: segment tree above the threshold, fast-call aggregation, nothing below the threshold
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$code = '<?php
namespace App\Reports;
class Report {
    public function build($slow) {
        $this->load($slow);
        $sum = 0;
        for ($i = 0; $i < 50; $i++) { $sum += $this->fast($i); }
        $this->render($slow);
        return $sum;
    }
    private function load($slow) { usleep($slow ? 300000 : 100); }
    private function fast($i) { return $i * 2; }
    protected function render($slow) { helper(); usleep($slow ? 250000 : 100); }
}
function helper() { usleep(20000); }
$slow = getenv("SLOW") === "1";
echo (new Report())->build($slow), "\n";
';
$r = ol_run($code, ['env' => ['SLOW' => '1']]);
echo $r['out'];
$t = ol_one_trace($r);
echo "function_trace=", var_export($t['function_trace'], true), "\n";
ol_print_tree($t, ['code.namespace', 'code.function.name', 'openlog.php.segment', 'openlog.php.fast_calls']);
$build = ol_find($t, 'App\Reports\Report::build')[0];
echo "fast_calls_ns>0: ", $build['attrs']['openlog.php.fast_calls_ns'] > 0 ? 'yes' : 'no', "\n";
echo "line: ", $build['attrs']['code.line.number'], " file set: ", $build['attrs']['code.file.path'] !== '' ? 'yes' : 'no', "\n";

$r = ol_run($code, ['env' => ['SLOW' => '0']]);
$t = ol_one_trace($r);
echo "fast request: function_trace=", var_export($t['function_trace'], true), " spans=", count($t['spans']), "\n";

$r = ol_run($code, ['env' => ['SLOW' => '1'], 'ini' => ['openlog.transaction_tracer.enabled' => '0']]);
$t = ol_one_trace($r);
echo "tracer disabled: function_trace=", var_export($t['function_trace'], true), " spans=", count($t['spans']), "\n";

$r = ol_run($code, ['env' => ['SLOW' => '1'], 'ini' => ['openlog.transaction_tracer.threshold_ms' => '5000']]);
$t = ol_one_trace($r);
echo "threshold 5 s: function_trace=", var_export($t['function_trace'], true), " spans=", count($t['spans']), "\n";

// below the threshold but failing: the function trace is sent
$r = ol_run('<?php function a() { b(); } function b() { usleep(3000); throw new LogicException("x"); } a();');
$t = ol_one_trace($r);
echo "error: function_trace=", var_export($t['function_trace'], true), " segments=", count(ol_find($t, 'a')) + count(ol_find($t, 'b')), "\n";
--EXPECTF--
2450
function_trace=true
php %s kind=1 status=0
  App\Reports\Report::build kind=1 status=0 code.namespace="App\\Reports\\Report" code.function.name="build" openlog.php.segment="function" openlog.php.fast_calls=50
    App\Reports\Report::load kind=1 status=0 code.namespace="App\\Reports\\Report" code.function.name="load" openlog.php.segment="function"
      usleep kind=1 status=0 code.function.name="usleep"
    App\Reports\Report::render kind=1 status=0 code.namespace="App\\Reports\\Report" code.function.name="render" openlog.php.segment="function"
      App\Reports\helper kind=1 status=0 code.function.name="App\\Reports\\helper" openlog.php.segment="function"
        usleep kind=1 status=0 code.function.name="usleep"
      usleep kind=1 status=0 code.function.name="usleep"
fast_calls_ns>0: yes
line: 4 file set: yes
fast request: function_trace=false spans=1
tracer disabled: function_trace=false spans=1
threshold 5 s: function_trace=false spans=1
error: function_trace=true segments=2

--TEST--
Transaction tracer (stack sampling): segment tree above the threshold, spans under the right function, nothing below
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
function without_fast(array $t) {
    /* a timer sample may hit the 50 tiny fast() calls: they are not part of the expectation */
    $t['spans'] = array_values(array_filter($t['spans'], function ($s) { return $s['name'] !== 'App\Reports\Report::fast'; }));
    return $t;
}
$r = ol_run($code, ['env' => ['SLOW' => '1']]);
echo $r['out'];
$t = without_fast(ol_one_trace($r));
echo "function_trace=", var_export($t['function_trace'], true), "\n";
ol_print_tree($t, ['code.namespace', 'code.function.name', 'openlog.php.segment']);
$build = ol_find($t, 'App\Reports\Report::build')[0];
$load = ol_find($t, 'App\Reports\Report::load')[0];
echo "samples>=1: ", $build['attrs']['openlog.php.samples'] >= 1 ? 'yes' : 'no', "\n";
echo "load covers its usleep (>=290 ms): ", $load['dur'] >= 290000000 ? 'yes' : 'no (' . $load['dur'] . ')', "\n";
echo "line: ", $build['attrs']['code.line.number'], " file set: ", $build['attrs']['code.file.path'] !== '' ? 'yes' : 'no', "\n";

// ~20 ms (helper's usleep), below an explicit 500 ms threshold
$r = ol_run($code, ['env' => ['SLOW' => '0'], 'ini' => ['openlog.transaction_tracer.threshold_ms' => '500']]);
$t = ol_one_trace($r);
echo "fast request: function_trace=", var_export($t['function_trace'], true), " spans=", count($t['spans']), "\n";

$r = ol_run($code, ['env' => ['SLOW' => '1'], 'ini' => ['openlog.transaction_tracer.enabled' => '0']]);
$t = ol_one_trace($r);
echo "tracer disabled: function_trace=", var_export($t['function_trace'], true), " spans=", count($t['spans']), "\n";

$r = ol_run($code, ['env' => ['SLOW' => '1'], 'ini' => ['openlog.transaction_tracer.threshold_ms' => '5000']]);
$t = ol_one_trace($r);
echo "threshold 5 s: function_trace=", var_export($t['function_trace'], true), " spans=", count($t['spans']), "\n";

// CPU-bound code without instrumented calls is found by the timer samples
$r = ol_run('<?php function spin() { $x = 0; for ($i = 0; $i < 30000000; $i++) { $x += $i % 7; } return $x; } function outer() { return spin(); } outer(); usleep(500000);');
$t = ol_one_trace($r);
echo "cpu-bound: spin segment=", count(ol_find($t, 'spin')) === 1 ? 'yes' : 'no', " parent outer=", (ol_find($t, 'spin') && ol_find($t, 'outer') && ol_find($t, 'spin')[0]['parent'] === ol_find($t, 'outer')[0]['id']) ? 'yes' : 'no', "\n";

// below the threshold but failing: the function trace is sent
$r = ol_run('<?php function a() { b(); } function b() { usleep(3000); throw new LogicException("x"); } a();');
$t = ol_one_trace($r);
echo "error: function_trace=", var_export($t['function_trace'], true), " segments=", count(ol_find($t, 'a')) + count(ol_find($t, 'b')), "\n";
--EXPECTF--
2450
function_trace=true
php %s kind=1 status=0
  App\Reports\Report::build kind=1 status=0 code.namespace="App\\Reports\\Report" code.function.name="build" openlog.php.segment="function"
    App\Reports\Report::load kind=1 status=0 code.namespace="App\\Reports\\Report" code.function.name="load" openlog.php.segment="function"
      usleep kind=1 status=0 code.function.name="usleep"
    App\Reports\Report::render kind=1 status=0 code.namespace="App\\Reports\\Report" code.function.name="render" openlog.php.segment="function"
      App\Reports\helper kind=1 status=0 code.function.name="App\\Reports\\helper" openlog.php.segment="function"
        usleep kind=1 status=0 code.function.name="usleep"
      usleep kind=1 status=0 code.function.name="usleep"
samples>=1: yes
load covers its usleep (>=290 ms): yes
line: 4 file set: yes
fast request: function_trace=false spans=1
tracer disabled: function_trace=false spans=1
threshold 5 s: function_trace=false spans=1
cpu-bound: spin segment=yes parent outer=yes
error: function_trace=true segments=2

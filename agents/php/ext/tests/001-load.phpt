--TEST--
openlog.so loads: functions, ini defaults
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$r = ol_run('<?php
var_dump(extension_loaded("openlog"));
foreach (["trace_id", "span_id", "traceparent", "set_transaction_name", "is_sampled", "stats"] as $f) {
    echo $f, " ", function_exists("openlog\\\\$f") ? "yes" : "no", "\n";
}
foreach (["openlog.enabled", "openlog.transaction_tracer.enabled", "openlog.transaction_tracer.threshold_ms",
    "openlog.transaction_tracer.max_segments", "openlog.transaction_tracer.min_segment_ms",
    "openlog.transaction_tracer.warmup_ms", "openlog.transaction_tracer.warmup_segment_ms",
    "openlog.transaction_tracer.max_memory_kb", "openlog.sampling_ratio", "openlog.capture_query_text",
    "openlog.log_level", "openlog.service_name"] as $k) {
    echo $k, "=", ini_get($k), "\n";
}
echo "default transport ", ini_get("openlog.transport") !== "" ? "set" : "empty", "\n";
', ['socket' => false, 'ini' => ['openlog.transport' => 'unix:///run/openlog-infra-agent/php.sock']]);
echo $r['out'], $r['err'];
--EXPECT--
bool(true)
trace_id yes
span_id yes
traceparent yes
set_transaction_name yes
is_sampled yes
stats yes
openlog.enabled=1
openlog.transaction_tracer.enabled=1
openlog.transaction_tracer.threshold_ms=10
openlog.transaction_tracer.max_segments=2000
openlog.transaction_tracer.min_segment_ms=1
openlog.transaction_tracer.warmup_ms=100
openlog.transaction_tracer.warmup_segment_ms=10
openlog.transaction_tracer.max_memory_kb=4096
openlog.sampling_ratio=1.0
openlog.capture_query_text=sanitized
openlog.log_level=warning
openlog.service_name=
default transport set

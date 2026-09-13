<?php
// Outbound HTTP: curl_exec, curl_multi_*, file_get_contents on http://.
require __DIR__ . '/lib.php';

$upstream = demo_upstream();
$symfony = rtrim(demo_env('DEMO_SYMFONY_URL', 'http://nginx-symfony-83'), '/');

// curl_exec
$ch = curl_init($symfony . '/api/products/' . mt_rand(1, 100));
curl_setopt_array($ch, [CURLOPT_RETURNTRANSFER => true, CURLOPT_TIMEOUT => 3]);
curl_exec($ch);
$single = curl_getinfo($ch, CURLINFO_HTTP_CODE);
curl_close($ch);

// curl_multi
$mh = curl_multi_init();
$handles = [];
foreach ([$upstream . '/health', $symfony . '/health'] as $url) {
    $h = curl_init($url);
    curl_setopt_array($h, [CURLOPT_RETURNTRANSFER => true, CURLOPT_TIMEOUT => 3]);
    curl_multi_add_handle($mh, $h);
    $handles[$url] = $h;
}
do {
    $status = curl_multi_exec($mh, $running);
    if ($running) {
        curl_multi_select($mh, 1.0);
    }
} while ($running && $status === CURLM_OK);
$multi = [];
foreach ($handles as $url => $h) {
    $multi[$url] = curl_getinfo($h, CURLINFO_HTTP_CODE);
    curl_multi_remove_handle($mh, $h);
    curl_close($h);
}
curl_multi_close($mh);

// http stream wrapper
$ctx = stream_context_create(['http' => ['timeout' => 3, 'ignore_errors' => true]]);
$body = @file_get_contents($upstream . '/health', false, $ctx);

demo_json(['curl_exec' => $single, 'curl_multi' => $multi, 'file_get_contents' => $body !== false]);

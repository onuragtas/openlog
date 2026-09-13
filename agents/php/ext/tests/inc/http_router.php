<?php
// Router for the phpt HTTP server: echoes trace headers; ?status=N sets the response code; ?sleep=ms delays.
$status = isset($_GET['status']) ? (int) $_GET['status'] : 200;
if (isset($_GET['sleep'])) {
    usleep((int) $_GET['sleep'] * 1000);
}
http_response_code($status);
header('Content-Type: application/json');
echo json_encode([
    'traceparent' => isset($_SERVER['HTTP_TRACEPARENT']) ? $_SERVER['HTTP_TRACEPARENT'] : null,
    'tracestate' => isset($_SERVER['HTTP_TRACESTATE']) ? $_SERVER['HTTP_TRACESTATE'] : null,
    'x_test' => isset($_SERVER['HTTP_X_TEST']) ? $_SERVER['HTTP_X_TEST'] : null,
    'method' => $_SERVER['REQUEST_METHOD'],
]);

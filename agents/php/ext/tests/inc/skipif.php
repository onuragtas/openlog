<?php
require_once __DIR__ . '/harness.php';
if (ol_ext_so() === '') {
    die('skip openlog.so not built (set OPENLOG_EXT_SO)');
}
if (!function_exists('proc_open') || !function_exists('stream_socket_server')) {
    die('skip proc_open/stream_socket_server unavailable');
}

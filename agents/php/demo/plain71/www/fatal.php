<?php
// Fatal errors -> HTTP 500.  ?type=undefined (default) | memory | user
require __DIR__ . '/lib.php';

$type = isset($_GET['type']) ? $_GET['type'] : 'undefined';
switch ($type) {
    case 'memory':
        ini_set('memory_limit', '32M');
        $chunks = [];
        while (true) {
            $chunks[] = str_repeat('x', 1024 * 1024);
        }
        break;
    case 'user':
        trigger_error('demo: E_USER_ERROR in fatal.php', E_USER_ERROR);
        break;
    default:
        openlog_demo_undefined_function();
}
echo "unreachable\n";

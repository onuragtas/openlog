<?php
// Uncaught exception -> HTTP 500.
require __DIR__ . '/lib.php';

function boom_validate_order($orderId)
{
    if ($orderId > 0) {
        throw new DomainException("demo: order $orderId failed validation (uncaught)");
    }
}

boom_validate_order(isset($_GET['order']) ? (int) $_GET['order'] : 42);
echo "unreachable\n";

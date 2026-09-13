<?php

// openlog PHP agent spike (option B): loaded with auto_prepend_file, no change to the application.
// SPDX-License-Identifier: Apache-2.0
// Fail-open: if anything goes wrong the request runs without tracing.

if (PHP_SAPI !== 'cli' && \extension_loaded('opentelemetry') && !\class_exists(\Openlog\Php\Bootstrap::class, false)) {
    try {
        require __DIR__ . '/vendor/autoload.php';
        \Openlog\Php\Bootstrap::start();
    } catch (\Throwable $e) {
        \error_log('[openlog] PHP agent disabled for this request: ' . $e->getMessage());
    }
}

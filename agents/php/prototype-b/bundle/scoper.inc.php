<?php

// php-scoper config for the option-B bundle (SPDX-License-Identifier: Apache-2.0).
// Everything the bundle ships is prefixed with OpenlogVendor\ so it cannot collide with the application's
// Composer dependencies. Excluded: the frameworks we hook (their classes come from the application), the
// ext-opentelemetry/ext-protobuf native symbols, Composer's runtime classes and our own entry namespace.

use Isolated\Symfony\Component\Finder\Finder;

return [
    'prefix' => 'OpenlogVendor',
    'finders' => [
        Finder::create()->files()->in('src')->name('*.php'),
        Finder::create()->files()->in('vendor')
            ->name(['*.php', 'installed.json', 'composer.json'])
            ->notPath('#(^|/)(tests?|Tests?|docs?)/#')
            ->exclude(['bin']),
    ],
    'exclude-namespaces' => [
        'Openlog\Php',
        'Illuminate',
        'Laravel',
        'Symfony\Component\HttpKernel',
        'Symfony\Component\HttpFoundation',
        'Symfony\Component\HttpClient',
        'Symfony\Component\Messenger',
        'Symfony\Component\Console',
        'Symfony\Contracts\HttpClient',
        'Google\Protobuf',
        // Generated OTLP protobuf classes must keep their names: ext-protobuf resolves message classes from the
        // PHP namespace embedded in the serialized descriptor ("Couldn't find descriptor" otherwise).
        'Opentelemetry\Proto',
        'GPBMetadata',
        'Composer',
    ],
    'exclude-functions' => [
        'OpenTelemetry\Instrumentation\hook',
    ],
    'exclude-classes' => [
        'OpenTelemetry\Instrumentation\WithSpan',
        'OpenTelemetry\Instrumentation\SpanAttribute',
    ],
    'expose-global-constants' => false,
    'expose-global-classes' => false,
    'expose-global-functions' => false,
];

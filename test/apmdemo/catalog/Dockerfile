FROM composer:2 AS composer

FROM php:8.3-cli-alpine

COPY --from=mlocati/php-extension-installer:2 /usr/bin/install-php-extensions /usr/local/bin/
RUN install-php-extensions opentelemetry protobuf redis
COPY --from=composer /usr/bin/composer /usr/local/bin/composer

WORKDIR /app
COPY composer.json composer.lock ./
RUN composer install --no-dev --no-interaction --no-progress --prefer-dist --optimize-autoloader \
    && rm -rf /root/.composer/cache
COPY public ./public
COPY src ./src
RUN composer dump-autoload --no-dev --optimize --classmap-authoritative

ENV OTEL_PHP_AUTOLOAD_ENABLED=true \
    OTEL_TRACES_EXPORTER=otlp \
    OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf \
    OTEL_METRICS_EXPORTER=none \
    OTEL_LOGS_EXPORTER=none \
    OTEL_PROPAGATORS=tracecontext,baggage \
    OTEL_TRACES_SAMPLER=parentbased_always_on \
    PHP_CLI_SERVER_WORKERS=4

USER www-data
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --retries=5 CMD php -r 'exit(@fsockopen("127.0.0.1", 8080) ? 0 : 1);'
CMD ["php", "-S", "0.0.0.0:8080", "-t", "public", "public/index.php"]

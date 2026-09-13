# syntax=docker/dockerfile:1.7
# openlog PHP demo: Laravel on PHP-FPM (php-laravel-83: Laravel 12 / PHP 8.3, php-laravel-74: Laravel 8 / PHP 7.4).
# Build context: agents/php (so both ext/ and demo/ are reachable).
#   OPENLOG_EXT=1  compile agents/php/ext (openlog.so) into the image; 0 = no extension (ext/ is not even read)
ARG PHP_IMAGE=php:8.3-fpm-bookworm
ARG OPENLOG_EXT=0

FROM ${PHP_IMAGE} AS php-base
ARG REDIS_VERSION=6.2.0
COPY --chmod=0755 demo/php/apt-update.sh /usr/local/bin/apt-update
RUN apt-update && apt-get install -y --no-install-recommends unzip git curl ca-certificates $PHPIZE_DEPS \
    && rm -rf /var/lib/apt/lists/*
RUN docker-php-ext-install -j"$(nproc)" pdo_mysql opcache
RUN pecl install -D 'enable-redis-igbinary="no" enable-redis-lzf="no" enable-redis-zstd="no" enable-redis-msgpack="no" enable-redis-lz4="no"' "redis-${REDIS_VERSION}" \
    && docker-php-ext-enable redis && rm -rf /tmp/pear
RUN cp "$PHP_INI_DIR/php.ini-production" "$PHP_INI_DIR/php.ini"
COPY demo/php/demo.ini "$PHP_INI_DIR/conf.d/zz-demo.ini"

# --- optional openlog extension (stage only built when OPENLOG_EXT=1) ---
FROM php-base AS ext-0
FROM php-base AS ext-1
COPY ext /usr/src/openlog-ext
RUN cd /usr/src/openlog-ext && (make distclean >/dev/null 2>&1 || true) && (phpize --clean >/dev/null 2>&1 || true) \
    && phpize && ./configure --enable-openlog && make -j"$(nproc)" && make install \
    && php -d extension=openlog.so -m | grep -qx openlog

# --- application ---
FROM php-base AS app
ARG LARAVEL_CONSTRAINT=^12.0
ARG ENV_FILE=laravel-83.env
# Packagist security advisories block resolution of some (EOL) framework versions; demo only.
ARG COMPOSER_BLOCK_INSECURE=true
COPY --from=composer:2 /usr/bin/composer /usr/bin/composer
ENV COMPOSER_ALLOW_SUPERUSER=1
RUN composer config -g audit.block-insecure "${COMPOSER_BLOCK_INSECURE}"
RUN --mount=type=cache,target=/root/.composer/cache \
    composer create-project --no-interaction --no-progress --no-scripts --prefer-dist "laravel/laravel:${LARAVEL_CONSTRAINT}" /srv/app \
    && cd /srv/app && composer require --no-interaction --no-progress --no-scripts --update-no-dev "predis/predis:^2.0" \
    && composer install --no-dev --no-interaction --no-progress --optimize-autoloader --no-scripts
COPY demo/laravel/overlay/ /srv/app/
COPY demo/laravel/${ENV_FILE} /srv/app/.env
RUN cd /srv/app && composer dump-autoload --no-dev --optimize --no-interaction \
    && php artisan package:discover && php artisan key:generate --force

FROM ext-${OPENLOG_EXT}
COPY --from=app /srv/app /srv/app
RUN rm -f /usr/local/etc/php-fpm.d/www.conf /usr/local/etc/php-fpm.d/docker.conf /usr/local/etc/php-fpm.d/zz-docker.conf
COPY demo/php/fpm-pool.conf /usr/local/etc/php-fpm.d/demo.conf
COPY --chmod=0755 demo/php/openlog-entrypoint.sh /usr/local/bin/openlog-entrypoint
COPY --chmod=0755 demo/laravel/start.sh /usr/local/bin/laravel-start
ENV FPM_WORKERS=4 OPENLOG_SERVICE_NAME=php-laravel OPENLOG_AGENT=on
WORKDIR /srv/app
ENTRYPOINT ["/usr/local/bin/openlog-entrypoint"]
CMD ["/usr/local/bin/laravel-start"]

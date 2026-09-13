# syntax=docker/dockerfile:1.7
# openlog PHP demo: php-symfony-83 (symfony/skeleton 7.4 + FrameworkBundle, attribute routes) on PHP-FPM 8.3.
# Build context: agents/php.  OPENLOG_EXT=1 compiles agents/php/ext into the image.
ARG OPENLOG_EXT=0

FROM php:8.3-fpm-bookworm AS php-base
COPY --chmod=0755 demo/php/apt-update.sh /usr/local/bin/apt-update
RUN apt-update && apt-get install -y --no-install-recommends unzip git libpq-dev $PHPIZE_DEPS \
    && rm -rf /var/lib/apt/lists/*
RUN docker-php-ext-install -j"$(nproc)" pdo_pgsql pgsql opcache
RUN pecl install -D 'enable-redis-igbinary="no" enable-redis-lzf="no" enable-redis-zstd="no" enable-redis-msgpack="no" enable-redis-lz4="no"' redis-6.2.0 \
    && docker-php-ext-enable redis && rm -rf /tmp/pear
RUN cp "$PHP_INI_DIR/php.ini-production" "$PHP_INI_DIR/php.ini"
COPY demo/php/demo.ini "$PHP_INI_DIR/conf.d/zz-demo.ini"

FROM php-base AS ext-0
FROM php-base AS ext-1
COPY ext /usr/src/openlog-ext
RUN cd /usr/src/openlog-ext && (make distclean >/dev/null 2>&1 || true) && (phpize --clean >/dev/null 2>&1 || true) \
    && phpize && ./configure --enable-openlog && make -j"$(nproc)" && make install \
    && php -d extension=openlog.so -m | grep -qx openlog

FROM php-base AS app
ARG SYMFONY_VERSION=7.4.*
ARG COMPOSER_BLOCK_INSECURE=true
COPY --from=composer:2 /usr/bin/composer /usr/bin/composer
ENV COMPOSER_ALLOW_SUPERUSER=1
RUN composer config -g audit.block-insecure "${COMPOSER_BLOCK_INSECURE}"
RUN --mount=type=cache,target=/root/.composer/cache \
    composer create-project --no-interaction --no-progress --prefer-dist "symfony/skeleton:${SYMFONY_VERSION}" /srv/app \
    && cd /srv/app && composer require --no-interaction --no-progress "symfony/http-client:${SYMFONY_VERSION}"
COPY demo/symfony/app/ /srv/app/
ENV APP_ENV=prod APP_DEBUG=0
RUN --mount=type=cache,target=/root/.composer/cache \
    cd /srv/app && composer install --no-dev --no-interaction --no-progress --optimize-autoloader --classmap-authoritative \
    && composer dump-env prod && php bin/console cache:warmup && php bin/console debug:router --env=prod | grep -q product_show

FROM ext-${OPENLOG_EXT}
COPY --from=app /srv/app /srv/app
RUN chown -R www-data:www-data /srv/app/var \
    && rm -f /usr/local/etc/php-fpm.d/www.conf /usr/local/etc/php-fpm.d/docker.conf /usr/local/etc/php-fpm.d/zz-docker.conf
COPY demo/php/fpm-pool.conf /usr/local/etc/php-fpm.d/demo.conf
COPY --chmod=0755 demo/php/openlog-entrypoint.sh /usr/local/bin/openlog-entrypoint
ENV FPM_WORKERS=4 OPENLOG_SERVICE_NAME=php-symfony-83 OPENLOG_AGENT=on APP_ENV=prod APP_DEBUG=0
WORKDIR /srv/app
ENTRYPOINT ["/usr/local/bin/openlog-entrypoint"]
CMD ["php-fpm"]

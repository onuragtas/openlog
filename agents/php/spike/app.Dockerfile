# syntax=docker/dockerfile:1.7
# PHP 8.3 FPM image for the PHP agent spike: Laravel 11 + Symfony 7 apps, and both agent options installed but
# NOT enabled. A container picks its variant with PHP_INI_SCAN_DIR=":/opt/variants/<base|a|b>".
# Build context: agents/php

FROM php:8.3-fpm-bookworm AS php-base
RUN apt-get update && apt-get install -y --no-install-recommends unzip git curl \
    && rm -rf /var/lib/apt/lists/*
RUN docker-php-ext-install -j"$(nproc)" pdo_mysql opcache
RUN pecl install -D 'enable-redis-igbinary="no" enable-redis-lzf="no" enable-redis-zstd="no" enable-redis-msgpack="no" enable-redis-lz4="no"' redis-6.2.0 \
    && docker-php-ext-enable redis
# Option B native parts (enabled only by /opt/variants/b)
RUN pecl install opentelemetry-1.2.1 protobuf-4.31.1 && rm -rf /tmp/pear
# Option A extension (enabled only by /opt/variants/a)
COPY prototype-a/ext /usr/src/openlog-ext
RUN cd /usr/src/openlog-ext && phpize && ./configure && make -j"$(nproc)" && make install && make clean
RUN cp "$PHP_INI_DIR/php.ini-production" "$PHP_INI_DIR/php.ini"
COPY spike/php/spike.ini "$PHP_INI_DIR/conf.d/zz-spike.ini"
COPY --from=composer:2 /usr/bin/composer /usr/bin/composer
ENV COMPOSER_ALLOW_SUPERUSER=1

FROM php-base AS laravel
RUN --mount=type=cache,target=/root/.composer/cache \
    composer create-project --no-interaction --no-progress --no-scripts --prefer-dist laravel/laravel:^12.0 /srv/laravel \
    && cd /srv/laravel && composer install --no-dev --no-interaction --no-progress --optimize-autoloader --no-scripts
COPY apps/laravel/overlay/ /srv/laravel/
RUN cd /srv/laravel && composer dump-autoload --no-dev --optimize --no-interaction \
    && php artisan package:discover && php artisan key:generate --force

FROM php-base AS symfony
COPY apps/symfony/app/ /srv/symfony/
RUN --mount=type=cache,target=/root/.composer/cache \
    cd /srv/symfony && composer update --no-dev --no-interaction --no-progress --optimize-autoloader

FROM php-base AS bundle
ADD https://github.com/humbug/php-scoper/releases/download/0.18.19/php-scoper.phar /usr/local/bin/php-scoper
COPY prototype-b/bundle /build
WORKDIR /build
RUN --mount=type=cache,target=/root/.composer/cache \
    composer install --no-dev --no-interaction --no-progress
RUN php -d memory_limit=-1 /usr/local/bin/php-scoper add-prefix --output-dir=/out --force --no-interaction \
    && cp composer.json /out/ && cd /out \
    && composer dump-autoload --classmap-authoritative --no-dev --no-interaction --no-plugins
COPY prototype-b/prepend.php /out/prepend.php

FROM php-base
COPY --from=laravel /srv/laravel /srv/laravel
COPY --from=symfony /srv/symfony /srv/symfony
COPY --from=bundle /out /opt/openlog-php
COPY spike/php/variants /opt/variants
RUN rm -f /usr/local/etc/php-fpm.d/www.conf /usr/local/etc/php-fpm.d/docker.conf /usr/local/etc/php-fpm.d/zz-docker.conf
COPY spike/php/fpm-pools.conf /usr/local/etc/php-fpm.d/spike.conf
COPY --chmod=0755 spike/php/entrypoint.sh /usr/local/bin/spike-entrypoint
ENV PHP_INI_SCAN_DIR=":/opt/variants/base" SPIKE_VARIANT=base
ENTRYPOINT ["/usr/local/bin/spike-entrypoint"]

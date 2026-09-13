# syntax=docker/dockerfile:1.7
# openlog PHP demo: php-plain-71 (no framework) on php:7.1-apache (mod_php).
# Build context: agents/php.  OPENLOG_EXT=1 compiles agents/php/ext into the image.
ARG OPENLOG_EXT=0

FROM php:7.1-apache AS php-base
ARG REDIS_VERSION=5.3.7
COPY --chmod=0755 demo/php/apt-update.sh /usr/local/bin/apt-update
RUN apt-update && apt-get install -y --no-install-recommends libpq-dev $PHPIZE_DEPS && rm -rf /var/lib/apt/lists/*
RUN docker-php-ext-install -j"$(nproc)" mysqli pdo_mysql pgsql pdo_pgsql opcache
# redis 5.3.7 is the last phpredis release for PHP 7.0/7.1 (4.3.0 as fallback); answer "no" to the optional codecs.
RUN (printf 'no\nno\nno\nno\nno\n' | pecl install "redis-${REDIS_VERSION}" || printf 'no\n' | pecl install redis-4.3.0) \
    && docker-php-ext-enable redis && rm -rf /tmp/pear && php -m | grep -qx redis
RUN cp "$PHP_INI_DIR/php.ini-production" "$PHP_INI_DIR/php.ini"
COPY demo/php/demo.ini "$PHP_INI_DIR/conf.d/zz-demo.ini"

FROM php-base AS ext-0
FROM php-base AS ext-1
COPY ext /usr/src/openlog-ext
RUN cd /usr/src/openlog-ext && (make distclean >/dev/null 2>&1 || true) && (phpize --clean >/dev/null 2>&1 || true) \
    && phpize && ./configure --enable-openlog && make -j"$(nproc)" && make install \
    && php -d extension=openlog.so -m | grep -qx openlog

FROM ext-${OPENLOG_EXT}
COPY demo/plain71/www/ /var/www/html/
COPY --chmod=0755 demo/php/openlog-entrypoint.sh /usr/local/bin/openlog-entrypoint
RUN echo 'ServerName php-plain-71' > /etc/apache2/conf-enabled/servername.conf
ENV OPENLOG_SERVICE_NAME=php-plain-71 OPENLOG_AGENT=on
ENTRYPOINT ["/usr/local/bin/openlog-entrypoint"]
CMD ["apache2-foreground"]

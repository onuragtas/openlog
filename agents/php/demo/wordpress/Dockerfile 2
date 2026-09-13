# syntax=docker/dockerfile:1.7
# openlog PHP demo: php-wordpress-82 (official wordpress:php8.2-apache, mod_php) auto-installed with wp-cli.
# Build context: agents/php.  OPENLOG_EXT=1 compiles agents/php/ext with the image's phpize.
ARG OPENLOG_EXT=0

FROM wordpress:php8.2-apache AS wp-base
ARG WP_CLI_VERSION=2.12.0
ADD --chmod=0755 https://github.com/wp-cli/wp-cli/releases/download/v${WP_CLI_VERSION}/wp-cli-${WP_CLI_VERSION}.phar /usr/local/bin/wp

FROM wp-base AS ext-0
FROM wp-base AS ext-1
RUN apt-get update && apt-get install -y --no-install-recommends $PHPIZE_DEPS && rm -rf /var/lib/apt/lists/*
COPY ext /usr/src/openlog-ext
RUN cd /usr/src/openlog-ext && (make distclean >/dev/null 2>&1 || true) && (phpize --clean >/dev/null 2>&1 || true) \
    && phpize && ./configure --enable-openlog && make -j"$(nproc)" && make install \
    && php -d extension=openlog.so -m | grep -qx openlog

FROM ext-${OPENLOG_EXT}
COPY demo/wordpress/mu-plugins/ /usr/src/wordpress/wp-content/mu-plugins/
COPY --chmod=0755 demo/php/openlog-entrypoint.sh /usr/local/bin/openlog-entrypoint
COPY --chmod=0755 demo/wordpress/entrypoint.sh /usr/local/bin/demo-wp-entrypoint
ENV OPENLOG_SERVICE_NAME=php-wordpress-82 OPENLOG_AGENT=on
ENTRYPOINT ["/usr/local/bin/openlog-entrypoint", "/usr/local/bin/demo-wp-entrypoint"]
CMD ["apache2-foreground"]

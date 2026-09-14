# Per-request micro benchmark image (bench/ext/micro/run.sh): the phpt test image (php-cgi, PDO SQLite, opcache) +
# valgrind + the demo Laravel 12 application (served through php-cgi -T, no web server, no database).
# Build context: agents/php/bench/ext/micro. Requires openlog-php/laravel-83:dev (make -C agents/php/demo images) and
# openlog-php-test:8.3 (agents/php/ext/build/run-matrix.sh builds it).
ARG TEST_IMAGE=openlog-php-test:8.3
ARG APP_IMAGE=openlog-php/laravel-83:dev
FROM ${APP_IMAGE} AS app

FROM ${TEST_IMAGE}
RUN apt-update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends valgrind util-linux \
    && rm -rf /var/lib/apt/lists/*
COPY --from=app /srv/app /srv/app
# Laravel caches (config/routes/views) as in the FPM image; the bench route (/health) needs no database.
RUN cd /srv/app && php artisan optimize >/dev/null && chmod -R a+rwX storage bootstrap/cache \
    && printf 'opcache.enable=1\nopcache.validate_timestamps=0\nopcache.memory_consumption=128\nrealpath_cache_ttl=600\n' \
       > /usr/local/etc/php/conf.d/zz-bench.ini

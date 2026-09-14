# HTTP benchmark image (bench/ext/http/run.sh): the demo Laravel 12 / PHP 8.3 FPM image plus openlog.so built from
# up to two source trees (a = older, b = current) installed as openlog-a.so / openlog-b.so, and the plain PHP app
# (bench/ext/micro/plain.php) at /srv/plain/index.php. Build context: a directory staged by run.sh.
ARG APP_IMAGE=openlog-php/laravel-83:dev
FROM ${APP_IMAGE}
COPY . /usr/src/openlog-bench
RUN set -e; cd /usr/src/openlog-bench; \
    for s in a b; do \
      [ -d "$s" ] || continue; \
      (cd "$s" && phpize >/dev/null && ./configure --enable-openlog >/dev/null && make -j"$(nproc)" >/dev/null 2>&1); \
      cp "$s/modules/openlog.so" "$(php-config --extension-dir)/openlog-$s.so"; \
    done; \
    mkdir -p /srv/plain && cp plain.php /srv/plain/index.php; \
    cd / && rm -rf /usr/src/openlog-bench

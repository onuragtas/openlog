#!/bin/sh
# Spike app container entrypoint: warm Laravel caches with the runtime env, then run PHP-FPM.
set -e
cd /srv/laravel
php artisan optimize >/dev/null
chown -R www-data:www-data storage bootstrap/cache
exec php-fpm

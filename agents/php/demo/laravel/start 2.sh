#!/bin/sh
# Warm Laravel caches with the runtime environment, then run PHP-FPM in the foreground.
set -e
cd /srv/app
php artisan optimize >/dev/null
chown -R www-data:www-data storage bootstrap/cache
exec php-fpm

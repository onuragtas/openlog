#!/bin/bash
# Installs WordPress with wp-cli in the background once the official entrypoint has copied the files and written
# wp-config.php, then hands over to the official entrypoint (apache2-foreground). Marker: /var/www/html/.demo-installed
set -euo pipefail

install_wordpress() {
  cd /var/www/html
  for _ in $(seq 1 120); do [ -f wp-config.php ] && break; sleep 1; done
  for _ in $(seq 1 120); do
    php -r '$h = explode(":", getenv("WORDPRESS_DB_HOST")); exit(@mysqli_connect($h[0], getenv("WORDPRESS_DB_USER"), getenv("WORDPRESS_DB_PASSWORD"), getenv("WORDPRESS_DB_NAME"), (int) ($h[1] ?? 3306)) ? 0 : 1);' && break
    sleep 1
  done
  local wp=(wp --allow-root --path=/var/www/html)
  if ! "${wp[@]}" core is-installed 2>/dev/null; then
    "${wp[@]}" core install --url="$WP_HOME" --title="openlog PHP demo" --admin_user=admin \
      --admin_password=openlog-demo --admin_email=admin@demo.local --skip-email
    "${wp[@]}" rewrite structure '/%postname%/'
    for n in 1 2 3 4 5; do
      "${wp[@]}" post create --post_type=post --post_status=publish --post_title="Demo post $n" --post_name="demo-post-$n" \
        --post_content="Demo content $n for the openlog PHP agent demo." >/dev/null
    done
    "${wp[@]}" post create --post_type=page --post_status=publish --post_title="Slow report" --post_name="slow-report" \
      --post_content="This page renders slowly on purpose." >/dev/null
  fi
  "${wp[@]}" option update home "$WP_HOME" >/dev/null
  "${wp[@]}" option update siteurl "$WP_HOME" >/dev/null
  "${wp[@]}" rewrite flush --hard >/dev/null 2>&1 || "${wp[@]}" rewrite flush >/dev/null
  chown -R www-data:www-data wp-content
  touch /var/www/html/.demo-installed
  echo "[demo-wp] WordPress ready at $WP_HOME" >&2
}

rm -f /var/www/html/.demo-installed
install_wordpress &
exec docker-entrypoint.sh "$@"

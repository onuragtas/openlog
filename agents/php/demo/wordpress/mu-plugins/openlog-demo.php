<?php
/**
 * Plugin Name: openlog PHP demo
 * Description: Demo routes for the openlog PHP agent: slow page, REST route with $wpdb + wp_remote_get, errors.
 */

if (!defined('ABSPATH')) {
    exit;
}

function openlog_demo_upstream(): string
{
    return rtrim(getenv('DEMO_UPSTREAM_URL') ?: 'http://nginx-symfony-83', '/');
}

add_action('rest_api_init', function () {
    register_rest_route('demo/v1', '/health', [
        'methods' => 'GET',
        'callback' => fn () => ['status' => 'ok'],
        'permission_callback' => '__return_true',
    ]);
    register_rest_route('demo/v1', '/items/(?P<id>\d+)', [
        'methods' => 'GET',
        'callback' => 'openlog_demo_rest_item',
        'permission_callback' => '__return_true',
    ]);
    register_rest_route('demo/v1', '/boom', [
        'methods' => 'GET',
        'callback' => 'openlog_demo_rest_boom',
        'permission_callback' => '__return_true',
    ]);
});

// $wpdb (mysqli) queries + wp_remote_get (Requests -> curl) to the Symfony demo.
function openlog_demo_rest_item(WP_REST_Request $request): WP_REST_Response
{
    global $wpdb;
    $id = (int) $request['id'];
    $post = $wpdb->get_row($wpdb->prepare(
        "SELECT ID, post_title FROM {$wpdb->posts} WHERE post_type = 'post' AND post_status = 'publish' ORDER BY ID LIMIT 1 OFFSET %d",
        $id % 5
    ));
    $published = (int) $wpdb->get_var("SELECT COUNT(*) FROM {$wpdb->posts} WHERE post_status = 'publish'");
    $meta = $post ? get_post_meta((int) $post->ID) : [];
    $response = wp_remote_get(openlog_demo_upstream() . '/api/products/' . $id, ['timeout' => 3]);

    return new WP_REST_Response([
        'id' => $id,
        'post' => $post ? $post->post_title : null,
        'published' => $published,
        'meta_keys' => count($meta),
        'upstream_status' => is_wp_error($response) ? $response->get_error_message() : wp_remote_retrieve_response_code($response),
    ]);
}

function openlog_demo_rest_boom(): void
{
    openlog_demo_explode('REST route /wp-json/demo/v1/boom');
}

function openlog_demo_explode(string $where): void
{
    throw new RuntimeException("demo: uncaught exception in WordPress ($where)");
}

// /?slow=1 or /slow-report/ : > 600 ms of nested functions.  /?boom=1 : uncaught exception.
add_action('template_redirect', function () {
    if (isset($_GET['boom'])) {
        openlog_demo_explode('template_redirect ?boom=1');
    }
    if (isset($_GET['slow']) || is_page('slow-report')) {
        wp_send_json(openlog_demo_slow_page());
    }
}, 1);

function openlog_demo_slow_page(): array
{
    $started = microtime(true);
    $posts = openlog_demo_collect_posts();
    $scores = openlog_demo_score_posts($posts);
    $widgets = openlog_demo_render_widgets($scores);
    openlog_demo_warm_cache($widgets);

    return ['posts' => count($posts), 'widgets' => count($widgets), 'elapsed_ms' => (int) ((microtime(true) - $started) * 1000)];
}

function openlog_demo_collect_posts(): array
{
    global $wpdb;
    $wpdb->query('SELECT SLEEP(0.12)');
    usleep(80000);

    return get_posts(['numberposts' => 10, 'post_status' => 'publish']);
}

function openlog_demo_score_posts(array $posts): array
{
    $scores = [];
    foreach ($posts as $post) {
        $scores[$post->ID] = openlog_demo_score_post($post);
    }
    usleep(40000);

    return $scores;
}

function openlog_demo_score_post(WP_Post $post): int
{
    $x = 0;
    for ($i = 0; $i < 40000; $i++) {
        $x = ($x + $i * strlen($post->post_title)) % 7919;
    }

    return $x;
}

function openlog_demo_render_widgets(array $scores): array
{
    $out = [];
    foreach (['recent', 'popular', 'related'] as $name) {
        $out[$name] = openlog_demo_render_widget($name, $scores);
    }

    return $out;
}

function openlog_demo_render_widget(string $name, array $scores): string
{
    usleep(120000);
    arsort($scores);

    return '<ul class="' . esc_attr($name) . '"><li>' . implode('</li><li>', array_keys($scores)) . '</li></ul>';
}

function openlog_demo_warm_cache(array $widgets): void
{
    usleep(150000);
    foreach ($widgets as $name => $html) {
        wp_cache_set('openlog_demo_' . $name, $html);
    }
}

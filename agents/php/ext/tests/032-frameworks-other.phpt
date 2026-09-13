--TEST--
Slim 3/4, WordPress (template, REST), CodeIgniter 3/4, Yii 2 (stubbed classes): transaction names
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; if (!is_executable(dirname(PHP_BINARY) . '/php-cgi')) die('skip no php-cgi'); ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$cases = [
    'slim3' => ['/hello/bob', '<?php namespace Slim; class Route { protected $pattern; public function __construct($p) { $this->pattern = $p; } public function run($req, $res) { return $res; } }
        (new Route("/hello/{name}"))->run(null, null);'],
    'slim4' => ['/books/12', '<?php namespace Slim\Routing; class Route { protected $pattern; public function __construct($p) { $this->pattern = $p; } public function run($req) { return 1; } }
        (new Route("/books/{id:[0-9]+}"))->run(null);'],
    'wordpress template' => ['/2024/01/hello-world/', '<?php
        function apply_filters($tag, $value) { return $tag === "template_include" ? "/var/www/html/wp-content/themes/twenty/single.php" : $value; }
        apply_filters("the_content", "x");
        $tpl = apply_filters("template_include", "/x/index.php");
        apply_filters("the_title", "y");'],
    'wordpress rest' => ['/wp-json/wp/v2/posts/12', '<?php
        function apply_filters($tag, $value) { return $value; }
        class WP_REST_Request { protected $route; public function __construct($r) { $this->route = $r; } }
        class WP_REST_Server {
            public function dispatch($request) { return $this->match_request_to_handler($request); }
            protected function match_request_to_handler($request) { return ["/wp/v2/posts/(?P<id>[\\\\d]+)", []]; }
        }
        (new WP_REST_Server())->dispatch(new WP_REST_Request("/wp/v2/posts/12"));'],
    'wordpress rest (no matcher)' => ['/wp-json/wp/v2/posts/12', '<?php
        class WP_REST_Request { protected $route; public function __construct($r) { $this->route = $r; } }
        class WP_REST_Server { public function dispatch($request) { return 1; } }
        (new WP_REST_Server())->dispatch(new WP_REST_Request("/wp/v2/posts/12"));'],
    'codeigniter3' => ['/index.php/admin/welcome/show/5', '<?php
        class CI_Router { public $class = ""; public $method = "index"; public $directory = "";
            public function _set_routing() { $this->class = "welcome"; $this->method = "show"; $this->directory = "admin/"; } }
        (new CI_Router())->_set_routing();'],
    'codeigniter4' => ['/products/5/red', '<?php namespace CodeIgniter\Router;
        class Router { protected $matchedRoute; public function handle($uri) { $this->matchedRoute = ["products/([0-9]+)/(:any)", "X"]; return "c"; } }
        (new Router())->handle("products/5/red");'],
    'yii2' => ['/index.php?r=post/view&id=3', '<?php
        namespace yii\base { class Module { public $defaultRoute = "site"; public function runAction($route, $params = []) { return 1; } } class Application extends Module {} }
        namespace yii\web { class Application extends \yii\base\Application {} }
        namespace { (new yii\base\Module())->runAction("ignored/module"); (new yii\web\Application())->runAction("post/view", ["id" => 3]); }'],
    'yii2 default route' => ['/', '<?php
        namespace yii\base { class Module { public $defaultRoute = "site"; public function runAction($route, $params = []) { return 1; } } class Application extends Module {} }
        namespace yii\web { class Application extends \yii\base\Application {} }
        namespace { (new yii\web\Application())->runAction(""); }'],
    'set_transaction_name wins' => ['/x', '<?php namespace Slim; class Route { protected $pattern = "/x"; public function run($a, $b) {} }
        \openlog\set_transaction_name("custom/name"); (new Route())->run(1, 2);'],
];
foreach ($cases as $label => $case) {
    $r = ol_run($case[1], ['cgi' => true, 'server' => ['REQUEST_URI' => $case[0]]]);
    $t = ol_one_trace($r);
    $root = $t ? ol_root($t) : ['name' => '?', 'attrs' => []];
    echo str_pad($label, 28), $root['name'], isset($root['attrs']['http.route']) ? '  http.route=' . $root['attrs']['http.route'] : '', trim($r['out']) !== '' ? "  OUT: " . trim($r['out']) : '', "\n";
}
--EXPECT--
slim3                       GET /hello/{name}  http.route=/hello/{name}
slim4                       GET /books/{id:[0-9]+}  http.route=/books/{id:[0-9]+}
wordpress template          GET template/single.php  http.route=template/single.php
wordpress rest              GET /wp-json/wp/v2/posts/{id}  http.route=/wp-json/wp/v2/posts/{id}
wordpress rest (no matcher) GET /wp-json/wp/v2/posts/{id}  http.route=/wp-json/wp/v2/posts/{id}
codeigniter3                GET /admin/welcome/show  http.route=/admin/welcome/show
codeigniter4                GET /products/{param}/{any}  http.route=/products/{param}/{any}
yii2                        GET /post/view  http.route=/post/view
yii2 default route          GET /site  http.route=/site
set_transaction_name wins   GET custom/name

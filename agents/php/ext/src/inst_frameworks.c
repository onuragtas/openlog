/*
 * Framework transaction naming and framework exception reporting.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Route priorities: explicit openlog\set_transaction_name 1000 > framework route template 50 > WordPress REST 45/40
 * > WordPress template 30 > plain PHP (normalized path, applied at request end).
 * Every handler only reads engine data (arguments, $this, properties) and never calls PHP code.
 */
#include "ol.h"

#define OL_PRIO_ROUTE 50

static bool ol_str_zv(zval *z)
{
	return z && Z_TYPE_P(z) == IS_STRING;
}

static void ol_route_slash(const char *s, size_t len, int prio)
{
	char buf[1100];
	if (len > 1024) {
		len = 1024;
	}
	if (len > 0 && s[0] == '/') {
		ol_set_route(s, len, prio, true);
		return;
	}
	buf[0] = '/';
	memcpy(buf + 1, s, len);
	ol_set_route(buf, len + 1, prio, true);
}

/* ---------------- Laravel ---------------- */

/* Illuminate\Routing\Router::runRoute(Request $request, Route $route): Route::$uri is the template. */
static void laravel_run_route(zend_execute_data *ex, const ol_hook *h)
{
	zval *route = ol_arg(ex, 2), *uri;
	if (route == NULL || Z_TYPE_P(route) != IS_OBJECT) {
		return;
	}
	uri = ol_prop(Z_OBJ_P(route), ZEND_STRL("uri"));
	if (ol_str_zv(uri)) {
		ol_route_slash(Z_STRVAL_P(uri), Z_STRLEN_P(uri), OL_PRIO_ROUTE);
	}
}

/* Laravel's internal $internalDontReport list (older versions without reportThrowable). */
static const char *laravel_dont_report[] = {
	"illuminate\\auth\\authenticationexception",
	"illuminate\\auth\\access\\authorizationexception",
	"illuminate\\database\\eloquent\\modelnotfoundexception",
	"illuminate\\database\\multiplerecordsfoundexception",
	"illuminate\\database\\recordsnotfoundexception",
	"illuminate\\http\\exceptions\\httpresponseexception",
	"illuminate\\session\\tokenmismatchexception",
	"illuminate\\validation\\validationexception",
	"symfony\\component\\httpkernel\\exception\\httpexceptioninterface",
	NULL
};

/* Handler::report(Throwable $e) (arg 0) and Handler::reportThrowable(Throwable $e) (arg 1, Laravel >= 8.x). */
static void laravel_report(zend_execute_data *ex, const ol_hook *h)
{
	zval *e = ol_arg(ex, 1), *self = ol_this(ex);
	int i;
	if (e == NULL || Z_TYPE_P(e) != IS_OBJECT || !instanceof_function(Z_OBJCE_P(e), zend_ce_throwable)) {
		return;
	}
	if (h->arg == 0) {
		if (self && zend_hash_str_exists(&Z_OBJCE_P(self)->function_table, ZEND_STRL("reportthrowable"))) {
			return; /* reportThrowable is only called for reportable exceptions */
		}
		for (i = 0; laravel_dont_report[i]; i++) {
			if (ol_instanceof(Z_OBJ_P(e), laravel_dont_report[i], strlen(laravel_dont_report[i]))) {
				return;
			}
		}
	}
	ol_report_exception(Z_OBJ_P(e));
}

/* ---------------- Symfony ---------------- */

/*
 * Symfony does not keep the path template after matching (compiled matchers only have regexes), so the template is
 * reconstructed: every route parameter value in the path is replaced by "{name}". Static routes are the path.
 */
static size_t sf_template(char *out, size_t cap, const char *path, size_t plen, zval *params)
{
	char work[1100];
	size_t len;
	zend_string *key;
	zval *val;

	if (plen >= sizeof(work)) {
		plen = sizeof(work) - 1;
	}
	memcpy(work, path, plen);
	len = plen;
	work[len] = '\0';
	if (params && Z_TYPE_P(params) == IS_ARRAY) {
		ZEND_HASH_FOREACH_STR_KEY_VAL(Z_ARRVAL_P(params), key, val) {
			char sval[256];
			size_t vl, i;
			ZVAL_DEREF(val);
			if (key == NULL || ZSTR_LEN(key) == 0 || ZSTR_VAL(key)[0] == '_' || ZSTR_LEN(key) > 64) {
				continue;
			}
			if (Z_TYPE_P(val) == IS_STRING) {
				vl = Z_STRLEN_P(val);
				if (vl == 0 || vl >= sizeof(sval)) continue;
				memcpy(sval, Z_STRVAL_P(val), vl);
			} else if (Z_TYPE_P(val) == IS_LONG) {
				vl = (size_t) snprintf(sval, sizeof(sval), ZEND_LONG_FMT, Z_LVAL_P(val));
			} else {
				continue;
			}
			/* prefer a whole segment, then any occurrence outside an existing placeholder */
			for (int pass = 0; pass < 2; pass++) {
				bool done = false;
				int depth = 0;
				for (i = 0; i + vl <= len; i++) {
					if (work[i] == '{') depth++;
					if (work[i] == '}') depth--;
					if (depth > 0 || memcmp(work + i, sval, vl) != 0) continue;
					if (pass == 0 && !((i == 0 || work[i - 1] == '/') && (i + vl == len || work[i + vl] == '/'))) continue;
					if (len - vl + ZSTR_LEN(key) + 2 >= sizeof(work)) break;
					memmove(work + i + ZSTR_LEN(key) + 2, work + i + vl, len - i - vl + 1);
					work[i] = '{';
					memcpy(work + i + 1, ZSTR_VAL(key), ZSTR_LEN(key));
					work[i + 1 + ZSTR_LEN(key)] = '}';
					len = len - vl + ZSTR_LEN(key) + 2;
					done = true;
					break;
				}
				if (done) break;
			}
		} ZEND_HASH_FOREACH_END();
	}
	if (len >= cap) {
		len = cap - 1;
	}
	memcpy(out, work, len);
	out[len] = '\0';
	return len;
}

/* ControllerResolver::getController(Request $request): routing is done, the controller not yet called. */
static void symfony_get_controller(zend_execute_data *ex, const ol_hook *h)
{
	zval *req = ol_arg(ex, 1), *attrs, *params, *name, *rparams, *pi;
	ol_node *root = ol_node_at(0);
	char tpl[1100];
	size_t len;

	if (OLG(route_prio) >= OL_PRIO_ROUTE || req == NULL || Z_TYPE_P(req) != IS_OBJECT) {
		return; /* the main request is resolved first; sub-requests keep its name */
	}
	attrs = ol_prop(Z_OBJ_P(req), ZEND_STRL("attributes"));
	if (attrs == NULL || Z_TYPE_P(attrs) != IS_OBJECT) {
		return;
	}
	params = ol_prop(Z_OBJ_P(attrs), ZEND_STRL("parameters"));
	name = ol_array_get(params, ZEND_STRL("_route"));
	if (!ol_str_zv(name)) {
		return;
	}
	rparams = ol_array_get(params, ZEND_STRL("_route_params"));
	pi = ol_prop(Z_OBJ_P(req), ZEND_STRL("pathInfo"));
	if (ol_str_zv(pi)) {
		len = sf_template(tpl, sizeof(tpl), Z_STRVAL_P(pi), Z_STRLEN_P(pi), rparams);
	} else {
		ol_attr *p = ol_attr_find(root, "url.path");
		if (p == NULL) {
			return;
		}
		len = sf_template(tpl, sizeof(tpl), p->v.s.p, p->v.s.len, rparams);
	}
	ol_route_slash(tpl, len, OL_PRIO_ROUTE);
	ol_attr_str(root, "openlog.php.route_name", Z_STRVAL_P(name), Z_STRLEN_P(name));
}

/* HttpKernel::handleThrowable / handleException(Throwable $e, ...): kernel.exception */
static void symfony_exception(zend_execute_data *ex, const ol_hook *h)
{
	zval *e = ol_arg(ex, 1);
	if (e == NULL || Z_TYPE_P(e) != IS_OBJECT || !instanceof_function(Z_OBJCE_P(e), zend_ce_throwable)) {
		return;
	}
	if (ol_instanceof(Z_OBJ_P(e), ZEND_STRL("symfony\\component\\httpkernel\\exception\\httpexceptioninterface"))) {
		zval *code = ol_prop(Z_OBJ_P(e), ZEND_STRL("statusCode"));
		if (code && Z_TYPE_P(code) == IS_LONG && Z_LVAL_P(code) < 500) {
			return;
		}
	}
	ol_report_exception(Z_OBJ_P(e));
}

/* ---------------- Slim 3 / 4 ---------------- */

/* Slim\Route::run (3) / Slim\Routing\Route::run (4): $this->pattern */
static void slim_route_run(zend_execute_data *ex, const ol_hook *h)
{
	zval *self = ol_this(ex), *pattern;
	if (self == NULL) {
		return;
	}
	pattern = ol_prop(Z_OBJ_P(self), ZEND_STRL("pattern"));
	if (ol_str_zv(pattern)) {
		ol_route_slash(Z_STRVAL_P(pattern), Z_STRLEN_P(pattern), OL_PRIO_ROUTE);
	}
}

/* ---------------- WordPress ---------------- */

static void wp_rest_route(const char *route, size_t len, bool pattern, int prio)
{
	char tpl[1100], full[1200];
	int n;
	if (pattern) {
		len = ol_route_from_pattern(tpl, sizeof(tpl), route, len);
	} else {
		len = ol_normalize_path(tpl, sizeof(tpl), route, len);
	}
	n = snprintf(full, sizeof(full), "/wp-json%s%s", (len && tpl[0] == '/') ? "" : "/", tpl);
	if (n > 0) {
		ol_set_route(full, (size_t) n < sizeof(full) ? (size_t) n : sizeof(full) - 1, prio, true);
	}
}

/* WP_REST_Server::dispatch(WP_REST_Request $request): fallback = normalized REST path */
static void wp_rest_dispatch(zend_execute_data *ex, const ol_hook *h)
{
	zval *req = ol_arg(ex, 1), *route;
	if (req == NULL || Z_TYPE_P(req) != IS_OBJECT) {
		return;
	}
	route = ol_prop(Z_OBJ_P(req), ZEND_STRL("route"));
	if (ol_str_zv(route)) {
		wp_rest_route(Z_STRVAL_P(route), Z_STRLEN_P(route), false, 40);
	}
}

/* WP_REST_Server::match_request_to_handler(): returns [$route_regex, $handler] */
static void wp_rest_match_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	zval *route;
	if (rv == NULL) {
		return;
	}
	ZVAL_DEREF(rv);
	if (Z_TYPE_P(rv) != IS_ARRAY) {
		return;
	}
	route = zend_hash_index_find(Z_ARRVAL_P(rv), 0);
	if (route && Z_TYPE_P(route) == IS_STRING) {
		wp_rest_route(Z_STRVAL_P(route), Z_STRLEN_P(route), true, 45);
	}
}

/* apply_filters('template_include', $template): the template WordPress renders */
static void wp_apply_filters_begin(zend_execute_data *ex, const ol_hook *h)
{
	zval *tag = ol_arg(ex, 1);
	if (tag && Z_TYPE_P(tag) == IS_STRING && Z_STRLEN_P(tag) == sizeof("template_include") - 1 &&
			memcmp(Z_STRVAL_P(tag), "template_include", sizeof("template_include") - 1) == 0) {
		OLG(wp_tpl_ex) = ex;
	}
}

static void wp_apply_filters_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	const char *base;
	char buf[300];
	int n;
	if (ex != OLG(wp_tpl_ex)) {
		return;
	}
	OLG(wp_tpl_ex) = NULL;
	if (rv == NULL) {
		return;
	}
	ZVAL_DEREF(rv);
	if (Z_TYPE_P(rv) != IS_STRING || Z_STRLEN_P(rv) == 0) {
		return;
	}
	base = strrchr(Z_STRVAL_P(rv), '/');
	base = base ? base + 1 : Z_STRVAL_P(rv);
	if (strcmp(base, "template-canvas.php") == 0) {
		/* block themes render every page through the canvas: name by the resolved block template ("theme//single") */
		zval *id = zend_hash_str_find(&EG(symbol_table), ZEND_STRL("_wp_current_template_id"));
		if (id && Z_TYPE_P(id) == IS_INDIRECT) {
			id = Z_INDIRECT_P(id);
		}
		if (id) {
			ZVAL_DEREF(id);
		}
		if (id && Z_TYPE_P(id) == IS_STRING && Z_STRLEN_P(id) > 0) {
			const char *slug = strstr(Z_STRVAL_P(id), "//");
			base = slug ? slug + 2 : Z_STRVAL_P(id);
		}
	}
	n = snprintf(buf, sizeof(buf), "template/%s", base);
	if (n > 0) {
		ol_set_route(buf, (size_t) n < sizeof(buf) ? (size_t) n : sizeof(buf) - 1, 30, true);
	}
}

/* ---------------- CodeIgniter 3 / 4 ---------------- */

/* CI_Router::_set_routing(): $this->directory, $this->class, $this->method */
static void ci3_set_routing_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	zval *self = ol_this(ex), *dir, *cls, *method;
	char buf[600];
	int n;
	if (self == NULL) {
		return;
	}
	dir = ol_prop(Z_OBJ_P(self), ZEND_STRL("directory"));
	cls = ol_prop(Z_OBJ_P(self), ZEND_STRL("class"));
	method = ol_prop(Z_OBJ_P(self), ZEND_STRL("method"));
	if (!ol_str_zv(cls) || Z_STRLEN_P(cls) == 0) {
		return;
	}
	n = snprintf(buf, sizeof(buf), "/%s%s/%s", ol_str_zv(dir) ? Z_STRVAL_P(dir) : "", Z_STRVAL_P(cls),
		ol_str_zv(method) && Z_STRLEN_P(method) ? Z_STRVAL_P(method) : "index");
	if (n > 0) {
		ol_set_route(buf, (size_t) n < sizeof(buf) ? (size_t) n : sizeof(buf) - 1, OL_PRIO_ROUTE, true);
	}
}

/* CodeIgniter\Router\Router::handle(): $this->matchedRoute = [$routeKey, $handler] */
static void ci4_handle_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	zval *self = ol_this(ex), *matched, *from;
	char tpl[1100];
	size_t len;
	if (self == NULL) {
		return;
	}
	matched = ol_prop(Z_OBJ_P(self), ZEND_STRL("matchedRoute"));
	if (matched == NULL || Z_TYPE_P(matched) != IS_ARRAY) {
		return;
	}
	from = zend_hash_index_find(Z_ARRVAL_P(matched), 0);
	if (from == NULL || Z_TYPE_P(from) != IS_STRING) {
		return;
	}
	len = ol_route_from_pattern(tpl, sizeof(tpl), Z_STRVAL_P(from), Z_STRLEN_P(from));
	ol_route_slash(tpl, len, OL_PRIO_ROUTE);
}

/* ---------------- Yii 2 ---------------- */

/* yii\base\Module::runAction($route, $params) on the yii\web\Application: "controller/action" */
static void yii_run_action(zend_execute_data *ex, const ol_hook *h)
{
	zval *self = ol_this(ex), *route;
	if (OLG(route_prio) >= OL_PRIO_ROUTE || self == NULL ||
			!ol_instanceof(Z_OBJ_P(self), ZEND_STRL("yii\\web\\application"))) {
		return;
	}
	route = ol_arg(ex, 1);
	if (!ol_str_zv(route) || Z_STRLEN_P(route) == 0) {
		route = ol_prop(Z_OBJ_P(self), ZEND_STRL("defaultRoute"));
	}
	if (ol_str_zv(route) && Z_STRLEN_P(route) > 0) {
		ol_route_slash(Z_STRVAL_P(route), Z_STRLEN_P(route), OL_PRIO_ROUTE);
	}
}

const ol_hook ol_hooks_frameworks[] = {
	{"illuminate\\routing\\router::runroute", laravel_run_route, NULL, 0, 0},
	{"illuminate\\foundation\\exceptions\\handler::report", laravel_report, NULL, 0, 0},
	{"illuminate\\foundation\\exceptions\\handler::reportthrowable", laravel_report, NULL, 1, 0},
	{"symfony\\component\\httpkernel\\controller\\controllerresolver::getcontroller", symfony_get_controller, NULL, 0, 0},
	{"symfony\\component\\httpkernel\\httpkernel::handlethrowable", symfony_exception, NULL, 0, 0},
	{"symfony\\component\\httpkernel\\httpkernel::handleexception", symfony_exception, NULL, 0, 0},
	{"slim\\route::run", slim_route_run, NULL, 0, 0},
	{"slim\\routing\\route::run", slim_route_run, NULL, 0, 0},
	{"wp_rest_server::dispatch", wp_rest_dispatch, NULL, 0, 0},
	{"wp_rest_server::match_request_to_handler", NULL, wp_rest_match_end, 0, 0},
	{"apply_filters", wp_apply_filters_begin, wp_apply_filters_end, 0, 0},
	{"ci_router::_set_routing", NULL, ci3_set_routing_end, 0, 0},
	{"codeigniter\\router\\router::handle", NULL, ci4_handle_end, 0, 0},
	{"yii\\base\\module::runaction", yii_run_action, NULL, 0, 0},
	{NULL, NULL, NULL, 0, 0}
};

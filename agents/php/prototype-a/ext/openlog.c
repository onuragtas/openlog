/*
 * openlog PHP agent spike (option A): a minimal PHP 8 Observer API extension.
 * SPDX-License-Identifier: Apache-2.0
 *
 * What it does (deliberately small, see agents/php/docs/decision.md):
 *   - one server span per request (RINIT..RSHUTDOWN), W3C traceparent is honoured
 *   - Laravel route template via Illuminate\Routing\Router::runRoute, Symfony route name via HttpKernel::handleRaw
 *   - client spans for PDOStatement::execute, PDO::exec/query and phpredis Redis methods
 *   - exceptions reported by Laravel's exception handler, fatal errors via the error observer
 *   - at request end the spans are serialized once into a fixed buffer and sent as ONE non-blocking
 *     unix datagram to a local forwarder; if the forwarder is absent or slow the datagram is dropped (fail-open)
 *
 * Crash-safety rules followed here: no heap allocation in the hot path (fixed span table + string arena in
 * module globals), no calls back into userland, bounded everything (spans, stack, strings, datagram size),
 * observers are only attached to the handful of functions we care about (decided once per function).
 */
#ifdef HAVE_CONFIG_H
# include "config.h"
#endif

#include "php.h"
#include "php_ini.h"
#include "SAPI.h"
#include "ext/standard/info.h"
#include "ext/pdo/php_pdo_driver.h"
#include "zend_observer.h"
#include "zend_exceptions.h"
#include "php_openlog.h"

#include <ctype.h>
#include <errno.h>
#include <stddef.h>
#include <string.h>
#include <sys/random.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <time.h>
#include <unistd.h>

#define OL_MAX_SPANS   256
#define OL_MAX_STACK   128
#define OL_MAX_ATTRS   8
#define OL_ARENA_SIZE  32768
#define OL_BUF_SIZE    60000
#define OL_NO_SPAN     UINT32_MAX

/* OTLP SpanKind values */
#define OL_KIND_SERVER 2
#define OL_KIND_CLIENT 3
/* OTLP status codes */
#define OL_STATUS_ERROR 2

typedef struct {
	const char *key;
	const char *str;
	zend_long num;
	bool is_num;
} ol_attr;

typedef struct {
	uint64_t span_id;
	uint64_t parent_id;
	uint64_t start_unix_ns;
	uint64_t start_mono_ns;
	uint64_t duration_ns;
	const char *name;
	const char *ex_type;
	const char *ex_msg;
	const char *ex_where;
	uint8_t kind;
	uint8_t status;
	uint8_t nattrs;
	ol_attr attrs[OL_MAX_ATTRS];
} ol_span;

ZEND_BEGIN_MODULE_GLOBALS(openlog)
	bool enabled;
	bool cli_enabled;
	char *socket_path;
	char *service_name;

	bool active;
	uint8_t trace_id[16];
	uint64_t remote_parent;
	const char *route;    /* http.route template (Laravel) */
	const char *tx_name;  /* framework route name (Symfony) */

	ol_span spans[OL_MAX_SPANS];
	uint32_t nspans;
	uint32_t stack[OL_MAX_STACK];
	uint32_t depth;
	uint32_t overflow;
	uint32_t dropped;

	char arena[OL_ARENA_SIZE];
	size_t arena_used;
	char out[OL_BUF_SIZE];

	uint64_t rng;
	int fd;
	bool addr_ready;
	struct sockaddr_un addr;
	socklen_t addrlen;

	zend_long stat_sent;
	zend_long stat_send_failed;
	zend_long stat_spans_dropped;
ZEND_END_MODULE_GLOBALS(openlog)

ZEND_DECLARE_MODULE_GLOBALS(openlog)
#define OLG(v) ZEND_MODULE_GLOBALS_ACCESSOR(openlog, v)

PHP_INI_BEGIN()
	STD_PHP_INI_BOOLEAN("openlog.enabled", "1", PHP_INI_SYSTEM, OnUpdateBool, enabled, zend_openlog_globals, openlog_globals)
	STD_PHP_INI_BOOLEAN("openlog.cli_enabled", "0", PHP_INI_SYSTEM, OnUpdateBool, cli_enabled, zend_openlog_globals, openlog_globals)
	STD_PHP_INI_ENTRY("openlog.socket", "/run/openlog/php.sock", PHP_INI_SYSTEM, OnUpdateString, socket_path, zend_openlog_globals, openlog_globals)
	STD_PHP_INI_ENTRY("openlog.service_name", "php-app", PHP_INI_ALL, OnUpdateString, service_name, zend_openlog_globals, openlog_globals)
PHP_INI_END()

/* ---------- small helpers (no allocation) ---------- */

static uint64_t ol_mono_ns(void)
{
	struct timespec ts;
	clock_gettime(CLOCK_MONOTONIC, &ts);
	return (uint64_t) ts.tv_sec * 1000000000ULL + (uint64_t) ts.tv_nsec;
}

static uint64_t ol_unix_ns(void)
{
	struct timespec ts;
	clock_gettime(CLOCK_REALTIME, &ts);
	return (uint64_t) ts.tv_sec * 1000000000ULL + (uint64_t) ts.tv_nsec;
}

/* xorshift64*, seeded lazily per process (after the FPM fork) from getrandom. */
static uint64_t ol_rand64(void)
{
	uint64_t x = OLG(rng);
	if (x == 0) {
		if (getrandom(&x, sizeof(x), GRND_NONBLOCK) != (ssize_t) sizeof(x)) {
			x = ol_mono_ns() ^ ((uint64_t) getpid() << 32);
		}
		if (x == 0) {
			x = 0x9E3779B97F4A7C15ULL;
		}
	}
	x ^= x >> 12;
	x ^= x << 25;
	x ^= x >> 27;
	OLG(rng) = x;
	return x * 0x2545F4914F6CDD1DULL;
}

/* Copies a string into the per-request arena, truncated to max bytes; "" when the arena is full. */
static const char *ol_copy(const char *s, size_t len, size_t max)
{
	size_t avail;
	char *d;
	if (s == NULL) {
		return NULL;
	}
	if (len > max) {
		len = max;
	}
	avail = OL_ARENA_SIZE - OLG(arena_used);
	if (avail <= 1) {
		return "";
	}
	if (len > avail - 1) {
		len = avail - 1;
	}
	d = OLG(arena) + OLG(arena_used);
	memcpy(d, s, len);
	d[len] = '\0';
	OLG(arena_used) += len + 1;
	return d;
}

static void ol_attr_str(ol_span *s, const char *key, const char *val)
{
	if (s == NULL || val == NULL || s->nattrs >= OL_MAX_ATTRS) {
		return;
	}
	s->attrs[s->nattrs].key = key;
	s->attrs[s->nattrs].str = val;
	s->attrs[s->nattrs].is_num = false;
	s->nattrs++;
}

static void ol_attr_num(ol_span *s, const char *key, zend_long val)
{
	if (s == NULL || s->nattrs >= OL_MAX_ATTRS) {
		return;
	}
	s->attrs[s->nattrs].key = key;
	s->attrs[s->nattrs].str = NULL;
	s->attrs[s->nattrs].num = val;
	s->attrs[s->nattrs].is_num = true;
	s->nattrs++;
}

static uint64_t ol_current_parent(void)
{
	uint32_t i = OLG(depth);
	while (i > 0) {
		uint32_t idx = OLG(stack)[--i];
		if (idx != OL_NO_SPAN) {
			return OLG(spans)[idx].span_id;
		}
	}
	return OLG(remote_parent);
}

/* Opens a span. Always keeps begin/end balanced, even when the span table or stack is full. */
static ol_span *ol_push(const char *name, uint8_t kind)
{
	ol_span *s = NULL;
	uint32_t idx = OL_NO_SPAN;

	if (!OLG(active)) {
		return NULL;
	}
	if (OLG(depth) >= OL_MAX_STACK) {
		OLG(overflow)++;
		return NULL;
	}
	if (OLG(nspans) < OL_MAX_SPANS) {
		idx = OLG(nspans)++;
		s = &OLG(spans)[idx];
		memset(s, 0, offsetof(ol_span, attrs));
		s->span_id = ol_rand64();
		s->parent_id = ol_current_parent();
		s->name = name;
		s->kind = kind;
		s->start_unix_ns = ol_unix_ns();
		s->start_mono_ns = ol_mono_ns();
	} else {
		OLG(dropped)++;
	}
	OLG(stack)[OLG(depth)++] = idx;
	return s;
}

static ol_span *ol_pop(void)
{
	uint32_t idx;
	ol_span *s;

	if (!OLG(active)) {
		return NULL;
	}
	if (OLG(overflow) > 0) {
		OLG(overflow)--;
		return NULL;
	}
	if (OLG(depth) <= 1) { /* never pop the root span from an observer */
		return NULL;
	}
	idx = OLG(stack)[--OLG(depth)];
	if (idx == OL_NO_SPAN) {
		return NULL;
	}
	s = &OLG(spans)[idx];
	s->duration_ns = ol_mono_ns() - s->start_mono_ns;
	return s;
}

static void ol_set_exception(ol_span *s, zend_object *ex)
{
	zval rv, *zv;
	char where[512];
	const char *file = NULL;
	zend_long line = 0;

	if (s == NULL || ex == NULL) {
		return;
	}
	s->status = OL_STATUS_ERROR;
	s->ex_type = ol_copy(ZSTR_VAL(ex->ce->name), ZSTR_LEN(ex->ce->name), 256);
	zv = zend_read_property_ex(ex->ce, ex, ZSTR_KNOWN(ZEND_STR_MESSAGE), 1, &rv);
	if (zv && Z_TYPE_P(zv) == IS_STRING) {
		s->ex_msg = ol_copy(Z_STRVAL_P(zv), Z_STRLEN_P(zv), 1024);
	}
	zv = zend_read_property_ex(ex->ce, ex, ZSTR_KNOWN(ZEND_STR_FILE), 1, &rv);
	if (zv && Z_TYPE_P(zv) == IS_STRING) {
		file = Z_STRVAL_P(zv);
	}
	zv = zend_read_property_ex(ex->ce, ex, ZSTR_KNOWN(ZEND_STR_LINE), 1, &rv);
	if (zv && Z_TYPE_P(zv) == IS_LONG) {
		line = Z_LVAL_P(zv);
	}
	if (file) {
		int n = snprintf(where, sizeof(where), "%s:" ZEND_LONG_FMT, file, line);
		if (n > 0) {
			s->ex_where = ol_copy(where, (size_t) n < sizeof(where) ? (size_t) n : sizeof(where) - 1, sizeof(where));
		}
	}
}

/* First SQL keyword, upper-cased, as the span name ("SELECT"). */
static const char *ol_sql_op(const char *sql, size_t len)
{
	char op[16];
	size_t i = 0, n = 0;
	while (i < len && isspace((unsigned char) sql[i])) {
		i++;
	}
	while (i < len && n < sizeof(op) - 1 && isalpha((unsigned char) sql[i])) {
		op[n++] = (char) toupper((unsigned char) sql[i++]);
	}
	return n ? ol_copy(op, n, sizeof(op)) : "SQL";
}

/* ---------- observer handlers ---------- */

static void ol_db_span(const char *driver, const char *sql, size_t sql_len)
{
	ol_span *s = ol_push(NULL, OL_KIND_CLIENT);
	if (s == NULL) {
		return;
	}
	s->name = sql ? ol_sql_op(sql, sql_len) : "SQL";
	ol_attr_str(s, "db.system.name", driver ? driver : "other_sql");
	if (sql) {
		ol_attr_str(s, "db.query.text", ol_copy(sql, sql_len, 2048));
	}
}

static void ol_pdo_stmt_begin(zend_execute_data *ex)
{
	pdo_stmt_t *stmt;
	if (!OLG(active)) {
		return;
	}
	if (Z_TYPE(ex->This) != IS_OBJECT) {
		ol_push("SQL", OL_KIND_CLIENT);
		return;
	}
	stmt = Z_PDO_STMT_P(&ex->This);
	ol_db_span(stmt->dbh && stmt->dbh->driver ? stmt->dbh->driver->driver_name : NULL,
		stmt->query_string ? ZSTR_VAL(stmt->query_string) : NULL,
		stmt->query_string ? ZSTR_LEN(stmt->query_string) : 0);
}

static void ol_pdo_dbh_begin(zend_execute_data *ex)
{
	pdo_dbh_t *dbh;
	zval *sql;
	if (!OLG(active)) {
		return;
	}
	if (Z_TYPE(ex->This) != IS_OBJECT || ZEND_CALL_NUM_ARGS(ex) < 1) {
		ol_push("SQL", OL_KIND_CLIENT);
		return;
	}
	dbh = Z_PDO_DBH_P(&ex->This);
	sql = ZEND_CALL_ARG(ex, 1);
	ol_db_span(dbh->driver ? dbh->driver->driver_name : NULL,
		Z_TYPE_P(sql) == IS_STRING ? Z_STRVAL_P(sql) : NULL,
		Z_TYPE_P(sql) == IS_STRING ? Z_STRLEN_P(sql) : 0);
}

static void ol_redis_begin(zend_execute_data *ex)
{
	char op[32];
	size_t n, i;
	zend_string *fn = ex->func->common.function_name;
	ol_span *s = ol_push(NULL, OL_KIND_CLIENT);
	if (s == NULL) {
		return;
	}
	n = ZSTR_LEN(fn) < sizeof(op) - 1 ? ZSTR_LEN(fn) : sizeof(op) - 1;
	for (i = 0; i < n; i++) {
		op[i] = (char) toupper((unsigned char) ZSTR_VAL(fn)[i]);
	}
	s->name = ol_copy(op, n, sizeof(op));
	ol_attr_str(s, "db.system.name", "redis");
	ol_attr_str(s, "db.operation.name", s->name);
}

static void ol_client_end(zend_execute_data *ex, zval *retval)
{
	ol_span *s = ol_pop();
	if (s == NULL) {
		return;
	}
	if (EG(exception)) {
		ol_set_exception(s, EG(exception));
	} else if (retval && Z_TYPE_P(retval) == IS_FALSE) {
		s->status = OL_STATUS_ERROR;
	}
}

/* Illuminate\Routing\Router::runRoute(Request $request, Route $route): Route::$uri is the route template. */
static void ol_laravel_route_begin(zend_execute_data *ex)
{
	zval rv, *route, *uri;
	char buf[512];
	size_t len;

	if (!OLG(active) || ZEND_CALL_NUM_ARGS(ex) < 2) {
		return;
	}
	route = ZEND_CALL_ARG(ex, 2);
	if (Z_TYPE_P(route) != IS_OBJECT) {
		return;
	}
	uri = zend_read_property(Z_OBJCE_P(route), Z_OBJ_P(route), "uri", sizeof("uri") - 1, 1, &rv);
	if (uri == NULL || Z_TYPE_P(uri) != IS_STRING) {
		return;
	}
	len = Z_STRLEN_P(uri);
	if (len > sizeof(buf) - 2) {
		len = sizeof(buf) - 2;
	}
	if (len > 0 && Z_STRVAL_P(uri)[0] == '/') {
		memcpy(buf, Z_STRVAL_P(uri), len);
	} else {
		buf[0] = '/';
		memcpy(buf + 1, Z_STRVAL_P(uri), len);
		len++;
	}
	OLG(route) = ol_copy(buf, len, sizeof(buf));
}

/* Illuminate\Foundation\Exceptions\Handler::report(Throwable $e) */
static void ol_laravel_report_begin(zend_execute_data *ex)
{
	zval *e;
	if (!OLG(active) || ZEND_CALL_NUM_ARGS(ex) < 1 || OLG(nspans) == 0) {
		return;
	}
	e = ZEND_CALL_ARG(ex, 1);
	if (Z_TYPE_P(e) == IS_OBJECT && instanceof_function(Z_OBJCE_P(e), zend_ce_throwable)) {
		ol_set_exception(&OLG(spans)[0], Z_OBJ_P(e));
	}
}

/* Symfony\Component\HttpKernel\HttpKernel::handleRaw(Request $request, int $type): read _route after routing. */
static void ol_symfony_handle_end(zend_execute_data *ex, zval *retval)
{
	zval rv1, rv2, *req, *type, *attrs, *params, *name;
	if (!OLG(active) || ZEND_CALL_NUM_ARGS(ex) < 2) {
		return;
	}
	type = ZEND_CALL_ARG(ex, 2);
	req = ZEND_CALL_ARG(ex, 1);
	if (Z_TYPE_P(type) != IS_LONG || Z_LVAL_P(type) != 1 || Z_TYPE_P(req) != IS_OBJECT) {
		return;
	}
	attrs = zend_read_property(Z_OBJCE_P(req), Z_OBJ_P(req), "attributes", sizeof("attributes") - 1, 1, &rv1);
	if (attrs == NULL || Z_TYPE_P(attrs) != IS_OBJECT) {
		return;
	}
	params = zend_read_property(Z_OBJCE_P(attrs), Z_OBJ_P(attrs), "parameters", sizeof("parameters") - 1, 1, &rv2);
	if (params == NULL || Z_TYPE_P(params) != IS_ARRAY) {
		return;
	}
	name = zend_hash_str_find(Z_ARRVAL_P(params), "_route", sizeof("_route") - 1);
	if (name && Z_TYPE_P(name) == IS_STRING) {
		OLG(tx_name) = ol_copy(Z_STRVAL_P(name), Z_STRLEN_P(name), 256);
	}
}

static bool ol_redis_skip(zend_string *fn)
{
	static const char *skip[] = {"__construct", "__destruct", "connect", "pconnect", "open", "popen", "close",
		"setoption", "getoption", "isconnected", "gethost", "getport", "getlasterror", "clearlasterror", NULL};
	for (int i = 0; skip[i]; i++) {
		if (zend_binary_strcasecmp(ZSTR_VAL(fn), ZSTR_LEN(fn), skip[i], strlen(skip[i])) == 0) {
			return true;
		}
	}
	return false;
}

#define OL_EQ(zs, lit) zend_string_equals_literal_ci(zs, lit)

/* Called once per function (result cached by the engine): decide whether to observe it at all. */
static zend_observer_fcall_handlers ol_observer_init(zend_execute_data *ex)
{
	zend_observer_fcall_handlers h = {NULL, NULL};
	zend_function *fn = ex->func;
	zend_string *cls, *name;

	if (fn->common.function_name == NULL || fn->common.scope == NULL) {
		return h;
	}
	cls = fn->common.scope->name;
	name = fn->common.function_name;

	if (OL_EQ(cls, "PDOStatement")) {
		if (OL_EQ(name, "execute")) {
			h.begin = ol_pdo_stmt_begin;
			h.end = ol_client_end;
		}
	} else if (OL_EQ(cls, "PDO")) {
		if (OL_EQ(name, "exec") || OL_EQ(name, "query")) {
			h.begin = ol_pdo_dbh_begin;
			h.end = ol_client_end;
		}
	} else if (OL_EQ(cls, "Redis")) {
		if (!ol_redis_skip(name)) {
			h.begin = ol_redis_begin;
			h.end = ol_client_end;
		}
	} else if (OL_EQ(cls, "Illuminate\\Routing\\Router")) {
		if (OL_EQ(name, "runRoute")) {
			h.begin = ol_laravel_route_begin;
		}
	} else if (OL_EQ(cls, "Illuminate\\Foundation\\Exceptions\\Handler")) {
		if (OL_EQ(name, "report")) {
			h.begin = ol_laravel_report_begin;
		}
	} else if (OL_EQ(cls, "Symfony\\Component\\HttpKernel\\HttpKernel")) {
		if (OL_EQ(name, "handleRaw")) {
			h.end = ol_symfony_handle_end;
		}
	}
	return h;
}

static void ol_error_cb(int type, zend_string *file, uint32_t line, zend_string *message)
{
	ol_span *root;
	char where[512];
	int n;

	if (!OLG(active) || OLG(nspans) == 0) {
		return;
	}
	if (!(type & (E_ERROR | E_CORE_ERROR | E_COMPILE_ERROR | E_USER_ERROR | E_RECOVERABLE_ERROR | E_PARSE))) {
		return;
	}
	root = &OLG(spans)[0];
	root->status = OL_STATUS_ERROR;
	if (root->ex_type == NULL) {
		root->ex_type = "php.fatal_error";
		root->ex_msg = message ? ol_copy(ZSTR_VAL(message), ZSTR_LEN(message), 1024) : NULL;
		if (file) {
			n = snprintf(where, sizeof(where), "%s:%u", ZSTR_VAL(file), line);
			if (n > 0) {
				root->ex_where = ol_copy(where, (size_t) n < sizeof(where) ? (size_t) n : sizeof(where) - 1, sizeof(where));
			}
		}
	}
}

/* ---------- serialization + non-blocking send ---------- */

typedef struct {
	char *p;
	size_t len;
	size_t cap;
	bool ok;
} ol_w;

static void w_raw(ol_w *w, const char *s, size_t n)
{
	if (!w->ok) {
		return;
	}
	if (w->len + n > w->cap) {
		w->ok = false;
		return;
	}
	memcpy(w->p + w->len, s, n);
	w->len += n;
}

#define W_LIT(w, lit) w_raw((w), (lit), sizeof(lit) - 1)

static void w_str(ol_w *w, const char *s)
{
	static const char hex[] = "0123456789abcdef";
	W_LIT(w, "\"");
	for (; s && *s && w->ok; s++) {
		unsigned char c = (unsigned char) *s;
		if (c == '"' || c == '\\') {
			char e[2] = {'\\', (char) c};
			w_raw(w, e, 2);
		} else if (c < 0x20) {
			char e[6] = {'\\', 'u', '0', '0', hex[c >> 4], hex[c & 15]};
			w_raw(w, e, 6);
		} else {
			w_raw(w, (const char *) &c, 1);
		}
	}
	W_LIT(w, "\"");
}

static void w_u64(ol_w *w, uint64_t v)
{
	char b[24];
	int n = snprintf(b, sizeof(b), "%llu", (unsigned long long) v);
	w_raw(w, b, (size_t) n);
}

static void w_i64(ol_w *w, zend_long v)
{
	char b[24];
	int n = snprintf(b, sizeof(b), ZEND_LONG_FMT, v);
	w_raw(w, b, (size_t) n);
}

static void w_hex(ol_w *w, const uint8_t *b, size_t n)
{
	static const char hex[] = "0123456789abcdef";
	char buf[64];
	size_t i;
	for (i = 0; i < n && i * 2 + 1 < sizeof(buf); i++) {
		buf[i * 2] = hex[b[i] >> 4];
		buf[i * 2 + 1] = hex[b[i] & 15];
	}
	W_LIT(w, "\"");
	w_raw(w, buf, i * 2);
	W_LIT(w, "\"");
}

static void w_id(ol_w *w, uint64_t id)
{
	uint8_t b[8];
	if (id == 0) {
		W_LIT(w, "\"\"");
		return;
	}
	for (int i = 0; i < 8; i++) {
		b[i] = (uint8_t) (id >> (56 - 8 * i));
	}
	w_hex(w, b, 8);
}

static void w_span(ol_w *w, const ol_span *s)
{
	W_LIT(w, "{\"span_id\":");
	w_id(w, s->span_id);
	W_LIT(w, ",\"parent_span_id\":");
	w_id(w, s->parent_id);
	W_LIT(w, ",\"name\":");
	w_str(w, s->name ? s->name : "");
	W_LIT(w, ",\"kind\":");
	w_u64(w, s->kind);
	W_LIT(w, ",\"start\":");
	w_u64(w, s->start_unix_ns);
	W_LIT(w, ",\"dur\":");
	w_u64(w, s->duration_ns);
	W_LIT(w, ",\"status\":");
	w_u64(w, s->status);
	W_LIT(w, ",\"attrs\":{");
	for (uint8_t i = 0; i < s->nattrs; i++) {
		if (i) {
			W_LIT(w, ",");
		}
		w_str(w, s->attrs[i].key);
		W_LIT(w, ":");
		if (s->attrs[i].is_num) {
			w_i64(w, s->attrs[i].num);
		} else {
			w_str(w, s->attrs[i].str);
		}
	}
	W_LIT(w, "}");
	if (s->ex_type) {
		W_LIT(w, ",\"exception\":{\"type\":");
		w_str(w, s->ex_type);
		W_LIT(w, ",\"message\":");
		w_str(w, s->ex_msg ? s->ex_msg : "");
		W_LIT(w, ",\"where\":");
		w_str(w, s->ex_where ? s->ex_where : "");
		W_LIT(w, "}");
	}
	W_LIT(w, "}");
}

static void ol_send(const char *buf, size_t len)
{
	ssize_t n;
	if (!OLG(addr_ready)) {
		size_t plen = strlen(OLG(socket_path));
		if (plen == 0 || plen >= sizeof(OLG(addr).sun_path)) {
			OLG(stat_send_failed)++;
			return;
		}
		memset(&OLG(addr), 0, sizeof(OLG(addr)));
		OLG(addr).sun_family = AF_UNIX;
		memcpy(OLG(addr).sun_path, OLG(socket_path), plen + 1);
		OLG(addrlen) = (socklen_t) (offsetof(struct sockaddr_un, sun_path) + plen + 1);
		OLG(addr_ready) = true;
	}
	if (OLG(fd) < 0) {
		OLG(fd) = socket(AF_UNIX, SOCK_DGRAM | SOCK_NONBLOCK | SOCK_CLOEXEC, 0);
		if (OLG(fd) < 0) {
			OLG(stat_send_failed)++;
			return;
		}
	}
	/* Unconnected sendto: a restarted forwarder (new socket inode) is picked up automatically. */
	n = sendto(OLG(fd), buf, len, MSG_DONTWAIT | MSG_NOSIGNAL, (struct sockaddr *) &OLG(addr), OLG(addrlen));
	if (n < 0) {
		OLG(stat_send_failed)++; /* ENOENT/ECONNREFUSED: no forwarder; EAGAIN: forwarder busy. Drop. */
	} else {
		OLG(stat_sent)++;
	}
}

static void ol_flush(void)
{
	ol_w w = {OLG(out), 0, OL_BUF_SIZE - 64, true};
	uint32_t i;

	/* OPENLOG_SERVICE_NAME (process env, e.g. FPM pool env[]) overrides openlog.service_name. */
	const char *svc = getenv("OPENLOG_SERVICE_NAME");
	W_LIT(&w, "{\"v\":1,\"service\":");
	w_str(&w, svc && *svc ? svc : OLG(service_name));
	W_LIT(&w, ",\"trace_id\":");
	w_hex(&w, OLG(trace_id), 16);
	W_LIT(&w, ",\"spans\":[");
	for (i = 0; i < OLG(nspans); i++) {
		size_t mark = w.len;
		if (i) {
			W_LIT(&w, ",");
		}
		w_span(&w, &OLG(spans)[i]);
		if (!w.ok) { /* datagram full: keep what fits */
			w.len = mark;
			w.ok = true;
			OLG(dropped) += OLG(nspans) - i;
			break;
		}
	}
	w.cap = OL_BUF_SIZE;
	W_LIT(&w, "],\"dropped\":");
	w_u64(&w, OLG(dropped));
	W_LIT(&w, "}");
	OLG(stat_spans_dropped) += OLG(dropped);
	if (w.ok) {
		ol_send(w.p, w.len);
	}
}

static int ol_hexval(char c)
{
	if (c >= '0' && c <= '9') return c - '0';
	if (c >= 'a' && c <= 'f') return c - 'a' + 10;
	if (c >= 'A' && c <= 'F') return c - 'A' + 10;
	return -1;
}

/* traceparent: 00-<32 hex trace id>-<16 hex parent id>-<2 hex flags> */
static bool ol_parse_traceparent(const char *tp)
{
	uint8_t tid[16];
	uint64_t pid = 0;
	int i;
	if (tp == NULL || strlen(tp) < 55 || tp[2] != '-' || tp[35] != '-' || tp[52] != '-') {
		return false;
	}
	for (i = 0; i < 16; i++) {
		int hi = ol_hexval(tp[3 + 2 * i]), lo = ol_hexval(tp[4 + 2 * i]);
		if (hi < 0 || lo < 0) return false;
		tid[i] = (uint8_t) (hi << 4 | lo);
	}
	for (i = 0; i < 16; i++) {
		int v = ol_hexval(tp[36 + i]);
		if (v < 0) return false;
		pid = pid << 4 | (uint64_t) v;
	}
	memcpy(OLG(trace_id), tid, 16);
	OLG(remote_parent) = pid;
	return true;
}

/* ---------- module lifecycle ---------- */

static PHP_GINIT_FUNCTION(openlog)
{
#if defined(COMPILE_DL_OPENLOG) && defined(ZTS)
	ZEND_TSRMLS_CACHE_UPDATE();
#endif
	memset(openlog_globals, 0, sizeof(*openlog_globals));
	openlog_globals->fd = -1;
}

PHP_MINIT_FUNCTION(openlog)
{
	REGISTER_INI_ENTRIES();
	if (OLG(enabled)) {
		zend_observer_fcall_register(ol_observer_init);
		zend_observer_error_register(ol_error_cb);
	}
	return SUCCESS;
}

PHP_MSHUTDOWN_FUNCTION(openlog)
{
	if (OLG(fd) >= 0) {
		close(OLG(fd));
		OLG(fd) = -1;
	}
	UNREGISTER_INI_ENTRIES();
	return SUCCESS;
}

PHP_RINIT_FUNCTION(openlog)
{
	ol_span *root;
	char *tp;
	const char *uri, *q;

#if defined(ZTS) && defined(COMPILE_DL_OPENLOG)
	ZEND_TSRMLS_CACHE_UPDATE();
#endif
	OLG(active) = false;
	if (!OLG(enabled)) {
		return SUCCESS;
	}
	if (!OLG(cli_enabled) && (strcmp(sapi_module.name, "cli") == 0 || strcmp(sapi_module.name, "phpdbg") == 0)) {
		return SUCCESS;
	}

	OLG(nspans) = 0;
	OLG(depth) = 0;
	OLG(overflow) = 0;
	OLG(dropped) = 0;
	OLG(arena_used) = 0;
	OLG(remote_parent) = 0;
	OLG(route) = NULL;
	OLG(tx_name) = NULL;

	tp = sapi_getenv("HTTP_TRACEPARENT", sizeof("HTTP_TRACEPARENT") - 1);
	if (!ol_parse_traceparent(tp)) {
		uint64_t a = ol_rand64(), b = ol_rand64();
		for (int i = 0; i < 8; i++) {
			OLG(trace_id)[i] = (uint8_t) (a >> (56 - 8 * i));
			OLG(trace_id)[8 + i] = (uint8_t) (b >> (56 - 8 * i));
		}
		OLG(remote_parent) = 0;
	}
	if (tp) {
		efree(tp);
	}

	OLG(active) = true;
	root = ol_push("HTTP", OL_KIND_SERVER);
	if (root == NULL) {
		OLG(active) = false;
		return SUCCESS;
	}
	if (SG(request_info).request_method) {
		ol_attr_str(root, "http.request.method", ol_copy(SG(request_info).request_method, strlen(SG(request_info).request_method), 16));
	}
	/* Under FPM request_uri is the script name; the client's path is REQUEST_URI. */
	tp = sapi_getenv("REQUEST_URI", sizeof("REQUEST_URI") - 1);
	uri = tp ? tp : SG(request_info).request_uri;
	if (uri) {
		q = strchr(uri, '?');
		ol_attr_str(root, "url.path", ol_copy(uri, q ? (size_t) (q - uri) : strlen(uri), 1024));
	}
	if (tp) {
		efree(tp);
	}
	return SUCCESS;
}

PHP_RSHUTDOWN_FUNCTION(openlog)
{
	ol_span *root;
	uint64_t now;
	int code;
	char name[600];
	const char *method;

	if (!OLG(active)) {
		return SUCCESS;
	}
	now = ol_mono_ns();
	/* Close anything left open (fatal error / bailout skips observer end handlers). */
	while (OLG(depth) > 0) {
		uint32_t idx = OLG(stack)[--OLG(depth)];
		if (idx != OL_NO_SPAN && OLG(spans)[idx].duration_ns == 0) {
			OLG(spans)[idx].duration_ns = now - OLG(spans)[idx].start_mono_ns;
		}
	}
	OLG(overflow) = 0;

	root = &OLG(spans)[0];
	code = SG(sapi_headers).http_response_code;
	if (code > 0) {
		ol_attr_num(root, "http.response.status_code", code);
		if (code >= 500) {
			root->status = OL_STATUS_ERROR;
		}
	}
	method = SG(request_info).request_method ? SG(request_info).request_method : "HTTP";
	if (OLG(route)) {
		int n = snprintf(name, sizeof(name), "%s %s", method, OLG(route));
		ol_attr_str(root, "http.route", OLG(route));
		root->name = ol_copy(name, n > 0 && (size_t) n < sizeof(name) ? (size_t) n : sizeof(name) - 1, sizeof(name));
	} else if (OLG(tx_name)) {
		int n = snprintf(name, sizeof(name), "%s %s", method, OLG(tx_name));
		ol_attr_str(root, "openlog.transaction.name", OLG(tx_name));
		root->name = ol_copy(name, n > 0 && (size_t) n < sizeof(name) ? (size_t) n : sizeof(name) - 1, sizeof(name));
	} else {
		root->name = ol_copy(method, strlen(method), 16);
	}
	if (root->ex_type && root->status != OL_STATUS_ERROR) {
		root->status = OL_STATUS_ERROR;
	}

	ol_flush();
	OLG(active) = false;
	return SUCCESS;
}

PHP_MINFO_FUNCTION(openlog)
{
	char buf[32];
	php_info_print_table_start();
	php_info_print_table_row(2, "openlog agent spike", OLG(enabled) ? "enabled" : "disabled");
	php_info_print_table_row(2, "version", PHP_OPENLOG_VERSION);
	snprintf(buf, sizeof(buf), ZEND_LONG_FMT, OLG(stat_sent));
	php_info_print_table_row(2, "datagrams sent (this process)", buf);
	snprintf(buf, sizeof(buf), ZEND_LONG_FMT, OLG(stat_send_failed));
	php_info_print_table_row(2, "datagrams dropped (this process)", buf);
	php_info_print_table_end();
	DISPLAY_INI_ENTRIES();
}

static const zend_module_dep openlog_deps[] = {
	ZEND_MOD_REQUIRED("pdo")
	ZEND_MOD_END
};

zend_module_entry openlog_module_entry = {
	STANDARD_MODULE_HEADER_EX,
	NULL,
	openlog_deps,
	"openlog",
	NULL,
	PHP_MINIT(openlog),
	PHP_MSHUTDOWN(openlog),
	PHP_RINIT(openlog),
	PHP_RSHUTDOWN(openlog),
	PHP_MINFO(openlog),
	PHP_OPENLOG_VERSION,
	PHP_MODULE_GLOBALS(openlog),
	PHP_GINIT(openlog),
	NULL,
	NULL,
	STANDARD_MODULE_PROPERTIES_EX
};

#ifdef COMPILE_DL_OPENLOG
# ifdef ZTS
ZEND_TSRMLS_CACHE_DEFINE()
# endif
ZEND_GET_MODULE(openlog)
#endif

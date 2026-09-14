/*
 * Helpers: engine access without side effects, exceptions, SQL / URL attributes. Pure text functions (UTF-8, SQL
 * sanitizing, path normalization) live in ol_text.c.
 * SPDX-License-Identifier: Apache-2.0
 */
#include "ol.h"
#include "zend_smart_str.h"

/* ---------------- engine access ---------------- */

zval *ol_arg(zend_execute_data *ex, uint32_t n)
{
	zval *z;
	if (ex == NULL || n == 0 || ex == OLG(end_ex) || n > ZEND_CALL_NUM_ARGS(ex)) {
		return NULL;
	}
	z = ZEND_CALL_ARG(ex, n);
	ZVAL_DEREF(z);
	return Z_TYPE_P(z) == IS_UNDEF ? NULL : z;
}

zval *ol_this(zend_execute_data *ex)
{
	if (ex && ex == OLG(end_ex)) {
		return Z_TYPE(OLG(end_this)) == IS_OBJECT ? &OLG(end_this) : NULL;
	}
	if (ex && Z_TYPE(ex->This) == IS_OBJECT) {
		return &ex->This;
	}
	return NULL;
}

/* Reads a declared or dynamic property directly from the object storage: no __get, no hooks, no userland. */
zval *ol_prop(zend_object *obj, const char *name, size_t len)
{
	zend_class_entry *ce;
	zval *zv = NULL;

	if (obj == NULL) {
		return NULL;
	}
	for (ce = obj->ce; ce; ce = ce->parent) {
		zend_property_info *pi = zend_hash_str_find_ptr(&ce->properties_info, name, len);
		if (pi && pi->ce == ce && !(pi->flags & ZEND_ACC_STATIC)) {
			zv = OBJ_PROP(obj, pi->offset);
			break;
		}
	}
	if (zv == NULL && obj->properties) {
		zv = zend_hash_str_find(obj->properties, name, len);
		if (zv && Z_TYPE_P(zv) == IS_INDIRECT) {
			zv = Z_INDIRECT_P(zv);
		}
	}
	if (zv == NULL) {
		return NULL;
	}
	ZVAL_DEREF(zv);
	return Z_TYPE_P(zv) == IS_UNDEF ? NULL : zv;
}

zval *ol_array_get(zval *arr, const char *key, size_t len)
{
	zval *zv;
	if (arr == NULL || Z_TYPE_P(arr) != IS_ARRAY) {
		return NULL;
	}
	zv = zend_hash_str_find(Z_ARRVAL_P(arr), key, len);
	if (zv && Z_TYPE_P(zv) == IS_INDIRECT) {
		zv = Z_INDIRECT_P(zv);
	}
	if (zv) {
		ZVAL_DEREF(zv);
	}
	return zv;
}

/* Class lookup without autoloading. */
zend_class_entry *ol_class_find(const char *lcname, size_t len)
{
	return zend_hash_str_find_ptr(EG(class_table), lcname, len);
}

bool ol_instanceof(zend_object *obj, const char *lcname, size_t len)
{
	zend_class_entry *ce = ol_class_find(lcname, len);
	return obj && ce && instanceof_function(obj->ce, ce);
}

static bool ol_call(zend_function *fn, zend_object *obj, zval *rv, uint32_t argc, zval *argv)
{
	bool ok;
	ZVAL_UNDEF(rv);
	if (EG(exception)) {
		return false;
	}
	OLG(in_call) = true;
#if PHP_VERSION_ID >= 80000
	zend_call_known_function(fn, obj, obj ? obj->ce : NULL, rv, argc, argv, NULL);
	ok = !EG(exception);
#else
	{
		zend_fcall_info fci;
		zend_fcall_info_cache fcc;
		memset(&fci, 0, sizeof(fci));
		memset(&fcc, 0, sizeof(fcc));
		fci.size = sizeof(fci);
		ZVAL_UNDEF(&fci.function_name);
		fci.retval = rv;
		fci.params = argv;
		fci.param_count = argc;
		fci.object = obj;
		fci.no_separation = 1;
# if PHP_VERSION_ID < 70300
		fcc.initialized = 1;
# endif
		fcc.function_handler = fn;
		fcc.calling_scope = obj ? obj->ce : NULL;
		fcc.called_scope = obj ? obj->ce : NULL;
		fcc.object = obj;
		ok = zend_call_function(&fci, &fcc) == SUCCESS && !EG(exception);
	}
#endif
	OLG(in_call) = false;
	if (EG(exception)) {
		/* never leak an exception raised by an agent call into the application */
		zend_clear_exception();
		ok = false;
	}
	return ok;
}

bool ol_call_function(const char *name, size_t len, zval *rv, uint32_t argc, zval *argv)
{
	zend_function *fn = zend_hash_str_find_ptr(EG(function_table), name, len);
	if (fn == NULL) {
		ZVAL_UNDEF(rv);
		return false;
	}
	return ol_call(fn, NULL, rv, argc, argv);
}

bool ol_call_method0(zend_object *obj, const char *lcname, size_t len, zval *rv)
{
	zend_function *fn;
	if (obj == NULL) {
		return false;
	}
	fn = zend_hash_str_find_ptr(&obj->ce->function_table, lcname, len);
	if (fn == NULL || (fn->common.fn_flags & ZEND_ACC_STATIC)) {
		ZVAL_UNDEF(rv);
		return false;
	}
	return ol_call(fn, obj, rv, 0, NULL);
}

/* ---------------- exceptions / errors ---------------- */

bool ol_exception_seen(zend_object *ex)
{
	uint32_t i;
	for (i = 0; i < OLG(nreported); i++) {
		if (OLG(last_reported)[i] == ex) {
			return true;
		}
	}
	return false;
}

static void ol_trace_string(smart_str *s, zval *trace)
{
	zval *frame;
	zend_ulong num = 0;
	if (trace == NULL || Z_TYPE_P(trace) != IS_ARRAY) {
		return;
	}
	ZEND_HASH_FOREACH_VAL(Z_ARRVAL_P(trace), frame) {
		zval *f, *l, *c, *t, *fn;
		ZVAL_DEREF(frame);
		if (Z_TYPE_P(frame) != IS_ARRAY) {
			continue;
		}
		if (s->s && ZSTR_LEN(s->s) > OL_STR_MAX) {
			break;
		}
		f = ol_array_get(frame, "file", 4);
		l = ol_array_get(frame, "line", 4);
		c = ol_array_get(frame, "class", 5);
		t = ol_array_get(frame, "type", 4);
		fn = ol_array_get(frame, "function", 8);
		smart_str_appendc(s, '#');
		smart_str_append_unsigned(s, num++);
		smart_str_appendc(s, ' ');
		if (f && Z_TYPE_P(f) == IS_STRING) {
			smart_str_append(s, Z_STR_P(f));
			smart_str_appendc(s, '(');
			smart_str_append_long(s, l && Z_TYPE_P(l) == IS_LONG ? Z_LVAL_P(l) : 0);
			smart_str_appendl(s, "): ", 3);
		} else {
			smart_str_appendl(s, "[internal function]: ", sizeof("[internal function]: ") - 1);
		}
		if (c && Z_TYPE_P(c) == IS_STRING) {
			smart_str_append(s, Z_STR_P(c));
			if (t && Z_TYPE_P(t) == IS_STRING) {
				smart_str_append(s, Z_STR_P(t));
			}
		}
		if (fn && Z_TYPE_P(fn) == IS_STRING) {
			smart_str_append(s, Z_STR_P(fn));
		}
		smart_str_appendl(s, "()\n", 3);
	} ZEND_HASH_FOREACH_END();
	smart_str_appendc(s, '#');
	smart_str_append_unsigned(s, num);
	smart_str_appendl(s, " {main}", 7);
}

/* Records an exception event on n (type, message, PHP-format stack trace). No PHP code is called. */
void ol_record_exception(ol_node *n, zend_object *ex, bool set_status)
{
	zval *msg, *file, *line;
	ol_event *e;
	smart_str st = {0};

	if (n == NULL || ex == NULL || !instanceof_function(ex->ce, zend_ce_throwable)) {
		return;
	}
	if (OLG(nreported) < sizeof(OLG(last_reported)) / sizeof(OLG(last_reported)[0])) {
		/* held until request end so the address cannot be reused by another exception */
		GC_ADDREF(ex);
		OLG(last_reported)[OLG(nreported)++] = ex;
	}
	msg = ol_prop(ex, "message", 7);
	file = ol_prop(ex, "file", 4);
	line = ol_prop(ex, "line", 4);
	e = ol_event_add(n, "exception");
	ol_event_attr_str(e, "exception.type", ZSTR_VAL(ex->ce->name), ZSTR_LEN(ex->ce->name));
	if (msg && Z_TYPE_P(msg) == IS_STRING) {
		ol_event_attr_str(e, "exception.message", Z_STRVAL_P(msg), Z_STRLEN_P(msg));
		n->status_msg = ol_strdup(Z_STRVAL_P(msg), Z_STRLEN_P(msg), 1024);
	} else {
		ol_event_attr_str(e, "exception.message", "", 0);
	}
	if (file && Z_TYPE_P(file) == IS_STRING) {
		/* PHP's __toString shape: the throw site first, then the trace */
		smart_str_appendl(&st, "## ", 3);
		smart_str_append(&st, Z_STR_P(file));
		smart_str_appendc(&st, '(');
		smart_str_append_long(&st, line && Z_TYPE_P(line) == IS_LONG ? Z_LVAL_P(line) : 0);
		smart_str_appendl(&st, ")\n", 2);
	}
	ol_trace_string(&st, ol_prop(ex, "trace", 5));
	smart_str_0(&st);
	if (st.s) {
		ol_event_attr_str(e, "exception.stacktrace", ZSTR_VAL(st.s), ZSTR_LEN(st.s));
		smart_str_free(&st);
	}
	if (set_status) {
		n->status = OL_STATUS_ERROR;
	}
}

/* Exception reported through a framework handler: exception event + error status on the transaction. */
void ol_report_exception(zend_object *e)
{
	ol_node *root = ol_node_at(0);
	if (!OL_REC() || root == NULL || e == NULL || ol_exception_seen(e)) {
		return;
	}
	/* frameworks turn the engine's fatal error into an exception object and report it again */
	if ((root->flags & OL_NF_FATAL) &&
			(ol_instanceof(e, ZEND_STRL("symfony\\component\\errorhandler\\error\\fatalerror")) ||
			 ol_instanceof(e, ZEND_STRL("symfony\\component\\debug\\exception\\fatalerrorexception")))) {
		return;
	}
	ol_record_exception(root, e, true);
}

/* Exception leaving the top-level script. */
void ol_uncaught_exception(zend_object *e)
{
	ol_node *root = ol_node_at(0);
	if (!OL_REC() || root == NULL || e == NULL || !instanceof_function(e->ce, zend_ce_throwable)) {
		return; /* exit() / die() unwinding is not an error */
	}
	if (!ol_exception_seen(e)) {
		ol_record_exception(root, e, true);
	}
	root->status = OL_STATUS_ERROR;
	root->flags |= OL_NF_UNCAUGHT;
}

static const char *ol_error_type_name(int type)
{
	switch (type) {
		case E_ERROR: return "E_ERROR";
		case E_CORE_ERROR: return "E_CORE_ERROR";
		case E_COMPILE_ERROR: return "E_COMPILE_ERROR";
		case E_USER_ERROR: return "E_USER_ERROR";
		case E_RECOVERABLE_ERROR: return "E_RECOVERABLE_ERROR";
		case E_PARSE: return "E_PARSE";
		default: return "E_UNKNOWN";
	}
}

void ol_record_error(ol_node *n, int type, const char *file, uint32_t line, const char *msg, size_t msg_len)
{
	char buf[1200];
	int len;
	ol_event *e;
	if (n == NULL) {
		return;
	}
	e = ol_event_add(n, "exception");
	ol_event_attr_str(e, "exception.type", ol_error_type_name(type), strlen(ol_error_type_name(type)));
	ol_event_attr_str(e, "exception.message", msg ? msg : "", msg ? msg_len : 0);
	len = snprintf(buf, sizeof(buf), "#0 %s(%u): {fatal error}\n#1 {main}", file ? file : "[unknown]", line);
	if (len > 0) {
		ol_event_attr_str(e, "exception.stacktrace", buf, (size_t) len < sizeof(buf) ? (size_t) len : sizeof(buf) - 1);
	}
	if (msg) {
		n->status_msg = ol_strdup(msg, msg_len, 1024);
	}
	n->status = OL_STATUS_ERROR;
	n->flags |= OL_NF_FATAL;
}

/* ---------------- SQL attributes ---------------- */

/*
 * Query analysis (operation, collection, sanitized text) is cached per request by query text: loops over the same
 * prepared statement or query string (the common ORM pattern) pay for it once. The cached raw text is compared in
 * full, so a hash collision only costs a recomputation.
 */
static uint32_t ol_query_slot(const char *system, const char *sql, size_t len)
{
	uint64_t a = 0, b = 0, h;
	memcpy(&a, sql, len < 8 ? len : 8);
	if (len > 8) {
		memcpy(&b, sql + len - 8, 8);
	}
	h = (a ^ (b * 0x9E3779B97F4A7C15ULL) ^ ((uint64_t) len << 17) ^ (uint64_t) (uintptr_t) system) * 0xff51afd7ed558ccdULL;
	return (uint32_t) (h >> 58) & (OL_QCACHE_SIZE - 1);
}

static void ol_query_compute(ol_qcache *e, const char *system, const char *sql, size_t len)
{
	char op[32], coll[128], name[192];
	size_t l = 0, cl = 0;

	e->name = system;
	e->op = e->coll = e->text = NULL;
	e->oplen = e->colllen = e->textlen = 0;
	e->mode = (uint8_t) OLG(query_mode);
	if (ol_sql_operation(sql, len, op, sizeof(op), coll, sizeof(coll)) > 0) {
		e->op = ol_strdup_n(op, strlen(op), sizeof(op), &l);
		e->oplen = (uint16_t) l;
		if (coll[0]) {
			e->coll = ol_strdup_n(coll, strlen(coll), sizeof(coll), &cl);
			e->colllen = (uint16_t) cl;
		}
		if (e->op && e->coll) {
			memcpy(name, e->op, e->oplen);
			name[e->oplen] = ' ';
			memcpy(name + e->oplen + 1, e->coll, e->colllen);
			e->name = ol_strdup_n(name, (size_t) e->oplen + 1 + e->colllen, sizeof(name), NULL);
		} else if (e->op && !coll[0]) {
			e->name = e->op;
		}
		if (e->name == NULL) {
			e->name = system;
		}
	}
	l = 0;
	if (e->mode == OL_QUERY_RAW) {
		e->text = ol_strdup_n(sql, len, OL_STR_MAX, &l);
	} else if (e->mode == OL_QUERY_SANITIZED) {
		char buf[OL_STR_MAX + 1];
		size_t sl = ol_sql_sanitize(buf, sizeof(buf), sql, len, system && strcmp(system, "mysql") == 0);
		e->text = ol_strdup_n(buf, sl, OL_STR_MAX, &l);
	}
	e->textlen = (uint16_t) l;
}

/* Span name, db.operation.name, db.collection.name, db.query.text (per openlog.capture_query_text). */
void ol_db_query_attrs(ol_node *n, const char *system, const char *sql, size_t len)
{
	ol_qcache tmp, *e = &tmp, *slot = NULL;

	if (n == NULL) {
		return;
	}
	if (sql == NULL) {
		n->name = system;
		return;
	}
	if (len > 0 && len <= OL_STR_MAX) {
		slot = &OLG(qcache)[ol_query_slot(system, sql, len)];
		if (slot->gen == OLG(qgen) && slot->len == len && slot->system == system && slot->mode == OLG(query_mode) &&
				memcmp(slot->raw, sql, len) == 0) {
			e = slot;
			goto apply;
		}
	}
	ol_query_compute(&tmp, system, sql, len);
	if (slot) {
		char *raw = ol_alloc(len);
		if (raw) {
			memcpy(raw, sql, len);
			tmp.raw = raw;
			tmp.len = (uint32_t) len;
			tmp.system = system;
			tmp.gen = OLG(qgen);
			*slot = tmp;
			e = slot;
		}
	}
apply:
	if (e->op) {
		ol_attr_static_n(n, "db.operation.name", e->op, e->oplen);
		if (e->coll) {
			ol_attr_static_n(n, "db.collection.name", e->coll, e->colllen);
		}
	}
	n->name = e->name;
	if (e->text) {
		ol_attr_static_n(n, "db.query.text", e->text, e->textlen);
	}
}

/* ---------------- URLs ---------------- */

/* url.full (without credentials), server.address, server.port */
void ol_url_attrs(ol_node *n, const char *url, size_t len)
{
	const char *p = url, *end = url + len, *auth, *auth_end, *at, *host, *host_end, *colon = NULL;
	char buf[OL_STR_MAX + 1];
	size_t o = 0;
	int64_t port = 0;
	bool https = ol_str_starts_ci(url, len, "https://");

	if (n == NULL || url == NULL) {
		return;
	}
	auth = memchr(p, ':', len);
	if (auth == NULL || auth + 2 >= end || auth[1] != '/' || auth[2] != '/') {
		ol_attr_str(n, "url.full", url, len);
		return;
	}
	auth += 3;
	auth_end = auth;
	while (auth_end < end && *auth_end != '/' && *auth_end != '?' && *auth_end != '#') auth_end++;
	at = NULL;
	for (host = auth; host < auth_end; host++) {
		if (*host == '@') at = host;
	}
	host = at ? at + 1 : auth;
	/* url.full without user:password */
	o = (size_t) (auth - url);
	if (o > OL_STR_MAX) o = OL_STR_MAX;
	memcpy(buf, url, o);
	{
		size_t rest = (size_t) (end - host);
		if (o + rest > OL_STR_MAX) rest = OL_STR_MAX - o;
		memcpy(buf + o, host, rest);
		o += rest;
	}
	ol_attr_str(n, "url.full", buf, o);

	host_end = auth_end;
	if (*host == '[') {
		const char *rb = memchr(host, ']', (size_t) (auth_end - host));
		if (rb) {
			if (rb + 1 < auth_end && rb[1] == ':') colon = rb + 1;
			ol_attr_str(n, "server.address", host + 1, (size_t) (rb - host - 1));
		}
	} else {
		const char *c;
		for (c = host; c < auth_end; c++) {
			if (*c == ':') colon = c;
		}
		host_end = colon ? colon : auth_end;
		ol_attr_str(n, "server.address", host, (size_t) (host_end - host));
	}
	if (colon) {
		const char *c;
		for (c = colon + 1; c < auth_end && OL_ISDIGIT(*c); c++) {
			port = port * 10 + (*c - '0');
		}
	}
	if (port == 0) {
		port = https ? 443 : (ol_str_starts_ci(url, len, "http://") ? 80 : 0);
	}
	if (port > 0) {
		ol_attr_int(n, "server.port", port);
	}
}

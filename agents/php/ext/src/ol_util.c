/*
 * Helpers: engine access without side effects, exceptions, SQL / URL / route normalization.
 * SPDX-License-Identifier: Apache-2.0
 */
#include "ol.h"
#include "zend_smart_str.h"

#include <ctype.h>

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

/* ---------------- strings ---------------- */

/* Copies at most max bytes of src replacing invalid UTF-8 bytes with '?'; never cuts a character in half. */
size_t ol_utf8_clean(char *dst, const char *src, size_t len, size_t max)
{
	size_t i = 0, o = 0;
	if (len > max) {
		len = max;
	}
	while (i < len) {
		unsigned char c = (unsigned char) src[i];
		size_t need;
		if (c < 0x80) {
			dst[o++] = (char) c;
			i++;
			continue;
		}
		if (c >= 0xC2 && c <= 0xDF) {
			need = 1;
		} else if (c >= 0xE0 && c <= 0xEF) {
			need = 2;
		} else if (c >= 0xF0 && c <= 0xF4) {
			need = 3;
		} else {
			dst[o++] = '?';
			i++;
			continue;
		}
		if (i + need >= len) {
			break; /* sequence runs past the (possibly truncated) end */
		}
		{
			size_t k;
			bool ok = true;
			unsigned char c1 = (unsigned char) src[i + 1];
			for (k = 1; k <= need; k++) {
				if (((unsigned char) src[i + k] & 0xC0) != 0x80) {
					ok = false;
					break;
				}
			}
			if (ok && need == 2 && ((c == 0xE0 && c1 < 0xA0) || (c == 0xED && c1 > 0x9F))) {
				ok = false; /* overlong / surrogate */
			}
			if (ok && need == 3 && ((c == 0xF0 && c1 < 0x90) || (c == 0xF4 && c1 > 0x8F))) {
				ok = false;
			}
			if (!ok) {
				dst[o++] = '?';
				i++;
				continue;
			}
			memcpy(dst + o, src + i, need + 1);
			o += need + 1;
			i += need + 1;
		}
	}
	return o;
}

bool ol_str_starts_ci(const char *s, size_t len, const char *prefix)
{
	size_t pl = strlen(prefix);
	return len >= pl && zend_binary_strncasecmp(s, pl, prefix, pl, pl) == 0;
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

/* ---------------- SQL ---------------- */

static inline bool ol_ident_char(char c)
{
	return isalnum((unsigned char) c) || c == '_' || c == '$' || c == '@' || c == ':' || (unsigned char) c >= 0x80;
}

/*
 * Replaces literals with '?': quoted strings ('' and backslash escapes; "..." only when double_quote_strings, i.e.
 * MySQL), dollar-quoted strings, numbers and hex literals. Removes comments, collapses whitespace. Placeholders
 * ($1, :name, ?) stay. Output is NUL terminated, at most cap - 1 bytes.
 */
size_t ol_sql_sanitize(char *dst, size_t cap, const char *s, size_t n, bool dq)
{
	size_t i = 0, o = 0;
	bool space = false;

#define OL_PUT(ch) do { if (o + 1 < cap) { dst[o++] = (char) (ch); } } while (0)
#define OL_FLUSH_SPACE() do { if (space && o > 0) { OL_PUT(' '); } space = false; } while (0)

	if (cap == 0) {
		return 0;
	}
	while (i < n && o + 8 < cap) {
		unsigned char c = (unsigned char) s[i];
		if (c == '-' && i + 1 < n && s[i + 1] == '-') {
			while (i < n && s[i] != '\n') i++;
			space = true;
			continue;
		}
		if (c == '#' && dq) {
			while (i < n && s[i] != '\n') i++;
			space = true;
			continue;
		}
		if (c == '/' && i + 1 < n && s[i + 1] == '*') {
			i += 2;
			while (i + 1 < n && !(s[i] == '*' && s[i + 1] == '/')) i++;
			i += 2;
			space = true;
			continue;
		}
		if (isspace(c)) {
			space = true;
			i++;
			continue;
		}
		OL_FLUSH_SPACE();
		if (c == '\'' || (dq && c == '"')) {
			char q = (char) c;
			/* E'...' / N'...' / X'...' prefixes */
			if (o > 0 && (dst[o - 1] == 'E' || dst[o - 1] == 'e' || dst[o - 1] == 'N' || dst[o - 1] == 'n' ||
					dst[o - 1] == 'X' || dst[o - 1] == 'x' || dst[o - 1] == 'B' || dst[o - 1] == 'b') &&
					(o == 1 || !ol_ident_char(dst[o - 2]))) {
				o--;
			}
			i++;
			while (i < n) {
				if (s[i] == '\\' && i + 1 < n) {
					i += 2;
					continue;
				}
				if (s[i] == q) {
					if (i + 1 < n && s[i + 1] == q) {
						i += 2;
						continue;
					}
					i++;
					break;
				}
				i++;
			}
			OL_PUT('?');
			continue;
		}
		if (c == '$') {
			if (i + 1 < n && isdigit((unsigned char) s[i + 1])) { /* $1 placeholder */
				OL_PUT('$');
				i++;
				while (i < n && isdigit((unsigned char) s[i])) {
					OL_PUT(s[i]);
					i++;
				}
				continue;
			}
			/* $tag$ ... $tag$ */
			{
				size_t j = i + 1;
				while (j < n && (isalnum((unsigned char) s[j]) || s[j] == '_')) j++;
				if (j < n && s[j] == '$' && (o == 0 || !ol_ident_char(dst[o - 1]))) {
					size_t taglen = j - i + 1, k = j + 1;
					bool found = false;
					while (k + taglen <= n) {
						if (memcmp(s + k, s + i, taglen) == 0) {
							found = true;
							break;
						}
						k++;
					}
					i = found ? k + taglen : n;
					OL_PUT('?');
					continue;
				}
			}
		}
		if (isdigit(c) && (o == 0 || !ol_ident_char(dst[o - 1]))) {
			if (c == '0' && i + 1 < n && (s[i + 1] == 'x' || s[i + 1] == 'X')) {
				i += 2;
				while (i < n && isxdigit((unsigned char) s[i])) i++;
			} else {
				while (i < n && isdigit((unsigned char) s[i])) i++;
				if (i < n && s[i] == '.') {
					i++;
					while (i < n && isdigit((unsigned char) s[i])) i++;
				}
				if (i < n && (s[i] == 'e' || s[i] == 'E')) {
					size_t j = i + 1;
					if (j < n && (s[j] == '+' || s[j] == '-')) j++;
					if (j < n && isdigit((unsigned char) s[j])) {
						i = j;
						while (i < n && isdigit((unsigned char) s[i])) i++;
					}
				}
			}
			OL_PUT('?');
			continue;
		}
		OL_PUT(c);
		i++;
	}
	if (i < n && o + 4 < cap) {
		/* truncated */
		dst[o++] = '.';
		dst[o++] = '.';
		dst[o++] = '.';
	}
	dst[o] = '\0';
	return o;
#undef OL_PUT
#undef OL_FLUSH_SPACE
}

static size_t ol_read_word(const char *s, size_t n, size_t *pos, char *out, size_t cap, bool ident)
{
	size_t i = *pos, k = 0;
	while (i < n && (isspace((unsigned char) s[i]) || s[i] == '(')) i++;
	while (i < n && k + 1 < cap) {
		char c = s[i];
		if (ident ? (isalnum((unsigned char) c) || c == '_' || c == '.' || c == '`' || c == '"' || c == '[' || c == ']')
				: isalpha((unsigned char) c)) {
			if (c != '`' && c != '"' && c != '[' && c != ']') {
				out[k++] = ident ? c : (char) toupper((unsigned char) c);
			}
			i++;
		} else {
			break;
		}
	}
	out[k] = '\0';
	*pos = i;
	return k;
}

/* First keyword (upper case) and, for simple statements, the table ("collection"). */
size_t ol_sql_operation(const char *sql, size_t len, char *op, size_t opcap, char *coll, size_t collcap)
{
	size_t pos = 0, oplen;
	char w[32];
	coll[0] = '\0';
	/* skip leading comments */
	while (pos < len) {
		while (pos < len && isspace((unsigned char) sql[pos])) pos++;
		if (pos + 1 < len && sql[pos] == '/' && sql[pos + 1] == '*') {
			pos += 2;
			while (pos + 1 < len && !(sql[pos] == '*' && sql[pos + 1] == '/')) pos++;
			pos += 2;
		} else if (pos + 1 < len && sql[pos] == '-' && sql[pos + 1] == '-') {
			while (pos < len && sql[pos] != '\n') pos++;
		} else {
			break;
		}
	}
	oplen = ol_read_word(sql, len, &pos, op, opcap, false);
	if (oplen == 0) {
		return 0;
	}
	if (strcmp(op, "SELECT") == 0 || strcmp(op, "DELETE") == 0) {
		/* find FROM at nesting level 0 */
		int depth = 0;
		size_t i = pos;
		while (i < len) {
			char c = sql[i];
			if (c == '(') depth++;
			else if (c == ')') depth--;
			else if (c == '\'' ) { i++; while (i < len && sql[i] != '\'') i++; }
			else if (depth == 0 && (c == 'f' || c == 'F') && i + 4 < len && (i == 0 || !ol_ident_char(sql[i - 1])) &&
					zend_binary_strncasecmp(sql + i, 4, "from", 4, 4) == 0 && isspace((unsigned char) sql[i + 4])) {
				size_t p = i + 4;
				ol_read_word(sql, len, &p, coll, collcap, true);
				break;
			}
			i++;
		}
	} else if (strcmp(op, "INSERT") == 0 || strcmp(op, "REPLACE") == 0) {
		size_t p = pos;
		ol_read_word(sql, len, &p, w, sizeof(w), false);
		if (strcmp(w, "INTO") == 0) {
			ol_read_word(sql, len, &p, coll, collcap, true);
		} else if (strcmp(w, "IGNORE") == 0) {
			ol_read_word(sql, len, &p, w, sizeof(w), false);
			ol_read_word(sql, len, &p, coll, collcap, true);
		}
	} else if (strcmp(op, "UPDATE") == 0) {
		size_t p = pos;
		ol_read_word(sql, len, &p, coll, collcap, true);
	}
	return oplen;
}

/* Span name, db.operation.name, db.collection.name, db.query.text (per openlog.capture_query_text). */
void ol_db_query_attrs(ol_node *n, const char *system, const char *sql, size_t len)
{
	char op[32], coll[128], name[192];
	if (n == NULL) {
		return;
	}
	if (sql == NULL) {
		n->name = system;
		return;
	}
	if (ol_sql_operation(sql, len, op, sizeof(op), coll, sizeof(coll)) > 0) {
		ol_attr_str(n, "db.operation.name", op, strlen(op));
		if (coll[0]) {
			ol_attr_str(n, "db.collection.name", coll, strlen(coll));
			snprintf(name, sizeof(name), "%s %s", op, coll);
			n->name = ol_strdup(name, strlen(name), sizeof(name));
		} else {
			n->name = ol_strdup(op, strlen(op), sizeof(op));
		}
	} else {
		n->name = system;
	}
	if (OLG(query_mode) == OL_QUERY_RAW) {
		ol_attr_str(n, "db.query.text", sql, len);
	} else if (OLG(query_mode) == OL_QUERY_SANITIZED) {
		char buf[OL_STR_MAX + 1];
		size_t l = ol_sql_sanitize(buf, sizeof(buf), sql, len, system && strcmp(system, "mysql") == 0);
		ol_attr_str(n, "db.query.text", buf, l);
	}
}

/* ---------------- paths / routes / URLs ---------------- */

static bool ol_seg_all(const char *s, size_t n, int (*pred)(int))
{
	size_t i;
	for (i = 0; i < n; i++) {
		if (!pred((unsigned char) s[i])) return false;
	}
	return n > 0;
}

static const char *ol_seg_replacement(const char *s, size_t n)
{
	size_t i, digits = 0, alpha = 0, hexletters = 0;
	bool allhex = true, token = true, email_at = false;

	/* UUID */
	if (n == 36 && s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-') {
		bool ok = true;
		for (i = 0; i < 36 && ok; i++) {
			if (i == 8 || i == 13 || i == 18 || i == 23) continue;
			ok = isxdigit((unsigned char) s[i]);
		}
		if (ok) return "{uuid}";
	}
	/* decimal, optionally signed */
	if ((s[0] == '-' || s[0] == '+') && n > 1 && ol_seg_all(s + 1, n - 1, isdigit)) return "{id}";
	if (ol_seg_all(s, n, isdigit)) return "{id}";
	for (i = 0; i < n; i++) {
		unsigned char c = (unsigned char) s[i];
		if (isdigit(c)) digits++;
		else if (isalpha(c)) alpha++;
		if (!isxdigit(c)) allhex = false;
		else if (!isdigit(c)) hexletters++;
		if (!(isalnum(c) || c == '_' || c == '-')) token = false;
		if (c == '@') email_at = true;
	}
	if (allhex && (n >= 16 || (n >= 8 && digits > 0 && hexletters > 0))) return "{hex}";
	if (token && n >= 20 && digits > 0) return "{token}";
	if (email_at) {
		const char *at = memchr(s, '@', n);
		if (at && at > s && memchr(at, '.', n - (size_t) (at - s)) != NULL) return "{email}";
	}
	(void) alpha;
	return NULL;
}

/* apm.md §2.2 path normalization. */
size_t ol_normalize_path(char *dst, size_t cap, const char *path, size_t len)
{
	size_t i = 0, o = 0;
	int segs = 0;
	const char *q = memchr(path, '?', len);
	const char *h = memchr(path, '#', len);
	if (q) len = (size_t) (q - path);
	if (h && (size_t) (h - path) < len) len = (size_t) (h - path);

	while (i < len && o + 8 < cap) {
		size_t start, n;
		const char *rep;
		while (i < len && path[i] == '/') i++;
		if (i >= len) break;
		start = i;
		while (i < len && path[i] != '/') i++;
		n = i - start;
		if (++segs > 8) {
			memcpy(dst + o, "/\xE2\x80\xA6", 4); /* "/…" */
			o += 4;
			break;
		}
		dst[o++] = '/';
		rep = ol_seg_replacement(path + start, n);
		if (rep) {
			size_t rl = strlen(rep);
			if (o + rl + 1 >= cap) break;
			memcpy(dst + o, rep, rl);
			o += rl;
		} else {
			if (o + n + 1 >= cap) n = cap - o - 2;
			memcpy(dst + o, path + start, n);
			o += n;
		}
	}
	if (o == 0) {
		dst[o++] = '/';
	}
	dst[o] = '\0';
	return o;
}

/*
 * Route template from a regex/placeholder pattern: "(?P<id>\d+)" -> "{id}", CodeIgniter "(:num)" -> "{num}",
 * other groups -> "{param}"; anchors dropped.
 */
size_t ol_route_from_pattern(char *dst, size_t cap, const char *s, size_t n)
{
	size_t i = 0, o = 0;
	while (i < n && o + 2 < cap) {
		char c = s[i];
		if (c == '(') {
			size_t j = i + 1;
			int depth = 1;
			const char *name = "param";
			size_t nlen = 5;
			while (j < n && depth > 0) {
				if (s[j] == '\\') { j += 2; continue; }
				if (s[j] == '(') depth++;
				else if (s[j] == ')') depth--;
				j++;
			}
			if (i + 3 < n && s[i + 1] == '?' && (s[i + 2] == 'P' || s[i + 2] == '<')) {
				size_t k = i + (s[i + 2] == 'P' ? 4 : 3);
				size_t e = k;
				while (e < n && s[e] != '>') e++;
				if (e < n && e > k) {
					name = s + k;
					nlen = e - k;
				}
			} else if (i + 1 < n && s[i + 1] == ':') {
				size_t k = i + 2, e = k;
				while (e < n && s[e] != ')') e++;
				if (e > k) {
					name = s + k;
					nlen = e - k;
				}
			}
			if (o + nlen + 3 < cap) {
				dst[o++] = '{';
				memcpy(dst + o, name, nlen);
				o += nlen;
				dst[o++] = '}';
			}
			i = j;
			if (i < n && (s[i] == '?' || s[i] == '+' || s[i] == '*')) i++;
			continue;
		}
		if (c == '^' || c == '$') { i++; continue; }
		if (c == '\\' && i + 1 < n) { dst[o++] = s[i + 1]; i += 2; continue; }
		if (c == '/' && o > 0 && dst[o - 1] == '/') { i++; continue; }
		dst[o++] = c;
		i++;
	}
	dst[o] = '\0';
	return o;
}

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
		for (c = colon + 1; c < auth_end && isdigit((unsigned char) *c); c++) {
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

/*
 * Outbound HTTP: curl (curl_exec, curl_multi_*), http(s) streams (file_get_contents, fopen), sleep functions.
 * Guzzle and Symfony HttpClient are covered through their curl / stream handlers.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Header injection: curl keeps no readable copy of CURLOPT_HTTPHEADER, so curl_setopt / curl_setopt_array are
 * observed to remember the application's header list per handle; before the request the agent calls curl_setopt
 * itself with that list + traceparent (+ tracestate). Streams: the "http.header" option of the stream context
 * (the default context when none is passed) is extended for the duration of the call and restored afterwards.
 */
#include "ol.h"
#include "ext/standard/file.h"
#include "main/php_streams.h"

#define OL_CURLOPT_URL           10002
#define OL_CURLOPT_HTTPHEADER    10023
#define OL_CURLOPT_CUSTOMREQUEST 10036
#define OL_CURLOPT_POST          47
#define OL_CURLOPT_POSTFIELDS    10015
#define OL_CURLOPT_NOBODY        44
#define OL_CURLOPT_PUT           54
#define OL_CURLOPT_UPLOAD        46
#define OL_CURLOPT_HTTPGET       80
#define OL_CURLINFO_EFFECTIVE_URL 0x100001
#define OL_CURLINFO_RESPONSE_CODE 0x200002

typedef struct {
	zval headers;       /* application's CURLOPT_HTTPHEADER array (UNDEF: none) */
	const char *method; /* arena */
	const char *url;    /* arena */
	uint32_t node;      /* curl_multi span */
} ol_curl;

static void curl_state_dtor(zval *zv)
{
	ol_curl *c = Z_PTR_P(zv);
	zval_ptr_dtor(&c->headers);
	efree(c);
}

static zend_ulong curl_key(zval *ch)
{
	if (ch == NULL) {
		return 0;
	}
	if (Z_TYPE_P(ch) == IS_OBJECT) {
		return (zend_ulong) Z_OBJ_HANDLE_P(ch);
	}
	if (Z_TYPE_P(ch) == IS_RESOURCE) {
		return ((zend_ulong) 1 << 40) | (zend_ulong) Z_RES_HANDLE_P(ch);
	}
	return 0;
}

static ol_curl *curl_state(zval *ch, bool create)
{
	zend_ulong key = curl_key(ch);
	ol_curl *c;
	if (key == 0) {
		return NULL;
	}
	if (OLG(curl) == NULL) {
		if (!create) {
			return NULL;
		}
		ALLOC_HASHTABLE(OLG(curl));
		zend_hash_init(OLG(curl), 8, NULL, curl_state_dtor, 0);
	}
	c = zend_hash_index_find_ptr(OLG(curl), key);
	if (c == NULL && create) {
		c = emalloc(sizeof(ol_curl));
		ZVAL_UNDEF(&c->headers);
		c->method = NULL;
		c->url = NULL;
		c->node = OL_NONE;
		zend_hash_index_update_ptr(OLG(curl), key, c);
	}
	return c;
}

static void curl_track_option(ol_curl *c, zend_long opt, zval *val)
{
	ZVAL_DEREF(val);
	switch (opt) {
		case OL_CURLOPT_HTTPHEADER:
			zval_ptr_dtor(&c->headers);
			ZVAL_UNDEF(&c->headers);
			if (Z_TYPE_P(val) == IS_ARRAY) {
				ZVAL_COPY(&c->headers, val);
			}
			break;
		case OL_CURLOPT_URL:
			if (Z_TYPE_P(val) == IS_STRING) {
				c->url = ol_strdup(Z_STRVAL_P(val), Z_STRLEN_P(val), OL_STR_MAX);
			}
			break;
		case OL_CURLOPT_CUSTOMREQUEST:
			if (Z_TYPE_P(val) == IS_STRING && Z_STRLEN_P(val) > 0) {
				c->method = ol_strdup(Z_STRVAL_P(val), Z_STRLEN_P(val), 16);
			} else {
				c->method = NULL;
			}
			break;
		case OL_CURLOPT_POST:
		case OL_CURLOPT_POSTFIELDS:
			if (zend_is_true(val) && (c->method == NULL || strcmp(c->method, "GET") == 0 || strcmp(c->method, "HEAD") == 0)) {
				c->method = "POST";
			}
			break;
		case OL_CURLOPT_NOBODY:
			if (zend_is_true(val)) c->method = "HEAD";
			break;
		case OL_CURLOPT_PUT:
		case OL_CURLOPT_UPLOAD:
			if (zend_is_true(val)) c->method = "PUT";
			break;
		case OL_CURLOPT_HTTPGET:
			if (zend_is_true(val)) c->method = "GET";
			break;
	}
}

/* curl_init($url = null) */
static void curl_init_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	zval *url = ol_arg(ex, 1);
	ol_curl *c;
	if (rv == NULL || (Z_TYPE_P(rv) != IS_OBJECT && Z_TYPE_P(rv) != IS_RESOURCE)) {
		return;
	}
	c = curl_state(rv, true);
	if (c) {
		zval_ptr_dtor(&c->headers);
		ZVAL_UNDEF(&c->headers);
		c->method = NULL;
		c->node = OL_NONE;
		c->url = (url && Z_TYPE_P(url) == IS_STRING) ? ol_strdup(Z_STRVAL_P(url), Z_STRLEN_P(url), OL_STR_MAX) : NULL;
	}
}

/* curl_setopt($ch, $option, $value) */
static void curl_setopt_begin(zend_execute_data *ex, const ol_hook *h)
{
	zval *opt = ol_arg(ex, 2), *val = ol_arg(ex, 3);
	ol_curl *c;
	if (opt == NULL || val == NULL || Z_TYPE_P(opt) != IS_LONG) {
		return;
	}
	c = curl_state(ol_arg(ex, 1), true);
	if (c) {
		curl_track_option(c, Z_LVAL_P(opt), val);
	}
}

/* curl_setopt_array($ch, $options) */
static void curl_setopt_array_begin(zend_execute_data *ex, const ol_hook *h)
{
	zval *opts = ol_arg(ex, 2), *val;
	zend_ulong idx;
	zend_string *key;
	ol_curl *c;
	if (opts == NULL || Z_TYPE_P(opts) != IS_ARRAY) {
		return;
	}
	c = curl_state(ol_arg(ex, 1), true);
	if (c == NULL) {
		return;
	}
	ZEND_HASH_FOREACH_KEY_VAL(Z_ARRVAL_P(opts), idx, key, val) {
		if (key == NULL) {
			curl_track_option(c, (zend_long) idx, val);
		}
	} ZEND_HASH_FOREACH_END();
}

static void curl_reset_begin(zend_execute_data *ex, const ol_hook *h)
{
	ol_curl *c = curl_state(ol_arg(ex, 1), false);
	if (c) {
		zval_ptr_dtor(&c->headers);
		ZVAL_UNDEF(&c->headers);
		c->method = NULL;
		c->url = NULL;
	}
}

static void curl_close_begin(zend_execute_data *ex, const ol_hook *h)
{
	zend_ulong key = curl_key(ol_arg(ex, 1));
	if (OLG(curl) && key) {
		ol_curl *c = zend_hash_index_find_ptr(OLG(curl), key);
		if (c && c->node != OL_NONE) {
			ol_node_finish(c->node);
		}
		zend_hash_index_del(OLG(curl), key);
	}
}

/* curl_copy_handle($ch): the copy has the same options */
static void curl_copy_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	ol_curl *src = curl_state(ol_arg(ex, 1), false), *dst;
	if (src == NULL || rv == NULL || (Z_TYPE_P(rv) != IS_OBJECT && Z_TYPE_P(rv) != IS_RESOURCE)) {
		return;
	}
	dst = curl_state(rv, true);
	if (dst) {
		zval_ptr_dtor(&dst->headers);
		ZVAL_COPY(&dst->headers, &src->headers);
		dst->method = src->method;
		dst->url = src->url;
		dst->node = OL_NONE;
	}
}

static void curl_inject(zval *ch, ol_curl *c, uint64_t span_id)
{
	char tp[64], line[600];
	size_t tplen;
	zval arr, args[3], rv, *hv;
	bool has_state = false;

	if (!OLG(propagate) || EG(exception)) {
		return;
	}
	tplen = ol_format_traceparent(tp, sizeof(tp), span_id);
	if (tplen == 0) {
		return;
	}
	array_init(&arr);
	if (Z_TYPE(c->headers) == IS_ARRAY) {
		ZEND_HASH_FOREACH_VAL(Z_ARRVAL(c->headers), hv) {
			ZVAL_DEREF(hv);
			if (Z_TYPE_P(hv) == IS_STRING) {
				if (ol_str_starts_ci(Z_STRVAL_P(hv), Z_STRLEN_P(hv), "traceparent:")) {
					zval_ptr_dtor(&arr);
					return; /* the application propagates its own context */
				}
				if (ol_str_starts_ci(Z_STRVAL_P(hv), Z_STRLEN_P(hv), "tracestate:")) {
					has_state = true;
				}
			}
			Z_TRY_ADDREF_P(hv);
			add_next_index_zval(&arr, hv);
		} ZEND_HASH_FOREACH_END();
	}
	snprintf(line, sizeof(line), "traceparent: %s", tp);
	add_next_index_string(&arr, line);
	if (OLG(tracestate)[0] && !has_state) {
		snprintf(line, sizeof(line), "tracestate: %s", OLG(tracestate));
		add_next_index_string(&arr, line);
	}
	ZVAL_COPY_VALUE(&args[0], ch);
	ZVAL_LONG(&args[1], OL_CURLOPT_HTTPHEADER);
	ZVAL_COPY_VALUE(&args[2], &arr);
	ol_call_function(ZEND_STRL("curl_setopt"), &rv, 3, args);
	zval_ptr_dtor(&rv);
	zval_ptr_dtor(&arr);
}

static void curl_span_attrs(ol_node *n, ol_curl *c)
{
	const char *method = c && c->method ? c->method : "GET";
	n->name = method;
	ol_attr_static(n, "http.request.method", method);
	if (c && c->url) {
		ol_url_attrs(n, c->url, strlen(c->url));
	}
}

static void curl_finish(ol_node *n, zval *ch, ol_curl *c)
{
	zval args[2], rv;
	zend_long code = 0, err = 0;

	ZVAL_COPY_VALUE(&args[0], ch);
	ZVAL_LONG(&args[1], OL_CURLINFO_RESPONSE_CODE);
	if (ol_call_function(ZEND_STRL("curl_getinfo"), &rv, 2, args) && Z_TYPE(rv) == IS_LONG) {
		code = Z_LVAL(rv);
	}
	zval_ptr_dtor(&rv);
	if (c == NULL || c->url == NULL) {
		ZVAL_LONG(&args[1], OL_CURLINFO_EFFECTIVE_URL);
		if (ol_call_function(ZEND_STRL("curl_getinfo"), &rv, 2, args) && Z_TYPE(rv) == IS_STRING && Z_STRLEN(rv) > 0) {
			ol_url_attrs(n, Z_STRVAL(rv), Z_STRLEN(rv));
		}
		zval_ptr_dtor(&rv);
	}
	if (ol_call_function(ZEND_STRL("curl_errno"), &rv, 1, args) && Z_TYPE(rv) == IS_LONG) {
		err = Z_LVAL(rv);
	}
	zval_ptr_dtor(&rv);
	if (code > 0) {
		ol_attr_int(n, "http.response.status_code", code);
	}
	if (err != 0) {
		char et[32];
		snprintf(et, sizeof(et), "curl_error_" ZEND_LONG_FMT, err);
		ol_attr_str(n, "error.type", et, strlen(et));
		n->status = OL_STATUS_ERROR;
		if (ol_call_function(ZEND_STRL("curl_error"), &rv, 1, args) && Z_TYPE(rv) == IS_STRING) {
			n->status_msg = ol_strdup(Z_STRVAL(rv), Z_STRLEN(rv), 1024);
		}
		zval_ptr_dtor(&rv);
	} else if (code >= 400) {
		char et[16];
		snprintf(et, sizeof(et), ZEND_LONG_FMT, code);
		ol_attr_str(n, "error.type", et, strlen(et));
		n->status = OL_STATUS_ERROR;
	}
}

/* curl_exec($ch) */
static void curl_exec_begin(zend_execute_data *ex, const ol_hook *h)
{
	zval *ch = ol_arg(ex, 1);
	ol_curl *c;
	uint32_t idx;
	ol_node *n;
	if (ch == NULL || (Z_TYPE_P(ch) != IS_OBJECT && Z_TYPE_P(ch) != IS_RESOURCE)) {
		return;
	}
	c = curl_state(ch, true);
	idx = ol_span_begin(ex, "GET", OL_KIND_CLIENT);
	n = ol_node_at(idx);
	if (n) {
		curl_span_attrs(n, c);
	}
	if (c) {
		curl_inject(ch, c, n ? n->id : ol_current_span_id());
	}
}

static void curl_exec_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	zval *ch = ol_arg(ex, 1);
	ol_node *n = ol_span_end(ex);
	if (n == NULL || ch == NULL) {
		return;
	}
	if (Z_TYPE_P(ch) != IS_OBJECT && Z_TYPE_P(ch) != IS_RESOURCE) {
		return;
	}
	curl_finish(n, ch, curl_state(ch, false));
	if (OL_EXCEPTION()) {
		ol_record_exception(n, EG(exception), true);
	}
}

/* curl_multi_add_handle($mh, $ch): the request runs until curl_multi_remove_handle */
static void curl_multi_add_begin(zend_execute_data *ex, const ol_hook *h)
{
	zval *ch = ol_arg(ex, 2);
	ol_curl *c;
	ol_node *n;
	if (ch == NULL || (Z_TYPE_P(ch) != IS_OBJECT && Z_TYPE_P(ch) != IS_RESOURCE)) {
		return;
	}
	c = curl_state(ch, true);
	if (c == NULL) {
		return;
	}
	if (c->node != OL_NONE) {
		ol_node_finish(c->node);
		c->node = OL_NONE;
	}
	c->node = ol_span_detached("GET", OL_KIND_CLIENT);
	n = ol_node_at(c->node);
	if (n) {
		curl_span_attrs(n, c);
	}
	curl_inject(ch, c, n ? n->id : ol_current_span_id());
}

static void curl_multi_remove_begin(zend_execute_data *ex, const ol_hook *h)
{
	zval *ch = ol_arg(ex, 2);
	ol_curl *c = curl_state(ch, false);
	ol_node *n;
	if (c == NULL || c->node == OL_NONE) {
		return;
	}
	n = ol_node_at(c->node);
	ol_node_finish(c->node);
	c->node = OL_NONE;
	if (n && OL_REC()) {
		curl_finish(n, ch, c);
	}
}

/* ---------------- streams ---------------- */

static php_stream_context *stream_ctx(zval *zctx)
{
	if (zctx && Z_TYPE_P(zctx) == IS_RESOURCE) {
		return (php_stream_context *) zend_fetch_resource_ex(zctx, NULL, php_le_stream_context());
	}
	if (FG(default_context) == NULL) {
		FG(default_context) = php_stream_context_alloc();
	}
	return FG(default_context);
}

static void stream_restore(zend_execute_data *ex)
{
	uint32_t i = OLG(nsaved_ctx);
	if (i == 0 || OLG(saved_ctx)[i - 1].ex != ex) {
		return;
	}
	i--;
	OLG(nsaved_ctx) = i;
	if (OLG(saved_ctx)[i].had) {
		php_stream_context_set_option(OLG(saved_ctx)[i].ctx, "http", "header", &OLG(saved_ctx)[i].orig);
		zval_ptr_dtor(&OLG(saved_ctx)[i].orig);
	} else {
		zval *http = zend_hash_str_find(Z_ARRVAL(OLG(saved_ctx)[i].ctx->options), ZEND_STRL("http"));
		if (http && Z_TYPE_P(http) == IS_ARRAY) {
			SEPARATE_ARRAY(http);
			zend_hash_str_del(Z_ARRVAL_P(http), ZEND_STRL("header"));
		}
	}
}

/* file_get_contents($url, $use_include_path, $context) (arg 3) / fopen($url, $mode, $use_include_path, $context) (arg 4) */
static void stream_begin(zend_execute_data *ex, const ol_hook *h)
{
	zval *url = ol_arg(ex, 1), *zctx, *orig, *mopt;
	php_stream_context *ctx;
	uint32_t idx;
	ol_node *n;
	char tp[64], line[700];
	zval nv;

	if (url == NULL || Z_TYPE_P(url) != IS_STRING ||
			!(ol_str_starts_ci(Z_STRVAL_P(url), Z_STRLEN_P(url), "http://") || ol_str_starts_ci(Z_STRVAL_P(url), Z_STRLEN_P(url), "https://"))) {
		return;
	}
	OLG(http_status) = 0;
	zctx = ol_arg(ex, (uint32_t) h->arg);
	ctx = stream_ctx(zctx);
	mopt = ctx ? php_stream_context_get_option(ctx, "http", "method") : NULL;
	idx = ol_span_begin(ex, "GET", OL_KIND_CLIENT);
	n = ol_node_at(idx);
	if (n) {
		const char *m = (mopt && Z_TYPE_P(mopt) == IS_STRING) ? ol_strdup(Z_STRVAL_P(mopt), Z_STRLEN_P(mopt), 16) : "GET";
		n->name = m ? m : "GET";
		ol_attr_static(n, "http.request.method", n->name);
		ol_url_attrs(n, Z_STRVAL_P(url), Z_STRLEN_P(url));
	}
	if (ctx == NULL || !OLG(propagate) || OLG(nsaved_ctx) >= OL_SAVED_CTX_MAX) {
		return;
	}
	if (ol_format_traceparent(tp, sizeof(tp), n ? n->id : ol_current_span_id()) == 0) {
		return;
	}
	if (OLG(tracestate)[0]) {
		snprintf(line, sizeof(line), "traceparent: %s\r\ntracestate: %s", tp, OLG(tracestate));
	} else {
		snprintf(line, sizeof(line), "traceparent: %s", tp);
	}
	orig = php_stream_context_get_option(ctx, "http", "header");
	if (orig && Z_TYPE_P(orig) == IS_STRING) {
		size_t k, olen = Z_STRLEN_P(orig);
		for (k = 0; k + 12 <= olen; k++) {
			if (zend_binary_strncasecmp(Z_STRVAL_P(orig) + k, 12, "traceparent:", 12, 12) == 0) {
				return; /* the application propagates its own context */
			}
		}
		ZVAL_STR(&nv, strpprintf(0,"%s%s%s", Z_STRVAL_P(orig), (Z_STRLEN_P(orig) && Z_STRVAL_P(orig)[Z_STRLEN_P(orig) - 1] != '\n') ? "\r\n" : "", line));
	} else if (orig && Z_TYPE_P(orig) == IS_ARRAY) {
		zval *hv;
		array_init(&nv);
		ZEND_HASH_FOREACH_VAL(Z_ARRVAL_P(orig), hv) {
			ZVAL_DEREF(hv);
			if (Z_TYPE_P(hv) == IS_STRING && ol_str_starts_ci(Z_STRVAL_P(hv), Z_STRLEN_P(hv), "traceparent:")) {
				zval_ptr_dtor(&nv);
				return;
			}
			Z_TRY_ADDREF_P(hv);
			add_next_index_zval(&nv, hv);
		} ZEND_HASH_FOREACH_END();
		add_next_index_string(&nv, line);
	} else {
		ZVAL_STRING(&nv, line);
	}
	{
		uint32_t i = OLG(nsaved_ctx)++;
		OLG(saved_ctx)[i].ctx = ctx;
		OLG(saved_ctx)[i].ex = ex;
		OLG(saved_ctx)[i].node = idx;
		OLG(saved_ctx)[i].had = orig != NULL;
		if (orig) {
			ZVAL_COPY(&OLG(saved_ctx)[i].orig, orig);
		} else {
			ZVAL_UNDEF(&OLG(saved_ctx)[i].orig);
		}
	}
	php_stream_context_set_option(ctx, "http", "header", &nv);
	zval_ptr_dtor(&nv);
}

static zend_long status_from_headers(zval *arr)
{
	zval *hv;
	zend_long code = 0;
	if (arr == NULL || Z_TYPE_P(arr) != IS_ARRAY) {
		return 0;
	}
	ZEND_HASH_FOREACH_VAL(Z_ARRVAL_P(arr), hv) {
		ZVAL_DEREF(hv);
		if (Z_TYPE_P(hv) == IS_STRING && Z_STRLEN_P(hv) > 9 && strncmp(Z_STRVAL_P(hv), "HTTP/", 5) == 0) {
			const char *sp = memchr(Z_STRVAL_P(hv), ' ', Z_STRLEN_P(hv));
			if (sp) {
				code = ZEND_STRTOL(sp + 1, NULL, 10);
			}
		}
	} ZEND_HASH_FOREACH_END();
	return code;
}

/* $http_response_header as set by the http wrapper in the calling userland frame */
static zval *caller_response_header(zend_execute_data *ex)
{
	zend_execute_data *p = ex->prev_execute_data;
	while (p && (p->func == NULL || !ZEND_USER_CODE(p->func->type))) {
		p = p->prev_execute_data;
	}
	if (p == NULL) {
		return NULL;
	}
	if (ZEND_CALL_INFO(p) & ZEND_CALL_HAS_SYMBOL_TABLE) {
		zval *zv = p->symbol_table ? zend_hash_str_find(p->symbol_table, ZEND_STRL("http_response_header")) : NULL;
		if (zv && Z_TYPE_P(zv) == IS_INDIRECT) {
			zv = Z_INDIRECT_P(zv);
		}
		if (zv) {
			ZVAL_DEREF(zv);
		}
		return zv;
	} else {
		zend_op_array *op = &p->func->op_array;
		int i;
		for (i = 0; i < op->last_var; i++) {
			if (zend_string_equals_literal(op->vars[i], "http_response_header")) {
				zval *zv = ZEND_CALL_VAR_NUM(p, i);
				ZVAL_DEREF(zv);
				return zv;
			}
		}
	}
	return NULL;
}

/*
 * http/https wrapper proxy: the status line is read from the stream right after it is opened. PHP >= 8.0 only
 * stores $http_response_header in the caller when that frame already has the variable or a symbol table, and
 * file_get_contents closes the stream before returning, so this is the only reliable place to see it.
 */
static const php_stream_wrapper *ol_orig_http;
static const php_stream_wrapper *ol_orig_https;
static php_stream_wrapper_ops ol_http_wops;
static php_stream_wrapper_ops ol_https_wops;
static php_stream_wrapper ol_http_wrapper;
static php_stream_wrapper ol_https_wrapper;

static php_stream *ol_http_open(php_stream_wrapper *wrapper, const char *filename, const char *mode, int options,
	zend_string **opened_path, php_stream_context *context STREAMS_DC)
{
	const php_stream_wrapper *orig = wrapper == &ol_https_wrapper ? ol_orig_https : ol_orig_http;
	php_stream *s = orig->wops->stream_opener((php_stream_wrapper *) orig, filename, mode, options, opened_path, context STREAMS_REL_CC);
	if (s && OLG(active) && Z_TYPE(s->wrapperdata) == IS_ARRAY) {
		zend_long code = 0;
		zval *hv;
		ZEND_HASH_FOREACH_VAL(Z_ARRVAL(s->wrapperdata), hv) {
			ZVAL_DEREF(hv);
			if (Z_TYPE_P(hv) == IS_STRING && Z_STRLEN_P(hv) > 9 && strncmp(Z_STRVAL_P(hv), "HTTP/", 5) == 0) {
				const char *sp = memchr(Z_STRVAL_P(hv), ' ', Z_STRLEN_P(hv));
				if (sp) {
					code = ZEND_STRTOL(sp + 1, NULL, 10);
				}
			}
		} ZEND_HASH_FOREACH_END();
		OLG(http_status) = code;
	}
	return s;
}

static void ol_wrap_one(HashTable *ht, const char *scheme, const php_stream_wrapper **orig, php_stream_wrapper_ops *wops, php_stream_wrapper *wrapper)
{
	php_stream_wrapper *w = zend_hash_str_find_ptr(ht, scheme, strlen(scheme));
	if (w == NULL || w == wrapper || w->wops == NULL || w->wops->stream_opener == NULL) {
		return;
	}
	*orig = w;
	*wops = *w->wops;
	wops->stream_opener = ol_http_open;
	*wrapper = *w;
	wrapper->wops = wops;
	zend_hash_str_update_ptr(ht, scheme, strlen(scheme), wrapper);
}

void ol_http_minit(void)
{
	HashTable *ht = php_stream_get_url_stream_wrappers_hash_global();
	if (ht) {
		ol_wrap_one(ht, "http", &ol_orig_http, &ol_http_wops, &ol_http_wrapper);
		ol_wrap_one(ht, "https", &ol_orig_https, &ol_https_wops, &ol_https_wrapper);
	}
}

void ol_http_mshutdown(void)
{
	HashTable *ht = php_stream_get_url_stream_wrappers_hash_global();
	if (ht == NULL) {
		return;
	}
	if (ol_orig_http && zend_hash_str_find_ptr(ht, ZEND_STRL("http")) == &ol_http_wrapper) {
		zend_hash_str_update_ptr(ht, ZEND_STRL("http"), (void *) ol_orig_http);
	}
	if (ol_orig_https && zend_hash_str_find_ptr(ht, ZEND_STRL("https")) == &ol_https_wrapper) {
		zend_hash_str_update_ptr(ht, ZEND_STRL("https"), (void *) ol_orig_https);
	}
}

static void stream_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	ol_node *n;
	zend_long code = 0;
	stream_restore(ex);
	n = ol_span_end(ex);
	if (n == NULL) {
		return;
	}
	if (rv && Z_TYPE_P(rv) != IS_FALSE && OLG(http_status) > 0) {
		code = OLG(http_status);
	} else if (h->arg == 4) {
		if (rv && Z_TYPE_P(rv) == IS_RESOURCE) {
			php_stream *s = (php_stream *) zend_fetch_resource2_ex(rv, NULL, php_file_le_stream(), php_file_le_pstream());
			if (s) {
				code = status_from_headers(&s->wrapperdata);
			}
		}
	} else if (rv && Z_TYPE_P(rv) != IS_FALSE) {
		/* only after a response: on failure the variable may still hold an earlier request's headers */
		code = status_from_headers(caller_response_header(ex));
	}
	if (code > 0) {
		ol_attr_int(n, "http.response.status_code", code);
	}
	if (OL_EXCEPTION()) {
		ol_record_exception(n, EG(exception), true);
	} else if (rv && Z_TYPE_P(rv) == IS_FALSE) {
		n->status = OL_STATUS_ERROR;
		if (code >= 400) {
			char et[16];
			snprintf(et, sizeof(et), ZEND_LONG_FMT, code);
			ol_attr_str(n, "error.type", et, strlen(et));
		} else {
			ol_attr_static(n, "error.type", "_OTHER");
		}
	} else if (code >= 400) {
		char et[16];
		snprintf(et, sizeof(et), ZEND_LONG_FMT, code);
		ol_attr_str(n, "error.type", et, strlen(et));
		n->status = OL_STATUS_ERROR;
	}
}

/* Request end: contexts whose end handler never ran (bailout) are not touched, only the saved copies freed. */
void ol_http_rshutdown(void)
{
	while (OLG(nsaved_ctx) > 0) {
		uint32_t i = --OLG(nsaved_ctx);
		zval_ptr_dtor(&OLG(saved_ctx)[i].orig);
		ZVAL_UNDEF(&OLG(saved_ctx)[i].orig);
	}
}

/* ---------------- sleep ---------------- */

/* sleep/usleep/time_nanosleep/time_sleep_until: internal spans that are part of the function trace */
static void sleep_begin(zend_execute_data *ex, const ol_hook *h)
{
	uint32_t idx;
	ol_node *n;
	if (!OLG(tracing) || OLG(tracer_full)) {
		return;
	}
	if (OLG(seg_kept) >= (uint32_t) OLG(tt_max_segments) || OLG(seg_bytes) + sizeof(ol_node) > OLG(seg_bytes_cap)) {
		OLG(tracer_full) = true;
		OLG(dropped)++;
		return;
	}
	idx = ol_span_begin_flags(ex, h->name, OL_KIND_INTERNAL, OL_NF_SEGMENT);
	n = ol_node_at(idx);
	if (n) {
		OLG(seg_kept)++;
		OLG(seg_bytes) += sizeof(ol_node) + 64;
		ol_attr_static(n, "code.function.name", h->name);
	}
}

static void sleep_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	ol_span_end(ex);
}

const ol_hook ol_hooks_http[] = {
	{"curl_init", NULL, curl_init_end, 0, OL_HF_ANY},
	{"curl_setopt", curl_setopt_begin, NULL, 0, OL_HF_ANY},
	{"curl_setopt_array", curl_setopt_array_begin, NULL, 0, OL_HF_ANY},
	{"curl_reset", curl_reset_begin, NULL, 0, OL_HF_ANY},
	{"curl_close", curl_close_begin, NULL, 0, OL_HF_ANY},
	{"curl_copy_handle", NULL, curl_copy_end, 0, OL_HF_ANY},
	{"curl_exec", curl_exec_begin, curl_exec_end, 0, OL_HF_PROPAGATE},
	{"curl_multi_add_handle", curl_multi_add_begin, NULL, 0, OL_HF_PROPAGATE},
	{"curl_multi_remove_handle", curl_multi_remove_begin, NULL, 0, OL_HF_ANY},
	{"file_get_contents", stream_begin, stream_end, 3, OL_HF_PROPAGATE},
	{"fopen", stream_begin, stream_end, 4, OL_HF_PROPAGATE},
	{"sleep", sleep_begin, sleep_end, 0, 0},
	{"usleep", sleep_begin, sleep_end, 0, 0},
	{"time_nanosleep", sleep_begin, sleep_end, 0, 0},
	{"time_sleep_until", sleep_begin, sleep_end, 0, 0},
	{NULL, NULL, NULL, 0, 0}
};

/*
 * Long-running PHP workers: one transaction per handled request inside one PHP request (the worker process).
 * SPDX-License-Identifier: Apache-2.0
 *
 *   Laravel Octane (Swoole, RoadRunner)  Worker::handle(Request, RequestContext) begin/end; <Client>::respond(): status
 *   RoadRunner (spiral/roadrunner-http)   HttpWorker::waitRequest() end starts, respond()/respondStream() ends
 *                                         (also below PSR7Worker and Octane on RoadRunner, which use HttpWorker)
 *   Swoole / OpenSwoole HTTP servers      the callable registered with Server::on('request') or
 *                                         Coroutine\Http\Server::handle() starts (observed userland callback);
 *                                         Response::end/redirect/sendfile ends; Response::status() sets the status
 *
 * The first worker request ends the process's own CLI transaction without sending it (it would last as long as the
 * worker). Between requests nothing is recorded and no header is propagated. Connection and statement attributes
 * (DSN, host, database: inst_datastores.c) are process state and survive transactions.
 *
 * Concurrency (Swoole coroutines: several requests in progress in one process). The node table and span stack hold
 * one transaction, so when a second request starts while one is running, the running ("primary") transaction stops
 * recording child spans, route names and the function tracer, and each further request is recorded as a root span
 * only ("light" transaction); all of them carry openlog.php.concurrent=true. This lasts until no request is in
 * progress. Outgoing traceparent headers use the context of the request whose handler frame is on the current
 * coroutine's call stack; code that belongs to none of them propagates nothing. No span is ever attached to another
 * request. Not supported: openlog.userland_hooks=0 (callbacks and framework hooks are userland), FrankenPHP workers.
 */
#include "ol.h"

#define OL_WKEY_OCTANE ((zend_ulong) 1 << 52)
#define OL_WKEY_RR     ((zend_ulong) 2 << 52)
#define OL_WKEY_OBJ    ((zend_ulong) 4 << 52) /* | Swoole response object handle */
#define OL_WTX_MAX_AGE_NS (300ULL * 1000000000ULL)

/* ---------------- request descriptions ---------------- */

static void ri_set(ol_str *s, zval *z)
{
	if (z && Z_TYPE_P(z) == IS_STRING) {
		s->p = Z_STRVAL_P(z);
		s->len = Z_STRLEN_P(z);
	}
}

/* case-insensitive header lookup in [name => value] or [name => [values]] */
static zval *hdr_find(zval *arr, const char *lname, size_t len)
{
	zend_string *k;
	zval *v;
	if (arr == NULL || Z_TYPE_P(arr) != IS_ARRAY) {
		return NULL;
	}
	ZEND_HASH_FOREACH_STR_KEY_VAL(Z_ARRVAL_P(arr), k, v) {
		if (k && ZSTR_LEN(k) == len && ol_ascii_ncaseeq(ZSTR_VAL(k), lname, len)) {
			ZVAL_DEREF(v);
			if (Z_TYPE_P(v) == IS_ARRAY) {
				v = zend_hash_index_find(Z_ARRVAL_P(v), 0);
				if (v) {
					ZVAL_DEREF(v);
				}
			}
			return v && Z_TYPE_P(v) == IS_STRING ? v : NULL;
		}
	} ZEND_HASH_FOREACH_END();
	return NULL;
}

/* Symfony HttpFoundation request (Laravel Octane): $server->parameters, $headers->headers */
static void octane_reqinfo(zend_object *req, ol_reqinfo *ri)
{
	zval *server = ol_prop(req, ZEND_STRL("server")), *params = NULL, *headers, *hdrs = NULL, *https;
	memset(ri, 0, sizeof(*ri));
	ri->web = true;
	if (server && Z_TYPE_P(server) == IS_OBJECT) {
		params = ol_prop(Z_OBJ_P(server), ZEND_STRL("parameters"));
	}
	ri_set(&ri->method, ol_array_get(params, ZEND_STRL("REQUEST_METHOD")));
	ri_set(&ri->uri, ol_array_get(params, ZEND_STRL("REQUEST_URI")));
	ri_set(&ri->host, ol_array_get(params, ZEND_STRL("HTTP_HOST")));
	ri_set(&ri->remote_addr, ol_array_get(params, ZEND_STRL("REMOTE_ADDR")));
	ri_set(&ri->user_agent, ol_array_get(params, ZEND_STRL("HTTP_USER_AGENT")));
	ri_set(&ri->protocol, ol_array_get(params, ZEND_STRL("SERVER_PROTOCOL")));
	ri_set(&ri->traceparent, ol_array_get(params, ZEND_STRL("HTTP_TRACEPARENT")));
	ri_set(&ri->tracestate, ol_array_get(params, ZEND_STRL("HTTP_TRACESTATE")));
	https = ol_array_get(params, ZEND_STRL("HTTPS"));
	ri->https = https && Z_TYPE_P(https) == IS_STRING && Z_STRLEN_P(https) > 0 && !OL_ZSTR_EQ_CI(Z_STR_P(https), "off");
	headers = ol_prop(req, ZEND_STRL("headers"));
	if (headers && Z_TYPE_P(headers) == IS_OBJECT) {
		hdrs = ol_prop(Z_OBJ_P(headers), ZEND_STRL("headers"));
	}
	if (ri->traceparent.p == NULL) {
		ri_set(&ri->traceparent, hdr_find(hdrs, ZEND_STRL("traceparent")));
		ri_set(&ri->tracestate, hdr_find(hdrs, ZEND_STRL("tracestate")));
	}
	if (ri->host.p == NULL) {
		ri_set(&ri->host, hdr_find(hdrs, ZEND_STRL("host")));
	}
}

/* Spiral\RoadRunner\Http\Request: method, uri (absolute URL), headers [name => [values]], remoteAddr, protocol */
static void rr_reqinfo(zend_object *req, ol_reqinfo *ri)
{
	zval *uri = ol_prop(req, ZEND_STRL("uri")), *hdrs = ol_prop(req, ZEND_STRL("headers"));
	memset(ri, 0, sizeof(*ri));
	ri->web = true;
	ri_set(&ri->method, ol_prop(req, ZEND_STRL("method")));
	ri_set(&ri->remote_addr, ol_prop(req, ZEND_STRL("remoteAddr")));
	ri_set(&ri->protocol, ol_prop(req, ZEND_STRL("protocol")));
	if (uri && Z_TYPE_P(uri) == IS_STRING) {
		const char *u = Z_STRVAL_P(uri), *e = u + Z_STRLEN_P(uri), *slash;
		size_t l = Z_STRLEN_P(uri);
		if (ol_str_starts_ci(u, l, "https://")) {
			ri->https = true;
			u += 8;
		} else if (ol_str_starts_ci(u, l, "http://")) {
			u += 7;
		} else {
			ri->uri.p = u;
			ri->uri.len = l;
			u = NULL;
		}
		if (u) {
			slash = memchr(u, '/', (size_t) (e - u));
			ri->host.p = u;
			ri->host.len = (size_t) ((slash ? slash : e) - u);
			if (slash) {
				ri->uri.p = slash;
				ri->uri.len = (size_t) (e - slash);
			} else {
				ri->uri.p = "/";
				ri->uri.len = 1;
			}
		}
	}
	ri_set(&ri->traceparent, hdr_find(hdrs, ZEND_STRL("traceparent")));
	ri_set(&ri->tracestate, hdr_find(hdrs, ZEND_STRL("tracestate")));
	ri_set(&ri->user_agent, hdr_find(hdrs, ZEND_STRL("user-agent")));
	if (ri->host.p == NULL) {
		ri_set(&ri->host, hdr_find(hdrs, ZEND_STRL("host")));
	}
}

/* Swoole\Http\Request / OpenSwoole\Http\Request: $server (lower-case keys), $header (lower-case names) */
static void swoole_reqinfo(zend_object *req, ol_reqinfo *ri)
{
	zval *server = ol_prop(req, ZEND_STRL("server")), *hdr = ol_prop(req, ZEND_STRL("header"));
	memset(ri, 0, sizeof(*ri));
	ri->web = true;
	ri_set(&ri->method, ol_array_get(server, ZEND_STRL("request_method")));
	ri_set(&ri->uri, ol_array_get(server, ZEND_STRL("request_uri")));
	ri_set(&ri->remote_addr, ol_array_get(server, ZEND_STRL("remote_addr")));
	ri_set(&ri->protocol, ol_array_get(server, ZEND_STRL("server_protocol")));
	ri_set(&ri->host, hdr_find(hdr, ZEND_STRL("host")));
	ri_set(&ri->user_agent, hdr_find(hdr, ZEND_STRL("user-agent")));
	ri_set(&ri->traceparent, hdr_find(hdr, ZEND_STRL("traceparent")));
	ri_set(&ri->tracestate, hdr_find(hdr, ZEND_STRL("tracestate")));
}

/* ---------------- transactions ---------------- */

static void ol_wtx_emit_free(ol_wtx *t)
{
	if (t->sampled) {
		ol_emit_light(t);
	}
	t->key = 0;
	t->frame = NULL;
	if (OLG(nwtx) > 0) {
		OLG(nwtx)--;
	}
}

static void ol_worker_settle(void)
{
	if (!OLG(wprimary) && OLG(nwtx) == 0) {
		OLG(concurrent) = false;
	}
}

static void ol_primary_finish(zend_long status_if_unset)
{
	if (OLG(txn_status) <= 0 && status_if_unset > 0) {
		OLG(txn_status) = status_if_unset;
	}
	OLG(wprimary) = false;
	OLG(wkey) = 0;
	OLG(wframe) = NULL;
	ol_txn_finish(true);
}

static void ol_clean_copy(char *dst, size_t cap, const char *s, size_t len)
{
	size_t n = 0;
	if (s && cap > 1) {
		n = ol_utf8_clean(dst, s, len, cap - 1);
	}
	if (cap > 0) {
		dst[n] = '\0';
	}
}

static void ol_light_begin(zend_execute_data *frame, const ol_reqinfo *ri, zend_ulong key)
{
	ol_wtx *t = NULL;
	uint32_t i;
	bool parent_ok;

	if (OLG(wtx) == NULL) {
		OLG(wtx) = ecalloc(OL_WTX_MAX, sizeof(ol_wtx));
	}
	for (i = 0; i < OL_WTX_MAX; i++) {
		if (OLG(wtx)[i].key == 0) {
			t = &OLG(wtx)[i];
			break;
		}
	}
	if (t == NULL) {
		OLG(c_dropped_messages)++;
		OLG(p_dropped_messages)++;
		return;
	}
	memset(t, 0, sizeof(*t));
	t->key = key;
	t->frame = frame;
	t->start_mono = ol_mono_ns();
	t->start_unix = ol_unix_ns();
	t->sampled = ol_trace_decision(&ri->traceparent, &ri->tracestate, t->trace_id, &t->remote_parent, &t->trace_flags,
		t->tracestate, sizeof(t->tracestate), &t->ratio, &parent_ok);
	t->span_id = ol_rand64();
	{
		const char *m = ri->method.p ? ri->method.p : "GET";
		size_t ml = ri->method.p ? ri->method.len : 3;
		for (i = 0; i < 16 && i < ml; i++) {
			t->method[i] = OL_TOUPPER(m[i]);
		}
		t->method[i] = '\0';
		ol_clean_copy(t->method, sizeof(t->method), t->method, i);
	}
	if (ri->uri.p) {
		const char *p;
		size_t l = ol_req_path(&ri->uri, &p);
		ol_clean_copy(t->path, sizeof(t->path), p, l);
	} else {
		strcpy(t->path, "/");
	}
	if (ri->host.p) {
		size_t hl = ol_req_host(&ri->host, &t->port);
		ol_clean_copy(t->host, sizeof(t->host), ri->host.p, hl);
	}
	ol_clean_copy(t->client, sizeof(t->client), ri->remote_addr.p, ri->remote_addr.len);
	ol_clean_copy(t->ua, sizeof(t->ua), ri->user_agent.p, ri->user_agent.len);
	if (ri->protocol.p && ri->protocol.len > 5 && strncmp(ri->protocol.p, "HTTP/", 5) == 0) {
		ol_clean_copy(t->proto, sizeof(t->proto), ri->protocol.p + 5, ri->protocol.len - 5);
	}
	t->https = ri->https;
	OLG(nwtx)++;
	OLG(c_requests)++;
}

/* A worker request starts. frame: the handler's frame (coroutine identity) or NULL; key: identifies the request. */
static void ol_worker_begin(zend_execute_data *frame, const ol_reqinfo *ri, zend_ulong key)
{
	uint64_t now = ol_mono_ns();
	uint32_t i;

	if (!OLG(worker_mode)) {
		OLG(worker_mode) = true;
		ol_txn_finish(false); /* the CLI transaction of the worker process is not a request */
	}
	/* requests whose end was never seen */
	if (OLG(wprimary) && (OLG(wkey) == key || now - OLG(wstart) > OL_WTX_MAX_AGE_NS)) {
		ol_primary_finish(0);
	}
	for (i = 0; OLG(wtx) && i < OL_WTX_MAX; i++) {
		ol_wtx *t = &OLG(wtx)[i];
		if (t->key && (t->key == key || now - t->start_mono > OL_WTX_MAX_AGE_NS)) {
			ol_wtx_emit_free(t);
		}
	}
	ol_worker_settle();
	if (OLG(wprimary) || OLG(nwtx) > 0) {
		if (!OLG(concurrent)) {
			OLG(concurrent) = true;
			if (OLG(wprimary) && OL_REC()) {
				ol_attr_bool(ol_node_at(0), "openlog.php.concurrent", true);
			}
			ol_sampler_disarm();
			OLG(tracing) = false;
		}
		ol_light_begin(frame, ri, key);
		return;
	}
	ol_txn_start(ri);
	OLG(wprimary) = true;
	OLG(wkey) = key;
	OLG(wframe) = frame;
	OLG(wstart) = now;
}

static void ol_worker_end(zend_ulong key, zend_long status_if_unset)
{
	uint32_t i;
	if (OLG(wprimary) && OLG(wkey) == key) {
		ol_primary_finish(status_if_unset);
		ol_worker_settle();
		return;
	}
	for (i = 0; OLG(wtx) && i < OL_WTX_MAX; i++) {
		ol_wtx *t = &OLG(wtx)[i];
		if (t->key == key && key) {
			if (t->status <= 0) {
				t->status = status_if_unset;
			}
			ol_wtx_emit_free(t);
			break;
		}
	}
	ol_worker_settle();
}

static void ol_worker_status(zend_ulong key, zend_long status)
{
	uint32_t i;
	if (status <= 0) {
		return;
	}
	if (OLG(wprimary) && OLG(wkey) == key) {
		OLG(txn_status) = status;
		return;
	}
	for (i = 0; OLG(wtx) && i < OL_WTX_MAX; i++) {
		if (OLG(wtx)[i].key == key && key) {
			OLG(wtx)[i].status = status;
			return;
		}
	}
}

/* Which request the running code belongs to: 0 the primary transaction, 1 a light one (*out), -1 none. */
int ol_worker_context(const ol_wtx **out)
{
	zend_execute_data *e;
	uint32_t i;
	if (!OLG(concurrent)) {
		return OLG(active) ? 0 : -1;
	}
	for (e = EG(current_execute_data); e; e = e->prev_execute_data) {
		if (OLG(wprimary) && OLG(wframe) == e) {
			return 0;
		}
		for (i = 0; OLG(nwtx) && OLG(wtx) && i < OL_WTX_MAX; i++) {
			if (OLG(wtx)[i].key && OLG(wtx)[i].frame == e) {
				*out = &OLG(wtx)[i];
				return 1;
			}
		}
	}
	return -1;
}

/* Request end of the worker process: requests still in progress are sent as they are. */
void ol_worker_rshutdown(void)
{
	uint32_t i;
	for (i = 0; OLG(wtx) && i < OL_WTX_MAX; i++) {
		if (OLG(wtx)[i].key) {
			ol_wtx_emit_free(&OLG(wtx)[i]);
		}
	}
	if (OLG(wprimary)) {
		OLG(wprimary) = false;
		OLG(wkey) = 0;
		OLG(wframe) = NULL; /* ol_txn_finish(true) in RSHUTDOWN sends it */
	}
}

/* ---------------- Laravel Octane ---------------- */

/* Laravel\Octane\Worker::handle(Request $request, RequestContext $context) */
static void octane_handle_begin(zend_execute_data *ex, const ol_hook *h)
{
	zval *req = ol_arg(ex, 1);
	ol_reqinfo ri;
	if (req == NULL || Z_TYPE_P(req) != IS_OBJECT) {
		return;
	}
	if (OLG(wprimary) && OLG(wkey) == OL_WKEY_RR) {
		return; /* Octane on RoadRunner: HttpWorker already started this request */
	}
	octane_reqinfo(Z_OBJ_P(req), &ri);
	ol_worker_begin(ex, &ri, OL_WKEY_OCTANE);
}

static void octane_handle_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	ol_worker_end(OL_WKEY_OCTANE, OL_EXCEPTION() ? 500 : 0);
}

/* <Swoole|RoadRunner|FrankenPhp>Client::respond(RequestContext $context, OctaneResponse $octaneResponse) */
static void octane_respond_begin(zend_execute_data *ex, const ol_hook *h)
{
	zval *or = ol_arg(ex, 2), *resp, *code;
	if (or == NULL || Z_TYPE_P(or) != IS_OBJECT) {
		return;
	}
	resp = ol_prop(Z_OBJ_P(or), ZEND_STRL("response"));
	code = resp && Z_TYPE_P(resp) == IS_OBJECT ? ol_prop(Z_OBJ_P(resp), ZEND_STRL("statusCode")) : NULL;
	if (code && Z_TYPE_P(code) == IS_LONG) {
		ol_worker_status(OL_WKEY_OCTANE, Z_LVAL_P(code));
		ol_worker_status(OL_WKEY_RR, Z_LVAL_P(code));
	}
}

/* ---------------- RoadRunner ---------------- */

/* Spiral\RoadRunner\Http\HttpWorker::waitRequest(): ?Request */
static void rr_wait_begin(zend_execute_data *ex, const ol_hook *h)
{
	if (OLG(wprimary) && OLG(wkey) == OL_WKEY_RR) {
		ol_worker_end(OL_WKEY_RR, 0); /* the previous request was never answered through respond() */
	}
}

static void rr_wait_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	ol_reqinfo ri;
	if (rv == NULL) {
		return;
	}
	ZVAL_DEREF(rv);
	if (Z_TYPE_P(rv) != IS_OBJECT) {
		return;
	}
	rr_reqinfo(Z_OBJ_P(rv), &ri);
	ol_worker_begin(NULL, &ri, OL_WKEY_RR);
}

/* HttpWorker::respond(int $status, ...) / respondStream(int $status, ...) */
static void rr_respond_begin(zend_execute_data *ex, const ol_hook *h)
{
	zval *status = ol_arg(ex, 1);
	if (status && Z_TYPE_P(status) == IS_LONG) {
		ol_worker_status(OL_WKEY_RR, Z_LVAL_P(status));
	}
}

static void rr_respond_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	ol_worker_end(OL_WKEY_RR, OL_EXCEPTION() ? 500 : 200);
}

/* ---------------- Swoole / OpenSwoole ---------------- */

/* Server::on(string $event, callable $callback) (arg 0) / Coroutine\Http\Server::handle(string $pattern, callable) (arg 1) */
static void swoole_on_begin(zend_execute_data *ex, const ol_hook *h)
{
	zval *ev = ol_arg(ex, 1), *cb = ol_arg(ex, 2);
	zend_fcall_info_cache fcc;
	char *err = NULL;
	zend_function *fn;
	uint32_t i;

	if (cb == NULL || (h->arg == 0 && (ev == NULL || Z_TYPE_P(ev) != IS_STRING || !OL_ZSTR_EQ_CI(Z_STR_P(ev), "request")))) {
		return;
	}
	memset(&fcc, 0, sizeof(fcc));
	if (!zend_is_callable_ex(cb, NULL, 0, NULL, &fcc, &err)) {
		if (err) {
			efree(err);
		}
		return;
	}
	if (err) {
		efree(err);
	}
	fn = fcc.function_handler;
	if (fn == NULL || (fn->common.fn_flags & ZEND_ACC_CALL_VIA_TRAMPOLINE)) {
#if PHP_VERSION_ID >= 80000
		zend_release_fcall_info_cache(&fcc);
#endif
		return;
	}
	if (fn->type != ZEND_USER_FUNCTION || OLG(nworker_cbs) >= sizeof(OLG(worker_cbs)) / sizeof(OLG(worker_cbs)[0])) {
		return;
	}
	for (i = 0; i < OLG(nworker_cbs); i++) {
		if (OLG(worker_cbs)[i] == fn->op_array.opcodes) {
			return;
		}
	}
	OLG(worker_cbs)[OLG(nworker_cbs)++] = fn->op_array.opcodes;
}

bool ol_worker_callback(zend_function *fn)
{
	uint32_t i;
	if (fn->type != ZEND_USER_FUNCTION) {
		return false;
	}
	for (i = 0; i < OLG(nworker_cbs); i++) {
		if (OLG(worker_cbs)[i] == fn->op_array.opcodes) {
			return true;
		}
	}
	return false;
}

static bool swoole_request_class(zend_object *obj)
{
	zend_string *n = obj->ce->name;
	return ZSTR_LEN(n) >= 12 && ol_ascii_ncaseeq(ZSTR_VAL(n) + ZSTR_LEN(n) - 12, "http\\request", 12);
}

/* the observed request callback (Request $request, Response $response) */
void ol_worker_callback_begin(zend_execute_data *ex)
{
	zval *req = ol_arg(ex, 1), *resp = ol_arg(ex, 2);
	ol_reqinfo ri;
	if (OLG(in_call) || req == NULL || resp == NULL || Z_TYPE_P(req) != IS_OBJECT || Z_TYPE_P(resp) != IS_OBJECT ||
			!swoole_request_class(Z_OBJ_P(req))) {
		return;
	}
	swoole_reqinfo(Z_OBJ_P(req), &ri);
	ol_worker_begin(ex, &ri, OL_WKEY_OBJ | (zend_ulong) Z_OBJ_HANDLE_P(resp));
}

/* callback returned: a request whose response was not ended explicitly ends now */
void ol_worker_callback_end(zend_execute_data *ex)
{
	uint32_t i;
	if (OLG(wprimary) && OLG(wframe) == ex) {
		ol_worker_end(OLG(wkey), OL_EXCEPTION() ? 500 : 200);
		return;
	}
	for (i = 0; OLG(wtx) && i < OL_WTX_MAX; i++) {
		if (OLG(wtx)[i].key && OLG(wtx)[i].frame == ex) {
			ol_worker_end(OLG(wtx)[i].key, OL_EXCEPTION() ? 500 : 200);
			return;
		}
	}
}

static zend_ulong swoole_response_key(zend_execute_data *ex)
{
	zval *self = ol_this(ex);
	return self ? OL_WKEY_OBJ | (zend_ulong) Z_OBJ_HANDLE_P(self) : 0;
}

/* Response::status(int $http_code, ...) */
static void swoole_status_begin(zend_execute_data *ex, const ol_hook *h)
{
	zval *code = ol_arg(ex, 1);
	zend_ulong key = swoole_response_key(ex);
	if (key && code && Z_TYPE_P(code) == IS_LONG) {
		ol_worker_status(key, Z_LVAL_P(code));
	}
}

/* Response::redirect(string $location, int $http_code = 302) */
static void swoole_redirect_begin(zend_execute_data *ex, const ol_hook *h)
{
	zval *code = ol_arg(ex, 2);
	zend_ulong key = swoole_response_key(ex);
	if (key) {
		ol_worker_status(key, code && Z_TYPE_P(code) == IS_LONG ? Z_LVAL_P(code) : 302);
	}
}

/* Response::end() / redirect() / sendfile(): the response is complete */
static void swoole_end_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	zend_ulong key = swoole_response_key(ex);
	if (key) {
		ol_worker_end(key, 200);
	}
}

#define OL_WF OL_HF_ALWAYS
const ol_hook ol_hooks_workers[] = {
	{"laravel\\octane\\worker::handle", octane_handle_begin, octane_handle_end, 0, OL_WF},
	{"laravel\\octane\\swoole\\swooleclient::respond", octane_respond_begin, NULL, 0, OL_WF},
	{"laravel\\octane\\roadrunner\\roadrunnerclient::respond", octane_respond_begin, NULL, 0, OL_WF},
	{"spiral\\roadrunner\\http\\httpworker::waitrequest", rr_wait_begin, rr_wait_end, 0, OL_WF},
	{"spiral\\roadrunner\\http\\httpworker::respond", rr_respond_begin, rr_respond_end, 0, OL_WF},
	{"spiral\\roadrunner\\http\\httpworker::respondstream", rr_respond_begin, rr_respond_end, 0, OL_WF},
	{"swoole\\server::on", swoole_on_begin, NULL, 0, OL_WF},
	{"swoole\\http\\server::on", swoole_on_begin, NULL, 0, OL_WF},
	{"swoole\\coroutine\\http\\server::handle", swoole_on_begin, NULL, 1, OL_WF},
	{"swoole\\http\\response::status", swoole_status_begin, NULL, 0, OL_WF},
	{"swoole\\http\\response::end", NULL, swoole_end_end, 0, OL_WF},
	{"swoole\\http\\response::redirect", swoole_redirect_begin, swoole_end_end, 0, OL_WF},
	{"swoole\\http\\response::sendfile", NULL, swoole_end_end, 0, OL_WF},
	{"openswoole\\server::on", swoole_on_begin, NULL, 0, OL_WF},
	{"openswoole\\http\\server::on", swoole_on_begin, NULL, 0, OL_WF},
	{"openswoole\\coroutine\\http\\server::handle", swoole_on_begin, NULL, 1, OL_WF},
	{"openswoole\\http\\response::status", swoole_status_begin, NULL, 0, OL_WF},
	{"openswoole\\http\\response::end", NULL, swoole_end_end, 0, OL_WF},
	{"openswoole\\http\\response::redirect", swoole_redirect_begin, swoole_end_end, 0, OL_WF},
	{"openswoole\\http\\response::sendfile", NULL, swoole_end_end, 0, OL_WF},
	{NULL, NULL, NULL, 0, 0}
};

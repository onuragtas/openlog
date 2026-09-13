/*
 * Module entry, ini settings (contract §4), request lifecycle, PHP functions.
 * SPDX-License-Identifier: Apache-2.0
 */
#include "ol.h"
#include "ext/standard/info.h"
#include "zend_objects_API.h"

#include <ctype.h>
#include <unistd.h>

ZEND_DECLARE_MODULE_GLOBALS(openlog)

PHP_INI_BEGIN()
	STD_PHP_INI_BOOLEAN("openlog.enabled", "1", PHP_INI_SYSTEM, OnUpdateBool, enabled, zend_openlog_globals, openlog_globals)
	STD_PHP_INI_ENTRY("openlog.service_name", "", PHP_INI_ALL, OnUpdateString, service_name, zend_openlog_globals, openlog_globals)
	STD_PHP_INI_ENTRY("openlog.service_namespace", "", PHP_INI_ALL, OnUpdateString, service_namespace, zend_openlog_globals, openlog_globals)
	STD_PHP_INI_ENTRY("openlog.service_version", "", PHP_INI_ALL, OnUpdateString, service_version, zend_openlog_globals, openlog_globals)
	STD_PHP_INI_ENTRY("openlog.environment", "", PHP_INI_ALL, OnUpdateString, environment, zend_openlog_globals, openlog_globals)
	STD_PHP_INI_ENTRY("openlog.transport", "unix:///run/openlog-infra-agent/php.sock", PHP_INI_SYSTEM, OnUpdateString, transport, zend_openlog_globals, openlog_globals)
	STD_PHP_INI_ENTRY("openlog.sampling_ratio", "1.0", PHP_INI_ALL, OnUpdateReal, sampling_ratio, zend_openlog_globals, openlog_globals)
	STD_PHP_INI_ENTRY("openlog.capture_query_text", "sanitized", PHP_INI_ALL, OnUpdateString, capture_query_text, zend_openlog_globals, openlog_globals)
	STD_PHP_INI_BOOLEAN("openlog.transaction_tracer.enabled", "1", PHP_INI_SYSTEM | PHP_INI_PERDIR, OnUpdateBool, tt_enabled, zend_openlog_globals, openlog_globals)
	STD_PHP_INI_ENTRY("openlog.transaction_tracer.threshold_ms", "500", PHP_INI_ALL, OnUpdateLong, tt_threshold_ms, zend_openlog_globals, openlog_globals)
	STD_PHP_INI_ENTRY("openlog.transaction_tracer.max_segments", "2000", PHP_INI_ALL, OnUpdateLong, tt_max_segments, zend_openlog_globals, openlog_globals)
	STD_PHP_INI_ENTRY("openlog.transaction_tracer.min_segment_ms", "1", PHP_INI_ALL, OnUpdateLong, tt_min_segment_ms, zend_openlog_globals, openlog_globals)
	STD_PHP_INI_ENTRY("openlog.transaction_tracer.max_memory_kb", "4096", PHP_INI_ALL, OnUpdateLong, tt_max_memory_kb, zend_openlog_globals, openlog_globals)
	STD_PHP_INI_ENTRY("openlog.log_level", "warning", PHP_INI_ALL, OnUpdateString, log_level, zend_openlog_globals, openlog_globals)
PHP_INI_END()

static PHP_GINIT_FUNCTION(openlog)
{
#if defined(COMPILE_DL_OPENLOG) && defined(ZTS)
	ZEND_TSRMLS_CACHE_UPDATE();
#endif
	memset(openlog_globals, 0, sizeof(*openlog_globals));
	openlog_globals->fd = -1;
	openlog_globals->stack = &openlog_globals->main_stack;
	openlog_globals->log_level_n = OL_LOG_WARNING;
	ZVAL_UNDEF(&openlog_globals->end_this);
}

static PHP_GSHUTDOWN_FUNCTION(openlog)
{
	int i;
	ol_chunk *c = openlog_globals->arena_first;
	for (i = 0; i < OL_MAX_CHUNKS; i++) {
		free(openlog_globals->chunks[i]);
		openlog_globals->chunks[i] = NULL;
	}
	while (c) {
		ol_chunk *n = c->next;
		free(c);
		c = n;
	}
	openlog_globals->arena_first = NULL;
	free(openlog_globals->main_stack.f);
	openlog_globals->main_stack.f = NULL;
	if (openlog_globals->fd >= 0) {
		close(openlog_globals->fd);
		openlog_globals->fd = -1;
	}
}

static void ol_derive_settings(void)
{
	const char *q = OLG(capture_query_text), *l = OLG(log_level);
	OLG(query_mode) = OL_QUERY_SANITIZED;
	if (q && strcasecmp(q, "raw") == 0) {
		OLG(query_mode) = OL_QUERY_RAW;
	} else if (q && (strcasecmp(q, "off") == 0 || strcmp(q, "0") == 0 || *q == '\0')) {
		OLG(query_mode) = OL_QUERY_OFF;
	}
	OLG(log_level_n) = OL_LOG_WARNING;
	if (l) {
		if (strcasecmp(l, "off") == 0 || strcasecmp(l, "none") == 0) OLG(log_level_n) = OL_LOG_OFF;
		else if (strcasecmp(l, "error") == 0) OLG(log_level_n) = OL_LOG_ERROR;
		else if (strcasecmp(l, "info") == 0) OLG(log_level_n) = OL_LOG_INFO;
		else if (strcasecmp(l, "debug") == 0) OLG(log_level_n) = OL_LOG_DEBUG;
	}
}

PHP_MINIT_FUNCTION(openlog)
{
#if defined(COMPILE_DL_OPENLOG) && defined(ZTS)
	ZEND_TSRMLS_CACHE_UPDATE();
#endif
	REGISTER_INI_ENTRIES();
	ol_derive_settings();
	if (OLG(enabled)) {
		ol_hooks_minit();
		ol_http_minit();
	}
	return SUCCESS;
}

PHP_MSHUTDOWN_FUNCTION(openlog)
{
	if (OLG(enabled)) {
		ol_http_mshutdown();
		ol_hooks_mshutdown();
	}
	UNREGISTER_INI_ENTRIES();
	return SUCCESS;
}

PHP_RINIT_FUNCTION(openlog)
{
#if defined(COMPILE_DL_OPENLOG) && defined(ZTS)
	ZEND_TSRMLS_CACHE_UPDATE();
#endif
	OLG(active) = false;
	OLG(recording) = false;
	OLG(tracing) = false;
	if (!OLG(enabled)) {
		return SUCCESS;
	}
	ol_derive_settings();
	OLG(is_cli) = strcmp(sapi_module.name, "cli") == 0 || strcmp(sapi_module.name, "phpdbg") == 0;
	OLG(nnodes) = 0;
	OLG(nspans) = 0;
	OLG(seg_kept) = 0;
	OLG(dropped) = 0;
	OLG(seg_bytes) = 0;
	OLG(seg_bytes_cap) = OLG(tt_max_memory_kb) > 0 ? (uint64_t) OLG(tt_max_memory_kb) * 1024 : 0;
	OLG(min_segment_ns) = OLG(tt_min_segment_ms) > 0 ? (uint64_t) OLG(tt_min_segment_ms) * 1000000ULL : 0;
	OLG(tracer_full) = false;
	OLG(route) = NULL;
	OLG(route_prio) = 0;
	OLG(route_attr) = NULL;
	OLG(nreported) = 0;
	OLG(conns) = NULL;
	OLG(stmts) = NULL;
	OLG(curl) = NULL;
	OLG(pg_last_conn_key_set) = 0;
	OLG(wp_tpl_ex) = NULL;
	OLG(nsaved_ctx) = 0;
	OLG(in_call) = false;
	OLG(end_ex) = NULL;
	OLG(stack) = &OLG(main_stack);
	ol_stack_reset(&OLG(main_stack));
	ol_arena_reset();
	OLG(req_mono) = ol_mono_ns();
	OLG(req_unix) = ol_unix_ns();
	OLG(applied_ratio) = 1.0;
	ol_process_info();

	OLG(active) = true;
	OLG(c_requests)++;
	ol_request_context();
	OLG(tracing) = OLG(recording) && OLG(tt_enabled) && OLG(tt_max_segments) > 0 && OLG(seg_bytes_cap) > 0;
	return SUCCESS;
}

static void ol_finalize_root(void)
{
	ol_node *root = ol_node_at(0);
	char name[1200];
	int len;

	if (root == NULL) {
		return;
	}
	ol_close_open_nodes();
	root->dur = ol_mono_ns() - OLG(req_mono);
	root->flags |= OL_NF_ENDED;

	if (OLG(is_cli)) {
		const char *script = "-";
		if (SG(request_info).argc > 0 && SG(request_info).argv && SG(request_info).argv[0]) {
			script = SG(request_info).argv[0];
		}
		if (strcmp(script, "Standard input code") == 0) {
			script = "-r";
		}
		len = snprintf(name, sizeof(name), "php %s", script);
		root->name = ol_strdup(name, len > 0 && (size_t) len < sizeof(name) ? (size_t) len : sizeof(name) - 1, sizeof(name));
		if (SG(request_info).path_translated) {
			ol_attr_cstr(root, "code.file.path", SG(request_info).path_translated);
		}
		if (EG(exit_status) != 0) {
			ol_attr_int(root, "process.exit.code", EG(exit_status));
		}
	} else {
		const char *method = SG(request_info).request_method ? SG(request_info).request_method : "GET";
		int code = SG(sapi_headers).http_response_code;
		char mbuf[17];
		size_t i;
		for (i = 0; i < 16 && method[i]; i++) {
			mbuf[i] = (char) toupper((unsigned char) method[i]);
		}
		mbuf[i] = '\0';
		if (code > 0) {
			ol_attr_int(root, "http.response.status_code", code);
			if (code >= 500) {
				root->status = OL_STATUS_ERROR;
			}
		}
		if (OLG(route)) {
			len = snprintf(name, sizeof(name), "%s %s", mbuf, OLG(route));
			if (OLG(route_attr)) {
				ol_attr_static(root, "http.route", OLG(route_attr));
			}
		} else {
			ol_attr *p = ol_attr_find(root, "url.path");
			char norm[1024];
			ol_normalize_path(norm, sizeof(norm), p ? p->v.s.p : "/", p ? p->v.s.len : 1);
			len = snprintf(name, sizeof(name), "%s %s", mbuf, norm);
		}
		root->name = ol_strdup(name, len > 0 && (size_t) len < sizeof(name) ? (size_t) len : sizeof(name) - 1, sizeof(name));
	}
	if (root->events && root->status != OL_STATUS_ERROR) {
		root->status = OL_STATUS_ERROR;
	}
	if (OLG(p_send_errors) > 0) {
		ol_attr_int(root, "openlog.php.send_errors", OLG(p_send_errors));
	}
	if (OLG(p_dropped_messages) > 0) {
		ol_attr_int(root, "openlog.php.dropped_messages", OLG(p_dropped_messages));
	}
}

static void ol_request_cleanup(void)
{
	int i;
	uint32_t k;
	for (k = 0; k < OLG(nreported); k++) {
		OBJ_RELEASE(OLG(last_reported)[k]);
	}
	OLG(nreported) = 0;
	ol_http_rshutdown();
	if (OLG(conns)) {
		zend_hash_destroy(OLG(conns));
		FREE_HASHTABLE(OLG(conns));
		OLG(conns) = NULL;
	}
	if (OLG(stmts)) {
		zend_hash_destroy(OLG(stmts));
		FREE_HASHTABLE(OLG(stmts));
		OLG(stmts) = NULL;
	}
	if (OLG(curl)) {
		zend_hash_destroy(OLG(curl));
		FREE_HASHTABLE(OLG(curl));
		OLG(curl) = NULL;
	}
	ol_fiber_stacks_free();
	ol_stack_reset(&OLG(main_stack));
	ol_arena_release();
	for (i = 2; i < OL_MAX_CHUNKS && OLG(chunks)[i]; i++) {
		free(OLG(chunks)[i]);
		OLG(chunks)[i] = NULL;
	}
	OLG(nnodes) = 0;
	OLG(active) = false;
	OLG(recording) = false;
	OLG(tracing) = false;
}

PHP_RSHUTDOWN_FUNCTION(openlog)
{
	if (!OLG(enabled)) {
		return SUCCESS;
	}
	if (OLG(active) && OLG(recording)) {
		ol_finalize_root();
		ol_emit();
	}
	ol_request_cleanup();
	return SUCCESS;
}

PHP_MINFO_FUNCTION(openlog)
{
	char buf[64];
	php_info_print_table_start();
	php_info_print_table_row(2, "openlog APM agent", OLG(enabled) ? "enabled" : "disabled");
	php_info_print_table_row(2, "Version", PHP_OPENLOG_VERSION);
#if PHP_VERSION_ID >= 80200
	php_info_print_table_row(2, "Hook layer", "Observer API (userland + internal)");
#elif PHP_VERSION_ID >= 80000
	php_info_print_table_row(2, "Hook layer", "Observer API (userland) + zend_execute_internal");
#else
	php_info_print_table_row(2, "Hook layer", "zend_execute_ex + zend_execute_internal");
#endif
	snprintf(buf, sizeof(buf), ZEND_LONG_FMT, OLG(c_messages));
	php_info_print_table_row(2, "Messages sent (this process)", buf);
	snprintf(buf, sizeof(buf), ZEND_LONG_FMT, OLG(c_dropped_messages));
	php_info_print_table_row(2, "Messages dropped (this process)", buf);
	php_info_print_table_end();
	DISPLAY_INI_ENTRIES();
}

/* ---------------- PHP functions ---------------- */

ZEND_BEGIN_ARG_INFO_EX(arginfo_openlog_void, 0, 0, 0)
ZEND_END_ARG_INFO()

ZEND_BEGIN_ARG_INFO_EX(arginfo_openlog_name, 0, 0, 1)
	ZEND_ARG_INFO(0, name)
ZEND_END_ARG_INFO()

/* {{{ openlog\trace_id(): string — current trace id (32 hex) or "" */
PHP_FUNCTION(openlog_trace_id)
{
	static const char hex[] = "0123456789abcdef";
	char buf[32];
	int i;
	if (zend_parse_parameters_none() == FAILURE) {
		return;
	}
	if (!OLG(active)) {
		RETURN_EMPTY_STRING();
	}
	for (i = 0; i < 16; i++) {
		buf[2 * i] = hex[OLG(trace_id)[i] >> 4];
		buf[2 * i + 1] = hex[OLG(trace_id)[i] & 15];
	}
	RETURN_STRINGL(buf, 32);
}

/* {{{ openlog\span_id(): string — innermost active span (16 hex) or "" */
PHP_FUNCTION(openlog_span_id)
{
	char buf[17];
	if (zend_parse_parameters_none() == FAILURE) {
		return;
	}
	if (!OLG(active)) {
		RETURN_EMPTY_STRING();
	}
	snprintf(buf, sizeof(buf), "%016llx", (unsigned long long) ol_current_span_id());
	RETURN_STRINGL(buf, 16);
}

/* {{{ openlog\traceparent(): string — W3C traceparent for the current span or "" */
PHP_FUNCTION(openlog_traceparent)
{
	char buf[64];
	size_t len;
	if (zend_parse_parameters_none() == FAILURE) {
		return;
	}
	if (!OLG(active)) {
		RETURN_EMPTY_STRING();
	}
	len = ol_format_traceparent(buf, sizeof(buf), ol_current_span_id());
	RETURN_STRINGL(buf, len);
}

/* {{{ openlog\set_transaction_name(string $name): bool */
PHP_FUNCTION(openlog_set_transaction_name)
{
	char *name;
	size_t len;
	if (zend_parse_parameters(ZEND_NUM_ARGS(), "s", &name, &len) == FAILURE) {
		return;
	}
	if (!OLG(active) || len == 0) {
		RETURN_FALSE;
	}
	ol_set_route(name, len, 1000, false);
	RETURN_TRUE;
}

/* {{{ openlog\is_sampled(): bool */
PHP_FUNCTION(openlog_is_sampled)
{
	if (zend_parse_parameters_none() == FAILURE) {
		return;
	}
	RETURN_BOOL(OLG(active) && OLG(recording));
}

/* {{{ openlog\stats(): array — self counters of this process */
PHP_FUNCTION(openlog_stats)
{
	if (zend_parse_parameters_none() == FAILURE) {
		return;
	}
	array_init(return_value);
	add_assoc_long(return_value, "requests", OLG(c_requests));
	add_assoc_long(return_value, "messages", OLG(c_messages));
	add_assoc_long(return_value, "datagrams", OLG(c_parts));
	add_assoc_long(return_value, "send_errors", OLG(c_send_errors));
	add_assoc_long(return_value, "dropped_messages", OLG(c_dropped_messages));
	add_assoc_long(return_value, "dropped_spans", OLG(c_dropped_spans));
	add_assoc_long(return_value, "failopen", OLG(c_failopen));
	add_assoc_long(return_value, "spans", OLG(active) ? OLG(nspans) : 0);
	add_assoc_long(return_value, "segments", OLG(active) ? OLG(seg_kept) : 0);
}

static const zend_function_entry openlog_functions[] = {
	ZEND_NS_NAMED_FE("openlog", trace_id, ZEND_FN(openlog_trace_id), arginfo_openlog_void)
	ZEND_NS_NAMED_FE("openlog", span_id, ZEND_FN(openlog_span_id), arginfo_openlog_void)
	ZEND_NS_NAMED_FE("openlog", traceparent, ZEND_FN(openlog_traceparent), arginfo_openlog_void)
	ZEND_NS_NAMED_FE("openlog", set_transaction_name, ZEND_FN(openlog_set_transaction_name), arginfo_openlog_name)
	ZEND_NS_NAMED_FE("openlog", is_sampled, ZEND_FN(openlog_is_sampled), arginfo_openlog_void)
	ZEND_NS_NAMED_FE("openlog", stats, ZEND_FN(openlog_stats), arginfo_openlog_void)
	PHP_FE_END
};

static const zend_module_dep openlog_deps[] = {
	ZEND_MOD_OPTIONAL("standard")
	ZEND_MOD_OPTIONAL("openssl")
	ZEND_MOD_OPTIONAL("pdo")
	ZEND_MOD_OPTIONAL("curl")
	ZEND_MOD_OPTIONAL("mysqli")
	ZEND_MOD_OPTIONAL("pgsql")
	ZEND_MOD_OPTIONAL("redis")
	ZEND_MOD_END
};

zend_module_entry openlog_module_entry = {
	STANDARD_MODULE_HEADER_EX,
	NULL,
	openlog_deps,
	"openlog",
	openlog_functions,
	PHP_MINIT(openlog),
	PHP_MSHUTDOWN(openlog),
	PHP_RINIT(openlog),
	PHP_RSHUTDOWN(openlog),
	PHP_MINFO(openlog),
	PHP_OPENLOG_VERSION,
	PHP_MODULE_GLOBALS(openlog),
	PHP_GINIT(openlog),
	PHP_GSHUTDOWN(openlog),
	NULL,
	STANDARD_MODULE_PROPERTIES_EX
};

#ifdef COMPILE_DL_OPENLOG
# ifdef ZTS
ZEND_TSRMLS_CACHE_DEFINE()
# endif
ZEND_GET_MODULE(openlog)
#endif

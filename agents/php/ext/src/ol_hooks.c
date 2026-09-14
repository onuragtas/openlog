/*
 * Hook layer: one registry, two engine backends.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Registry: lowercase "class::method" / "class::*" / "function" -> ol_hook, built once at MINIT. The result of the
 * lookup for a zend_function is cached in the function's reserved[] slot as a small integer (0 unknown, 1 none,
 * index + 2), so after the first call every lookup is O(1) without hashing. Integers (not pointers) are stored
 * because opcache shares user op_arrays between processes.
 *
 * Backends:
 *   PHP >= 8.2  Observer API for userland and internal functions
 *   PHP 8.0/8.1 Observer API for userland functions; zend_execute_internal for internal functions (the 8.0/8.1
 *               observer does not see internal calls)
 *   PHP 7.x     zend_execute_ex (userland) + zend_execute_internal (internal)
 * openlog.userland_hooks=0 (lean mode): no fcall observer and no zend_execute_ex override at all — the engine's
 * per-call cost of observing (~100 instructions per call on 8.2/8.3, measured) disappears; internal functions are
 * hooked through zend_execute_internal on every version; framework hooks (routes, reported exceptions) and Predis
 * are off.
 * Previous zend_execute_ex / zend_execute_internal / zend_error_cb values are always chained (Xdebug, New Relic,
 * Datadog and other profilers that replace them keep working; order = load order).
 */
#include "ol.h"
#include "zend_extensions.h"
#if PHP_VERSION_ID >= 80000
# include "zend_observer.h"
#endif
#if PHP_VERSION_ID >= 80100
# include "zend_fibers.h"
#endif

static HashTable ol_registry;
static const ol_hook **ol_hook_table;
static uint32_t ol_hook_count;
static int ol_res = -1;
static bool ol_use_reserved;
static bool ol_registry_ready;
static bool ol_userland; /* userland backend installed (fcall observer / zend_execute_ex) */
static bool ol_internal; /* zend_execute_internal backend installed */

#if PHP_VERSION_ID < 80000
static zend_extension ol_resource_ext;
static void (*ol_prev_execute_ex)(zend_execute_data *ex);
static void (*ol_prev_error_cb)(int type, const char *file, const unsigned int line, const char *format, va_list args);
#endif
static void (*ol_prev_execute_internal)(zend_execute_data *ex, zval *rv);

#define OL_FATAL_MASK (E_ERROR | E_CORE_ERROR | E_COMPILE_ERROR | E_PARSE | E_RECOVERABLE_ERROR | E_USER_ERROR)

/* ---------------- registry ---------------- */

static void ol_registry_add(const ol_hook *hooks)
{
	const ol_hook *h;
	for (h = hooks; h->name; h++) {
		zval zv;
		ZVAL_LONG(&zv, (zend_long) ol_hook_count);
		if (zend_hash_str_add(&ol_registry, h->name, strlen(h->name), &zv) != NULL) {
			ol_hook_table[ol_hook_count++] = h;
		}
	}
}

static uint32_t ol_hooks_len(const ol_hook *hooks)
{
	uint32_t n = 0;
	while (hooks[n].name) n++;
	return n;
}

static const ol_hook *ol_hook_resolve(zend_function *fn, uintptr_t *code)
{
	char key[512];
	size_t kl = 0, sl = 0;
	zend_string *name = fn->common.function_name;
	zend_class_entry *scope = fn->common.scope;
	zval *zv;
	const ol_hook *h;

	*code = 1;
	/* closures — also those other tracers (ddtrace) run without ZEND_ACC_CLOSURE — must not match "class::*" */
	if (name == NULL || (fn->common.fn_flags & ZEND_ACC_CLOSURE) || memchr(ZSTR_VAL(name), '{', ZSTR_LEN(name))) {
		return NULL;
	}
	if (scope) {
		sl = ZSTR_LEN(scope->name);
		if (sl + ZSTR_LEN(name) + 3 >= sizeof(key)) {
			return NULL;
		}
		zend_str_tolower_copy(key, ZSTR_VAL(scope->name), sl);
		key[sl] = ':';
		key[sl + 1] = ':';
		kl = sl + 2;
	} else if (ZSTR_LEN(name) >= sizeof(key)) {
		return NULL;
	}
	zend_str_tolower_copy(key + kl, ZSTR_VAL(name), ZSTR_LEN(name));
	kl += ZSTR_LEN(name);
	zv = zend_hash_str_find(&ol_registry, key, kl);
	if (zv == NULL && scope) {
		key[sl + 2] = '*';
		zv = zend_hash_str_find(&ol_registry, key, sl + 3);
	}
	if (zv == NULL) {
		return NULL;
	}
	h = ol_hook_table[Z_LVAL_P(zv)];
	if (h->begin == NULL && h->end == NULL) {
		return NULL; /* explicitly excluded (e.g. Redis::getOption under "redis::*") */
	}
	*code = (uintptr_t) Z_LVAL_P(zv) + 2;
	return h;
}

const ol_hook *ol_hook_lookup(zend_function *fn)
{
	void **slot = NULL;
	uintptr_t code;
	const ol_hook *h;

	if (!ol_registry_ready || (fn->common.fn_flags & ZEND_ACC_CALL_VIA_TRAMPOLINE)) {
		return NULL;
	}
	if (ol_use_reserved) {
		if (fn->type == ZEND_INTERNAL_FUNCTION) {
			slot = &fn->internal_function.reserved[ol_res];
		} else if (fn->type == ZEND_USER_FUNCTION) {
			slot = &fn->op_array.reserved[ol_res];
		}
		if (slot) {
			code = (uintptr_t) *slot;
			if (code == 1) {
				return NULL;
			}
			if (code >= 2 && code - 2 < ol_hook_count) {
				return ol_hook_table[code - 2];
			}
		}
	}
	h = ol_hook_resolve(fn, &code);
	if (slot) {
		*slot = (void *) code;
	}
	return h;
}

static inline bool ol_hook_runs(const ol_hook *h)
{
	if (OLG(in_call)) {
		return false;
	}
	return (h->flags & OL_HF_ALWAYS) || (OLG(active) && (OLG(recording) || (h->flags & (OL_HF_PROPAGATE | OL_HF_ANY))));
}

/* ---------------- errors ---------------- */

static void ol_on_error(int type, const char *file, uint32_t line, const char *msg, size_t len)
{
	ol_node *root;
	if (!OL_REC() || !(type & OL_FATAL_MASK)) {
		return;
	}
	if ((type & (E_USER_ERROR | E_RECOVERABLE_ERROR)) && Z_TYPE(EG(user_error_handler)) != IS_UNDEF) {
		return; /* may be handled by the application's error handler */
	}
	root = ol_node_at(0);
	if (root == NULL) {
		return;
	}
	if ((root->flags & OL_NF_UNCAUGHT) && len >= 9 && strncmp(msg, "Uncaught ", 9) == 0) {
		return; /* already recorded as an exception event */
	}
	ol_record_error(root, type, file, line, msg, len);
}

#if PHP_VERSION_ID >= 80100
static void ol_error_observer(int type, zend_string *file, uint32_t line, zend_string *message)
{
	ol_on_error(type, file ? ZSTR_VAL(file) : NULL, line, ZSTR_VAL(message), ZSTR_LEN(message));
}
#elif PHP_VERSION_ID >= 80000
static void ol_error_observer(int type, const char *file, uint32_t line, zend_string *message)
{
	ol_on_error(type, file, line, ZSTR_VAL(message), ZSTR_LEN(message));
}
#else
static void ol_error_cb(int type, const char *file, const unsigned int line, const char *format, va_list args)
{
	if (OL_REC() && (type & OL_FATAL_MASK)) {
		va_list copy;
		char *msg = NULL;
		size_t len;
		va_copy(copy, args);
		len = vspprintf(&msg, 0, format, copy);
		va_end(copy);
		if (msg) {
			ol_on_error(type, file, line, msg, len);
			efree(msg);
		}
	}
	ol_prev_error_cb(type, file, line, format, args);
}
#endif

/* ---------------- PHP >= 8.0: Observer API ---------------- */

#if PHP_VERSION_ID >= 80000
static void ol_obs_hook_begin(zend_execute_data *ex)
{
	const ol_hook *h = ol_hook_lookup(ex->func);
	if (h && h->begin && ol_hook_runs(h)) {
		h->begin(ex, h);
	}
}

static void ol_obs_hook_end(zend_execute_data *ex, zval *rv)
{
	const ol_hook *h = ol_hook_lookup(ex->func);
	if (h && h->end && ol_hook_runs(h)) {
		h->end(ex, rv, h);
	}
}

static void ol_obs_main_end(zend_execute_data *ex, zval *rv)
{
	if (OL_EXCEPTION() && ex->prev_execute_data == NULL && OL_REC()) {
		ol_uncaught_exception(EG(exception));
	}
}

static void ol_obs_worker_begin(zend_execute_data *ex)
{
	ol_worker_callback_begin(ex);
}

static void ol_obs_worker_end(zend_execute_data *ex, zval *rv)
{
	ol_worker_callback_end(ex);
}

static zend_observer_fcall_handlers ol_obs_init(zend_execute_data *ex)
{
	zend_observer_fcall_handlers h = {NULL, NULL};
	zend_function *fn = ex->func;

	if (fn->common.function_name == NULL) {
		if (ZEND_USER_CODE(fn->type)) {
			h.end = ol_obs_main_end;
		}
		return h;
	}
	if (fn->common.fn_flags & ZEND_ACC_CALL_VIA_TRAMPOLINE) {
		return h;
	}
	/* a Swoole request callback (inst_workers.c): registered before its first call */
	if (OLG(nworker_cbs) && ol_worker_callback(fn)) {
		h.begin = ol_obs_worker_begin;
		h.end = ol_obs_worker_end;
		return h;
	}
	/* only instrumented functions are observed; the transaction tracer samples instead (ol_sampler.c) */
	if (ol_hook_lookup(fn)) {
		h.begin = ol_obs_hook_begin;
		h.end = ol_obs_hook_end;
	}
	return h;
}
#endif

#if PHP_VERSION_ID >= 80100
static void ol_obs_fiber_switch(zend_fiber_context *from, zend_fiber_context *to)
{
	ol_fiber_switch(from, to);
}

static void ol_obs_fiber_destroy(zend_fiber_context *ctx)
{
	ol_fiber_destroy(ctx);
}
#endif

/* ---------------- PHP 7.x: zend_execute_ex ---------------- */

#if PHP_VERSION_ID < 80000
static void ol_execute_ex(zend_execute_data *ex)
{
	zend_function *fn = ex->func;
	const ol_hook *h;
	bool run_end;
	zval *rv;
	zval this_copy;

	if (fn->common.function_name == NULL) {
		bool top = ex->prev_execute_data == NULL;
		ol_prev_execute_ex(ex);
		if (top && OL_EXCEPTION()) {
			ol_uncaught_exception(EG(exception));
		}
		return;
	}
	if (OLG(nworker_cbs) && ol_worker_callback(fn)) {
		ol_worker_callback_begin(ex);
		ol_prev_execute_ex(ex);
		ol_worker_callback_end(ex);
		return;
	}
	h = ol_hook_lookup(fn);
	if (h == NULL) {
		ol_prev_execute_ex(ex);
		return;
	}
	/* the frame is freed when execute_ex returns: keep what the end handlers need */
	rv = ex->return_value;
	ZVAL_UNDEF(&this_copy);
	if (h->begin && ol_hook_runs(h)) {
		h->begin(ex, h);
	}
	run_end = h->end != NULL;
	if (run_end && Z_TYPE(ex->This) == IS_OBJECT) {
		ZVAL_COPY(&this_copy, &ex->This);
	}
	ol_prev_execute_ex(ex);
	if (run_end && ol_hook_runs(h)) {
		OLG(end_ex) = ex;
		ZVAL_COPY_VALUE(&OLG(end_this), &this_copy);
		h->end(ex, rv, h);
		OLG(end_ex) = NULL;
		ZVAL_UNDEF(&OLG(end_this));
	}
	zval_ptr_dtor(&this_copy);
}
#endif

/* zend_interrupt_function: transaction tracer samples (chained) */
static void (*ol_prev_interrupt)(zend_execute_data *ex);

static void ol_interrupt(zend_execute_data *ex)
{
	ol_sampler_interrupt(ex);
	if (ol_prev_interrupt) {
		ol_prev_interrupt(ex);
	}
}

/*
 * Internal functions (PHP < 8.2, or lean mode). Runs for every internal call, so a function already known not to be
 * instrumented (reserved slot = 1) goes straight to the previous handler without touching the request globals.
 */
static void ol_execute_internal(zend_execute_data *ex, zval *rv)
{
	zend_function *fn = ex->func;
	const ol_hook *h;

	if (EXPECTED(ol_use_reserved) && (uintptr_t) fn->internal_function.reserved[ol_res] == 1) {
		goto pass;
	}
	h = ol_hook_lookup(fn);
	if (h == NULL) {
		goto pass;
	}
	if (h->begin && ol_hook_runs(h)) {
		h->begin(ex, h);
	}
	if (ol_prev_execute_internal) {
		ol_prev_execute_internal(ex, rv);
	} else {
		execute_internal(ex, rv);
	}
	if (h->end && ol_hook_runs(h)) {
		h->end(ex, rv, h);
	}
	return;
pass:
	if (ol_prev_execute_internal) {
		ol_prev_execute_internal(ex, rv);
	} else {
		execute_internal(ex, rv);
	}
}

/* ---------------- lifecycle ---------------- */

int ol_hooks_minit(void)
{
	zend_long protect = 0;
	uint32_t total = ol_hooks_len(ol_hooks_frameworks) + ol_hooks_len(ol_hooks_datastores) + ol_hooks_len(ol_hooks_http) +
		ol_hooks_len(ol_hooks_workers);

	ol_hook_table = pemalloc(sizeof(ol_hook *) * (total + 1), 1);
	ol_hook_count = 0;
	zend_hash_init(&ol_registry, total, NULL, NULL, 1);
	ol_registry_add(ol_hooks_frameworks);
	ol_registry_add(ol_hooks_datastores);
	ol_registry_add(ol_hooks_http);
	ol_registry_add(ol_hooks_workers);
	ol_registry_ready = true;

#if PHP_VERSION_ID >= 80000
	ol_res = zend_get_resource_handle("openlog");
#else
	memset(&ol_resource_ext, 0, sizeof(ol_resource_ext));
	ol_resource_ext.name = "openlog";
	ol_res = zend_get_resource_handle(&ol_resource_ext);
#endif
	/* opcache.protect_memory write-protects shared op_arrays: fall back to hashing on every lookup */
	cfg_get_long("opcache.protect_memory", &protect);
	ol_use_reserved = ol_res >= 0 && !protect;

	ol_userland = OLG(userland_hooks);
#if PHP_VERSION_ID >= 80000
	if (ol_userland) {
		zend_observer_fcall_register(ol_obs_init);
	}
	zend_observer_error_register(ol_error_observer);
# if PHP_VERSION_ID >= 80100
	zend_observer_fiber_switch_register(ol_obs_fiber_switch);
	zend_observer_fiber_destroy_register(ol_obs_fiber_destroy);
# endif
#else
	if (ol_userland) {
		ol_prev_execute_ex = zend_execute_ex;
		zend_execute_ex = ol_execute_ex;
	}
	ol_prev_error_cb = zend_error_cb;
	zend_error_cb = ol_error_cb;
#endif
#if PHP_VERSION_ID >= 80200
	ol_internal = !ol_userland; /* the 8.2+ observer also sees internal calls */
#else
	ol_internal = true;
#endif
	if (ol_internal) {
		ol_prev_execute_internal = zend_execute_internal;
		zend_execute_internal = ol_execute_internal;
	}
	ol_prev_interrupt = zend_interrupt_function;
	zend_interrupt_function = ol_interrupt;
	return SUCCESS;
}

const char *ol_hooks_describe(void)
{
#if PHP_VERSION_ID >= 80200
	return ol_userland ? "Observer API (userland + internal)" : "zend_execute_internal (lean: openlog.userland_hooks=0)";
#elif PHP_VERSION_ID >= 80000
	return ol_userland ? "Observer API (userland) + zend_execute_internal" : "zend_execute_internal (lean: openlog.userland_hooks=0)";
#else
	return ol_userland ? "zend_execute_ex + zend_execute_internal" : "zend_execute_internal (lean: openlog.userland_hooks=0)";
#endif
}

void ol_hooks_mshutdown(void)
{
	if (zend_interrupt_function == ol_interrupt) {
		zend_interrupt_function = ol_prev_interrupt;
	}
#if PHP_VERSION_ID < 80000
	if (ol_userland && zend_execute_ex == ol_execute_ex) {
		zend_execute_ex = ol_prev_execute_ex;
	}
	if (zend_error_cb == ol_error_cb) {
		zend_error_cb = ol_prev_error_cb;
	}
#endif
	if (ol_internal && zend_execute_internal == ol_execute_internal) {
		zend_execute_internal = ol_prev_execute_internal;
	}
	if (ol_registry_ready) {
		ol_registry_ready = false;
		zend_hash_destroy(&ol_registry);
		pefree(ol_hook_table, 1);
		ol_hook_table = NULL;
	}
}

void ol_hooks_rinit(void)
{
}

void ol_hooks_rshutdown(void)
{
}

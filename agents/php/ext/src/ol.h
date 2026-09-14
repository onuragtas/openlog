/*
 * openlog PHP agent: internal header shared by all translation units.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Architecture (see ../README.md):
 *   - ol_hooks.c    hook layer: one registry (lowercase "class::method" -> hook) with two backends
 *                   (Observer API on PHP >= 8.0; zend_execute_ex / zend_execute_internal on 7.1-8.1 where needed)
 *   - ol_core.c     request state: bounded arena, node table (spans + function segments), span stack, tracer
 *   - ol_context.c  W3C trace context, sampling, resource
 *   - ol_json.c     message encoding, datagram splitting, non-blocking transport
 *   ol_text.c     PHP-independent text code: JSON writer, UTF-8, SQL sanitizing, paths, traceparent (fuzzed)
 *   - ol_util.c     property reads without magic, internal calls, exceptions, SQL/URL/route helpers
 *   - inst_*.c      instrumentation (frameworks, datastores, HTTP clients)
 */
#ifndef OL_H
#define OL_H

#ifdef HAVE_CONFIG_H
# include "config.h"
#endif

#include "php.h"
#include "php_ini.h"
#include "SAPI.h"
#include "zend_exceptions.h"
#include "zend_interfaces.h"
#include "php_openlog.h"
#include "ol_text.h"

#include <stdbool.h>
#include <stdint.h>
#include <sys/socket.h>

#ifndef GC_ADDREF /* PHP < 7.3 */
# define GC_ADDREF(p) (++GC_REFCOUNT(p))
#endif

#if PHP_VERSION_ID >= 80000
typedef bool ol_bool;
#else
typedef zend_bool ol_bool;
#endif

#define OL_NONE           UINT32_MAX
#define OL_DGRAM_MAX      60000
#define OL_STR_MAX        4096      /* forwarder limit per string value */
#define OL_MAX_SPANS      4096      /* non-segment spans per request */
#define OL_NODE_CHUNK     1024
#define OL_MAX_CHUNKS     64        /* 65536 nodes */
#define OL_ARENA_CHUNK    (64 * 1024)
#define OL_ARENA_BASE_CAP (8 * 1024 * 1024)
#define OL_MAX_ATTRS      64        /* per span, far below the forwarder's 128 */
#define OL_STACK_MAX      2048

/* OTLP span kinds / status */
#define OL_KIND_INTERNAL 1
#define OL_KIND_SERVER   2
#define OL_KIND_CLIENT   3
#define OL_STATUS_UNSET  0
#define OL_STATUS_OK     1
#define OL_STATUS_ERROR  2

/* node flags */
#define OL_NF_SEGMENT   0x01 /* only emitted with the function trace */
#define OL_NF_DEAD      0x02 /* discarded (fast call / limit), skipped at emission */
#define OL_NF_ENDED     0x04
#define OL_NF_ROOT      0x08
#define OL_NF_UNCAUGHT  0x10 /* root carries an uncaught exception event */
#define OL_NF_FATAL     0x20 /* root carries an engine fatal error event */

/* attribute value types */
#define OL_AT_STR  1
#define OL_AT_INT  2
#define OL_AT_DBL  3
#define OL_AT_BOOL 4

typedef struct ol_attr {
	struct ol_attr *next;
	const char *key;
	union {
		struct { const char *p; uint32_t len; } s;
		int64_t i;
		double d;
		bool b;
	} v;
	uint8_t type;
} ol_attr;

typedef struct ol_event {
	struct ol_event *next;
	const char *name;
	uint64_t mono;
	ol_attr *attrs;
} ol_event;

typedef struct ol_node {
	uint64_t id;
	uint64_t start;      /* monotonic ns */
	uint64_t dur;
	const char *name;
	const char *status_msg;
	ol_attr *attrs;
	ol_event *events;
	uint64_t fast_ns;
	uint32_t parent;
	uint32_t seg_parent;  /* innermost sampled function segment when the span started, or OL_NONE */
	uint32_t emit_parent; /* computed at emission */
	uint32_t fast_calls;
	uint16_t nattrs;
	uint8_t kind;
	uint8_t status;
	uint8_t flags;
} ol_node;

#define OL_FT_TRACER 1
#define OL_FT_SPAN   2

typedef struct ol_frame {
	zend_execute_data *ex;
	uint32_t node;     /* node of this frame or OL_NONE */
	uint32_t eff;      /* parent for children (nearest node) */
	uint32_t eff_span; /* nearest non-segment span (log correlation, propagation) */
	uint8_t type;
} ol_frame;

typedef struct ol_stack {
	ol_frame *f;
	uint32_t depth;
	uint32_t cap;
	uint32_t overflow;
} ol_stack;

typedef struct ol_chunk {
	struct ol_chunk *next;
	size_t used;
	size_t size;
	char data[1];
} ol_chunk;

/* one level of the sampled userland call path (transaction tracer) */
#define OL_PATH_MAX 256
typedef struct ol_pathent {
	zend_execute_data *ex;
	zend_function *fn;
	uint32_t seg;      /* index into OLG(segs) or OL_NONE */
} ol_pathent;

/* a sampled function segment; turned into a span only when the function trace is sent */
typedef struct ol_seg {
	uint64_t id;
	uint64_t first;    /* monotonic ns of the first / last sample containing the frame */
	uint64_t last;
	zend_string *fname; /* referenced (released at request end) */
	zend_string *cname;
	zend_string *file;
	uint32_t line;
	uint32_t parent;   /* parent segment index or OL_NONE (root) */
	uint32_t samples;
} ol_seg;

/* one registered hook */
struct ol_hook;
typedef void (*ol_begin_fn)(zend_execute_data *ex, const struct ol_hook *h);
typedef void (*ol_end_fn)(zend_execute_data *ex, zval *rv, const struct ol_hook *h);
typedef struct ol_hook {
	const char *name;  /* lowercase "class::method", "class::*" or "function" */
	ol_begin_fn begin;
	ol_end_fn end;
	int arg;           /* hook specific */
	uint32_t flags;
} ol_hook;

#define OL_HF_PROPAGATE 0x01 /* also active in unsampled requests (header propagation) */
#define OL_HF_ANY       0x02 /* runs even when the request is not recording (bookkeeping, e.g. connect) */
#define OL_HF_ALWAYS    0x04 /* runs even without an active transaction (worker request boundaries) */

#define OL_QUERY_SANITIZED 0
#define OL_QUERY_RAW       1
#define OL_QUERY_OFF       2

#define OL_LOG_OFF     0
#define OL_LOG_ERROR   1
#define OL_LOG_WARNING 2
#define OL_LOG_INFO    3
#define OL_LOG_DEBUG   4

#define OL_SAVED_CTX_MAX 8

/* per-request cache of query analysis (ol_util.c), keyed by the full query text */
#define OL_QCACHE_SIZE 64
typedef struct ol_qcache {
	const char *raw;    /* arena copy of the query text */
	const char *system;
	const char *name;
	const char *op;
	const char *coll;
	const char *text;   /* db.query.text as recorded (sanitized / raw) or NULL */
	uint32_t len;
	uint32_t gen;       /* == OLG(qgen): valid in this request */
	uint16_t oplen;
	uint16_t colllen;
	uint16_t textlen;
	uint8_t mode;
} ol_qcache;

typedef struct ol_str {
	const char *p;
	size_t len;
} ol_str;

/* description of a request a transaction starts from (SAPI request or long-running worker request object) */
typedef struct ol_reqinfo {
	bool web;
	bool https;
	ol_str method;
	ol_str uri;         /* request target: path[?query] or absolute-form */
	ol_str host;        /* host[:port] */
	ol_str remote_addr;
	ol_str user_agent;
	ol_str protocol;    /* "HTTP/1.1" */
	ol_str traceparent;
	ol_str tracestate;
} ol_reqinfo;

/* a concurrent ("light") worker request: recorded as a root span only (inst_workers.c) */
#define OL_WTX_MAX 64
typedef struct ol_wtx {
	zend_ulong key;            /* 0: free slot */
	zend_execute_data *frame;  /* handler frame: identifies the coroutine */
	uint64_t span_id;
	uint64_t remote_parent;
	uint64_t start_mono;
	uint64_t start_unix;
	int64_t port;
	zend_long status;
	double ratio;
	uint8_t trace_id[16];
	uint8_t trace_flags;
	bool sampled;
	bool https;
	char method[17];
	char proto[9];
	char client[65];
	char host[257];
	char ua[257];
	char tracestate[520];
	char path[1024];
} ol_wtx;

ZEND_BEGIN_MODULE_GLOBALS(openlog)
	/* ini */
	ol_bool enabled;
	char *service_name;
	char *service_namespace;
	char *service_version;
	char *environment;
	char *transport;
	double sampling_ratio;
	char *capture_query_text;
	ol_bool tt_enabled;
	zend_long tt_threshold_ms;
	zend_long tt_max_segments;
	zend_long tt_min_segment_ms;
	zend_long tt_max_memory_kb;
	char *log_level;
	ol_bool userland_hooks;

	/* request state */
	uint32_t qgen;
	ol_qcache qcache[OL_QCACHE_SIZE];
	char txn_method[17];
	zend_long txn_status;   /* status set by a worker (0: SAPI response code) */
	ol_bool txn_web;        /* transaction is an HTTP request (web SAPI or worker request) */

	/* long-running workers (inst_workers.c) */
	ol_bool worker_mode;    /* this PHP request is a worker handling many requests */
	ol_bool concurrent;     /* several worker requests in progress: only root spans are recorded */
	ol_bool wprimary;       /* a worker request owns the node table */
	zend_ulong wkey;
	zend_execute_data *wframe;
	uint64_t wstart;
	ol_wtx *wtx;            /* concurrent requests (OL_WTX_MAX, emalloc'ed on first use) */
	uint32_t nwtx;
	const zend_op *worker_cbs[8]; /* opcodes of the registered Swoole request callbacks */
	uint32_t nworker_cbs;
	ol_bool active;     /* instrumentation running for this request (fail-open clears it) */
	ol_bool recording;  /* sampled: spans are recorded */
	ol_bool tracing;    /* function segments are recorded */
	ol_bool tracer_full;
	ol_bool propagate;  /* inject outgoing trace headers */
	ol_bool is_cli;
	ol_bool in_call;    /* the agent itself is calling a PHP function */
	uint8_t trace_id[16];
	uint64_t remote_parent;
	uint64_t local_root_id; /* span id used for propagation when not recording */
	uint8_t trace_flags;
	char tracestate[520];
	double applied_ratio;
	uint64_t req_mono;
	uint64_t req_unix;
	uint64_t sample_interval_ns;
	void *sampler;               /* ol_sampler of this process/thread */
	ol_pathent path[OL_PATH_MAX];
	uint32_t path_depth;
	ol_seg *segs;
	uint32_t nsegs;
	uint32_t segs_cap;
	uint64_t seg_bytes;
	uint64_t seg_bytes_cap;

	ol_node *chunks[OL_MAX_CHUNKS];
	uint32_t nnodes;
	uint32_t nspans;
	uint32_t seg_kept;
	uint32_t dropped;

	ol_stack main_stack;
	ol_stack *stack;
	HashTable *fiber_stacks;

	ol_chunk *arena_first;
	ol_chunk *arena_cur;
	size_t arena_total;

	const char *route;
	int route_prio;
	const char *route_attr; /* value for http.route (NULL: not set) */
	zend_object *last_reported[8];
	uint32_t nreported;

	HashTable *conns;   /* object/resource handle -> ol_conn* */
	HashTable *stmts;   /* statement handle -> query string */
	HashTable *curl;    /* curl handle -> ol_curl* */
	int pg_last_conn_key_set;
	zend_ulong pg_last_conn_key;
	zend_execute_data *wp_tpl_ex;

	struct {
		php_stream_context *ctx;
		zend_execute_data *ex;
		zval orig;
		uint32_t node;
		bool had;
	} saved_ctx[OL_SAVED_CTX_MAX];
	uint32_t nsaved_ctx;
	zend_long http_status; /* status line seen by the http wrapper proxy for the current stream call */

	uint64_t rng;

	/* PHP 7.x end handlers of userland functions run after the frame is gone: This is kept here */
	zend_execute_data *end_ex;
	zval end_this;

	/* process state */
	int fd;
	int fd_pid;
	int addr_state; /* 0 unparsed, 1 ok, -1 invalid */
	struct sockaddr_storage addr;
	socklen_t addrlen;
	int log_level_n;
	int query_mode;
	int retry_budget;
	char container_id[65];
	ol_bool proc_info_done;
	char out[OL_DGRAM_MAX + 1];
	long log_window;
	int log_count;

	/* self counters (process lifetime) */
	zend_long c_requests;
	zend_long c_messages;
	zend_long c_parts;
	zend_long c_send_errors;
	zend_long c_dropped_messages;
	zend_long c_dropped_spans;
	zend_long c_failopen;
	/* pending, reported on the next message's root span */
	zend_long p_send_errors;
	zend_long p_dropped_messages;
ZEND_END_MODULE_GLOBALS(openlog)

ZEND_EXTERN_MODULE_GLOBALS(openlog)
#define OLG(v) ZEND_MODULE_GLOBALS_ACCESSOR(openlog, v)

#if defined(ZTS) && defined(COMPILE_DL_OPENLOG)
ZEND_TSRMLS_CACHE_EXTERN()
#endif

/* ---- ol_core.c ---- */
uint64_t ol_mono_ns(void);
uint64_t ol_unix_ns(void);
uint64_t ol_rand64(void);
void *ol_alloc(size_t n);
const char *ol_strdup(const char *s, size_t len, size_t max);
const char *ol_strdup_n(const char *s, size_t len, size_t max, size_t *out_len);
const char *ol_strdup_lit(const char *s);
void ol_arena_reset(void);
void ol_arena_release(void);
ol_node *ol_node_at(uint32_t idx);
uint32_t ol_node_new(uint8_t kind, uint8_t flags, uint32_t parent);
void ol_stack_reset(ol_stack *st);
ol_frame *ol_stack_top(void);
uint32_t ol_current_parent(void);
uint32_t ol_current_span(void);
uint64_t ol_current_span_id(void);
uint32_t ol_span_begin(zend_execute_data *ex, const char *name, uint8_t kind);
uint32_t ol_span_begin_flags(zend_execute_data *ex, const char *name, uint8_t kind, uint8_t flags);
ol_node *ol_span_end(zend_execute_data *ex);
bool ol_span_is_open(zend_execute_data *ex);
uint32_t ol_span_detached(const char *name, uint8_t kind);
void ol_node_finish(uint32_t idx);
void ol_node_discard(uint32_t idx);
void ol_fiber_stacks_free(void);
void ol_close_open_nodes(void);
void ol_attr_str(ol_node *n, const char *key, const char *s, size_t len);
void ol_attr_cstr(ol_node *n, const char *key, const char *s);
void ol_attr_static(ol_node *n, const char *key, const char *s);
void ol_attr_static_n(ol_node *n, const char *key, const char *s, size_t len);
void ol_attr_int(ol_node *n, const char *key, int64_t v);
void ol_attr_dbl(ol_node *n, const char *key, double v);
void ol_attr_bool(ol_node *n, const char *key, bool v);
ol_attr *ol_attr_find(ol_node *n, const char *key);
ol_event *ol_event_add(ol_node *n, const char *name);
void ol_event_attr_str(ol_event *e, const char *key, const char *s, size_t len);
void ol_set_route(const char *route, size_t len, int prio, bool http_route);
void ol_fail(const char *reason);
void ol_log(int level, const char *fmt, ...) ZEND_ATTRIBUTE_FORMAT(printf, 2, 3);
void ol_fiber_switch(void *from, void *to);
void ol_fiber_destroy(void *ctx);

/* ---- ol_sampler.c ---- */
bool ol_sampler_arm(void);
void ol_sampler_disarm(void);
void ol_sampler_stop(void *sampler);
void ol_sampler_interrupt(zend_execute_data *ex);
void ol_sampler_finish(void);
void ol_segs_release(void);
uint32_t ol_sample_now(zend_execute_data *ex);

/* ---- ol_module.c ---- */
void ol_txn_start(const ol_reqinfo *ri);
void ol_txn_finish(bool emit);

/* ---- ol_context.c ---- */
void ol_request_context(const ol_reqinfo *ri);
size_t ol_reqinfo_sapi(ol_reqinfo *ri, char **owned, size_t max);
bool ol_trace_decision(const ol_str *tp, const ol_str *ts, uint8_t *tid, uint64_t *parent, uint8_t *flags,
	char *tracestate, size_t tscap, double *ratio, bool *parent_ok);
const char *ol_tracestate(void);
size_t ol_req_path(const ol_str *uri, const char **path);
size_t ol_req_host(const ol_str *host, int64_t *port);
size_t ol_format_traceparent(char *buf, size_t cap, uint64_t span_id);
void ol_process_info(void);
char *ol_server_var(const char *name, size_t len); /* emalloc'ed or NULL */

/* ---- ol_json.c ---- */
void ol_emit(void);
void ol_emit_light(const ol_wtx *t);
bool ol_transport_parse(const char *spec);

/* ---- ol_hooks.c ---- */
int ol_hooks_minit(void);
void ol_hooks_mshutdown(void);
void ol_hooks_rinit(void);
void ol_hooks_rshutdown(void);
const ol_hook *ol_hook_lookup(zend_function *fn);
const char *ol_hooks_describe(void);
extern const ol_hook ol_hooks_frameworks[];
extern const ol_hook ol_hooks_datastores[];
extern const ol_hook ol_hooks_http[];
extern const ol_hook ol_hooks_workers[];

/* ---- inst_workers.c ---- */
bool ol_worker_callback(zend_function *fn);
void ol_worker_callback_begin(zend_execute_data *ex);
void ol_worker_callback_end(zend_execute_data *ex);
int ol_worker_context(const ol_wtx **out);
void ol_worker_rshutdown(void);

/* ---- ol_util.c ---- */
zval *ol_arg(zend_execute_data *ex, uint32_t n);
zval *ol_this(zend_execute_data *ex);
zval *ol_prop(zend_object *obj, const char *name, size_t len);
zval *ol_array_get(zval *arr, const char *key, size_t len);
zend_class_entry *ol_class_find(const char *lcname, size_t len);
bool ol_instanceof(zend_object *obj, const char *lcname, size_t len);
bool ol_call_function(const char *name, size_t len, zval *rv, uint32_t argc, zval *argv);
bool ol_call_method0(zend_object *obj, const char *name, size_t len, zval *rv);
void ol_record_exception(ol_node *n, zend_object *ex, bool set_status);
bool ol_exception_seen(zend_object *ex);
void ol_record_error(ol_node *n, int type, const char *file, uint32_t line, const char *msg, size_t msg_len);
void ol_db_query_attrs(ol_node *n, const char *system, const char *sql, size_t len);
void ol_url_attrs(ol_node *n, const char *url, size_t len);
void ol_report_exception(zend_object *e);
void ol_uncaught_exception(zend_object *e);

/* ---- inst_http.c ---- */
void ol_http_rshutdown(void);
void ol_http_txn_end(void);
void ol_http_minit(void);
void ol_http_mshutdown(void);

#define OL_ZSTR_EQ_CI(zs, lit) ((zs) && ZSTR_LEN(zs) == sizeof(lit) - 1 && zend_binary_strcasecmp(ZSTR_VAL(zs), ZSTR_LEN(zs), lit, sizeof(lit) - 1) == 0)
/* EG(exception) when it is a real Throwable (PHP 8 unwinds exit() with an internal non-Throwable object) */
#define OL_EXCEPTION() ((EG(exception) && instanceof_function(EG(exception)->ce, zend_ce_throwable)) ? EG(exception) : NULL)
#define OL_ACTIVE() (OLG(active))
#define OL_REC() (OLG(active) && OLG(recording))

#endif

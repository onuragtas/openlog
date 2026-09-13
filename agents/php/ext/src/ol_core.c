/*
 * Request state: bounded arena, node table, span stack, transaction tracer.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Nodes (spans and function segments) live in fixed-size chunks addressed by index; a node's parent is an index,
 * always smaller than the node's own index. The transaction tracer allocates a segment node when a userland
 * function starts and gives it back when the call turns out to be fast (< min_segment_ms): a fast call has no
 * surviving children, so its node is the last allocated one and is popped in O(1); otherwise it is marked dead and
 * skipped at emission (its children are re-parented to the nearest emitted ancestor).
 */
#include "ol.h"

#include <errno.h>
#include <fcntl.h>
#include <stdarg.h>
#include <time.h>
#include <unistd.h>
#include <sys/syscall.h>

uint64_t ol_mono_ns(void)
{
	struct timespec ts;
	clock_gettime(CLOCK_MONOTONIC, &ts);
	return (uint64_t) ts.tv_sec * 1000000000ULL + (uint64_t) ts.tv_nsec;
}

uint64_t ol_unix_ns(void)
{
	struct timespec ts;
	clock_gettime(CLOCK_REALTIME, &ts);
	return (uint64_t) ts.tv_sec * 1000000000ULL + (uint64_t) ts.tv_nsec;
}

static bool ol_os_random(uint64_t *out)
{
#ifdef SYS_getrandom
	if (syscall(SYS_getrandom, out, sizeof(*out), 0x0001 /* GRND_NONBLOCK */) == (long) sizeof(*out)) {
		return true;
	}
#endif
	int fd = open("/dev/urandom", O_RDONLY | O_CLOEXEC);
	if (fd >= 0) {
		ssize_t n = read(fd, out, sizeof(*out));
		close(fd);
		return n == (ssize_t) sizeof(*out);
	}
	return false;
}

/* xorshift64*, seeded per process (after fork) from the OS. Not cryptographic; ids only need to be unique. */
uint64_t ol_rand64(void)
{
	uint64_t x = OLG(rng);
	if (x == 0) {
		if (!ol_os_random(&x)) {
			x = ol_mono_ns() ^ ((uint64_t) getpid() << 32) ^ (uint64_t) (uintptr_t) &x;
		}
		if (x == 0) {
			x = 0x9E3779B97F4A7C15ULL;
		}
	}
	x ^= x >> 12;
	x ^= x << 25;
	x ^= x >> 27;
	OLG(rng) = x;
	x *= 0x2545F4914F6CDD1DULL;
	return x ? x : 1;
}

/* ---------------- arena ---------------- */

void *ol_alloc(size_t n)
{
	ol_chunk *c = OLG(arena_cur);
	n = (n + 7) & ~(size_t) 7;
	if (c && c->used + n <= c->size) {
		void *p = c->data + c->used;
		c->used += n;
		return p;
	}
	/* next chunk: reuse a retained one or allocate */
	if (c && c->next && c->next->size >= n) {
		c = c->next;
		c->used = 0;
	} else {
		size_t size = n > OL_ARENA_CHUNK ? n : OL_ARENA_CHUNK;
		ol_chunk *nc;
		if (OLG(arena_total) + size > OL_ARENA_BASE_CAP + (size_t) OLG(seg_bytes_cap)) {
			return NULL;
		}
		nc = malloc(sizeof(ol_chunk) + size);
		if (nc == NULL) {
			return NULL;
		}
		nc->size = size;
		nc->used = 0;
		OLG(arena_total) += size;
		if (c) {
			nc->next = c->next;
			c->next = nc;
		} else {
			nc->next = OLG(arena_first);
			OLG(arena_first) = nc;
		}
		c = nc;
	}
	OLG(arena_cur) = c;
	c->used = n;
	return c->data;
}

void ol_arena_reset(void)
{
	OLG(arena_cur) = OLG(arena_first);
	if (OLG(arena_cur)) {
		OLG(arena_cur)->used = 0;
	}
}

/* Keeps the first chunk for the next request and frees the rest. */
void ol_arena_release(void)
{
	ol_chunk *c = OLG(arena_first), *next;
	if (c == NULL) {
		return;
	}
	next = c->next;
	c->next = NULL;
	c->used = 0;
	OLG(arena_total) = c->size;
	while (next) {
		ol_chunk *n = next->next;
		free(next);
		next = n;
	}
	OLG(arena_cur) = c;
}

/* Copies len bytes (at most max, UTF-8 cleaned, NUL terminated) into the arena. */
const char *ol_strdup(const char *s, size_t len, size_t max)
{
	char *d;
	if (s == NULL) {
		return NULL;
	}
	if (max > OL_STR_MAX) {
		max = OL_STR_MAX;
	}
	if (len > max) {
		len = max;
	}
	d = ol_alloc(len + 1);
	if (d == NULL) {
		return NULL;
	}
	len = ol_utf8_clean(d, s, len, max);
	d[len] = '\0';
	return d;
}

const char *ol_strdup_lit(const char *s)
{
	return s ? ol_strdup(s, strlen(s), OL_STR_MAX) : NULL;
}

/* ---------------- nodes ---------------- */

ol_node *ol_node_at(uint32_t idx)
{
	if (idx == OL_NONE || idx >= OLG(nnodes)) {
		return NULL;
	}
	return &OLG(chunks)[idx / OL_NODE_CHUNK][idx % OL_NODE_CHUNK];
}

uint32_t ol_node_new(uint8_t kind, uint8_t flags, uint32_t parent)
{
	uint32_t idx = OLG(nnodes);
	uint32_t ci = idx / OL_NODE_CHUNK;
	ol_node *n;

	if (ci >= OL_MAX_CHUNKS) {
		return OL_NONE;
	}
	if (!(flags & OL_NF_SEGMENT)) {
		if (OLG(nspans) >= OL_MAX_SPANS) {
			return OL_NONE;
		}
	}
	if (OLG(chunks)[ci] == NULL) {
		OLG(chunks)[ci] = malloc(sizeof(ol_node) * OL_NODE_CHUNK);
		if (OLG(chunks)[ci] == NULL) {
			return OL_NONE;
		}
	}
	n = &OLG(chunks)[ci][idx % OL_NODE_CHUNK];
	n->id = ol_rand64();
	n->start = ol_mono_ns();
	n->dur = 0;
	n->name = NULL;
	n->status_msg = NULL;
	n->attrs = NULL;
	n->events = NULL;
	n->fast_ns = 0;
	n->parent = parent;
	n->emit_parent = OL_NONE;
	n->fast_calls = 0;
	n->nattrs = 0;
	n->kind = kind;
	n->status = OL_STATUS_UNSET;
	n->flags = flags;
	OLG(nnodes) = idx + 1;
	if (!(flags & OL_NF_SEGMENT)) {
		OLG(nspans)++;
	}
	return idx;
}

void ol_node_finish(uint32_t idx)
{
	ol_node *n = ol_node_at(idx);
	if (n && !(n->flags & OL_NF_ENDED)) {
		n->dur = ol_mono_ns() - n->start;
		n->flags |= OL_NF_ENDED;
	}
}

/* ---------------- stack ---------------- */

void ol_stack_reset(ol_stack *st)
{
	st->depth = 0;
	st->overflow = 0;
}

static ol_frame *ol_push_frame(void)
{
	ol_stack *st = OLG(stack);
	if (st->depth >= st->cap) {
		uint32_t ncap = st->cap ? st->cap * 2 : 64;
		ol_frame *nf;
		if (ncap > OL_STACK_MAX) {
			st->overflow++;
			return NULL;
		}
		nf = realloc(st->f, sizeof(ol_frame) * ncap);
		if (nf == NULL) {
			st->overflow++;
			return NULL;
		}
		st->f = nf;
		st->cap = ncap;
	}
	return &st->f[st->depth++];
}

ol_frame *ol_stack_top(void)
{
	ol_stack *st = OLG(stack);
	return st->depth ? &st->f[st->depth - 1] : NULL;
}

/* Parent node for a new node: nearest frame node, else the root. */
uint32_t ol_current_parent(void)
{
	ol_frame *t = ol_stack_top();
	if (t) {
		return t->eff;
	}
	return OLG(nnodes) > 0 ? 0 : OL_NONE;
}

uint32_t ol_current_span(void)
{
	ol_frame *t = ol_stack_top();
	if (t) {
		return t->eff_span;
	}
	return OLG(nnodes) > 0 ? 0 : OL_NONE;
}

uint64_t ol_current_span_id(void)
{
	ol_node *n = ol_node_at(ol_current_span());
	if (n) {
		return n->id;
	}
	return OLG(local_root_id);
}

uint32_t ol_span_begin_flags(zend_execute_data *ex, const char *name, uint8_t kind, uint8_t flags)
{
	uint32_t parent, eff_span, idx;
	ol_frame *f;

	if (!OL_REC()) {
		return OL_NONE;
	}
	parent = ol_current_parent();
	eff_span = ol_current_span();
	idx = ol_node_new(kind, flags, parent);
	if (idx == OL_NONE) {
		OLG(dropped)++;
	} else {
		ol_node_at(idx)->name = name;
	}
	f = ol_push_frame();
	if (f == NULL) {
		/* stack exhausted: keep the node as a detached span that ends at request end */
		return idx;
	}
	f->ex = ex;
	f->node = idx;
	f->eff = idx != OL_NONE ? idx : parent;
	f->eff_span = (idx != OL_NONE && !(flags & OL_NF_SEGMENT)) ? idx : eff_span;
	f->type = OL_FT_SPAN;
	return idx;
}

uint32_t ol_span_begin(zend_execute_data *ex, const char *name, uint8_t kind)
{
	return ol_span_begin_flags(ex, name, kind, 0);
}

bool ol_span_is_open(zend_execute_data *ex)
{
	ol_frame *t = ol_stack_top();
	return t && t->ex == ex && t->type == OL_FT_SPAN;
}

/* Ends the span frame opened for ex. Frames left open above it (a missed end handler) are closed too. */
ol_node *ol_span_end(zend_execute_data *ex)
{
	ol_stack *st = OLG(stack);
	uint32_t i, stop;
	uint64_t now;

	if (!OLG(active) || st->depth == 0) {
		return NULL;
	}
	if (st->overflow) {
		/* frames beyond the stack limit were never pushed */
		ol_frame *t = &st->f[st->depth - 1];
		if (t->ex != ex) {
			st->overflow--;
			return NULL;
		}
	}
	stop = st->depth > 16 ? st->depth - 16 : 0;
	for (i = st->depth; i > stop; i--) {
		ol_frame *f = &st->f[i - 1];
		if (f->ex == ex && f->type == OL_FT_SPAN) {
			ol_node *n = NULL;
			now = ol_mono_ns();
			while (st->depth >= i) {
				ol_frame *g = &st->f[--st->depth];
				ol_node *gn = ol_node_at(g->node);
				if (gn && !(gn->flags & OL_NF_ENDED)) {
					gn->dur = now - gn->start;
					gn->flags |= OL_NF_ENDED;
				}
				if (st->depth == i - 1) {
					n = gn;
				}
			}
			return n;
		}
		if (f->ex != ex) {
			break; /* the top frame belongs to someone else: this end had no begin */
		}
	}
	return NULL;
}

uint32_t ol_span_detached(const char *name, uint8_t kind)
{
	uint32_t idx;
	if (!OL_REC()) {
		return OL_NONE;
	}
	idx = ol_node_new(kind, 0, ol_current_parent());
	if (idx == OL_NONE) {
		OLG(dropped)++;
		return OL_NONE;
	}
	ol_node_at(idx)->name = name;
	return idx;
}

/* ---------------- transaction tracer ---------------- */

void ol_tracer_begin(zend_execute_data *ex)
{
	ol_frame *f;
	ol_frame *t;
	uint32_t parent, idx = OL_NONE;

	t = ol_stack_top();
	parent = t ? t->eff : (OLG(nnodes) ? 0 : OL_NONE);
	if (!OLG(tracer_full)) {
		if (OLG(seg_bytes) + sizeof(ol_node) > OLG(seg_bytes_cap)) {
			OLG(tracer_full) = true;
		} else {
			idx = ol_node_new(OL_KIND_INTERNAL, OL_NF_SEGMENT, parent);
			if (idx != OL_NONE) {
				OLG(seg_bytes) += sizeof(ol_node);
			} else {
				OLG(tracer_full) = true;
			}
		}
	}
	f = ol_push_frame();
	if (f == NULL) {
		if (idx != OL_NONE) {
			ol_node_at(idx)->flags |= OL_NF_DEAD;
		}
		return;
	}
	f->ex = ex;
	f->node = idx;
	f->eff = idx != OL_NONE ? idx : parent;
	f->eff_span = t ? t->eff_span : (OLG(nnodes) ? 0 : OL_NONE);
	f->type = OL_FT_TRACER;
}

static void ol_segment_describe(ol_node *n, zend_execute_data *ex)
{
	zend_function *fn = ex->func;
	zend_string *fname = fn->common.function_name;
	zend_class_entry *scope = fn->common.scope;
	size_t before = OLG(arena_total);
	char buf[512];
	int len;

	if (scope && fname) {
		len = snprintf(buf, sizeof(buf), "%s::%s", ZSTR_VAL(scope->name), ZSTR_VAL(fname));
	} else {
		len = snprintf(buf, sizeof(buf), "%s", fname ? ZSTR_VAL(fname) : "{main}");
	}
	if (len < 0) {
		len = 0;
	} else if ((size_t) len >= sizeof(buf)) {
		len = sizeof(buf) - 1;
	}
	n->name = ol_strdup(buf, (size_t) len, sizeof(buf));
	if (fname) {
		ol_attr_str(n, "code.function.name", ZSTR_VAL(fname), ZSTR_LEN(fname));
	}
	if (scope) {
		ol_attr_str(n, "code.namespace", ZSTR_VAL(scope->name), ZSTR_LEN(scope->name));
	}
	if (fn->type == ZEND_USER_FUNCTION && fn->op_array.filename) {
		ol_attr_str(n, "code.file.path", ZSTR_VAL(fn->op_array.filename), ZSTR_LEN(fn->op_array.filename));
		ol_attr_int(n, "code.line.number", fn->op_array.line_start);
	}
	ol_attr_static(n, "openlog.php.segment", "function");
	/* approximate accounting of the strings and attributes of this segment */
	OLG(seg_bytes) += 384 + (OLG(arena_total) - before);
}

void ol_tracer_end(zend_execute_data *ex)
{
	ol_stack *st = OLG(stack);
	ol_frame *f;
	ol_node *n, *p;
	uint64_t dur;

	if (st->depth == 0) {
		return;
	}
	f = &st->f[st->depth - 1];
	if (f->ex != ex || f->type != OL_FT_TRACER) {
		if (st->overflow) {
			st->overflow--;
		}
		return;
	}
	st->depth--;
	if (f->node == OL_NONE) {
		/* not recorded (limit): count as dropped only if it would have been kept */
		return;
	}
	n = ol_node_at(f->node);
	if (n == NULL) {
		return;
	}
	dur = ol_mono_ns() - n->start;
	n->dur = dur;
	n->flags |= OL_NF_ENDED;
	if (dur < OLG(min_segment_ns)) {
		p = ol_node_at(n->parent);
		if (p) {
			p->fast_calls += 1 + n->fast_calls;
			p->fast_ns += dur;
		}
		if (f->node == OLG(nnodes) - 1) {
			OLG(nnodes)--; /* no surviving children: give the node back */
		} else {
			n->flags |= OL_NF_DEAD;
		}
		if (OLG(seg_bytes) >= sizeof(ol_node)) {
			OLG(seg_bytes) -= sizeof(ol_node);
		}
		return;
	}
	if (OLG(seg_kept) >= (uint32_t) OLG(tt_max_segments) || OLG(seg_bytes) > OLG(seg_bytes_cap)) {
		n->flags |= OL_NF_DEAD;
		OLG(dropped)++;
		OLG(tracer_full) = true;
		return;
	}
	OLG(seg_kept)++;
	ol_segment_describe(n, ex);
}

/* ---------------- attributes ---------------- */

static ol_attr *ol_attr_new(ol_node *n, const char *key, uint8_t type)
{
	ol_attr *a;
	if (n == NULL || n->nattrs >= OL_MAX_ATTRS) {
		return NULL;
	}
	a = ol_alloc(sizeof(ol_attr));
	if (a == NULL) {
		return NULL;
	}
	a->key = key;
	a->type = type;
	a->next = n->attrs;
	n->attrs = a;
	n->nattrs++;
	return a;
}

ol_attr *ol_attr_find(ol_node *n, const char *key)
{
	ol_attr *a;
	for (a = n ? n->attrs : NULL; a; a = a->next) {
		if (strcmp(a->key, key) == 0) {
			return a;
		}
	}
	return NULL;
}

void ol_attr_str(ol_node *n, const char *key, const char *s, size_t len)
{
	const char *copy;
	ol_attr *a;
	if (n == NULL || s == NULL) {
		return;
	}
	copy = ol_strdup(s, len, OL_STR_MAX);
	if (copy == NULL) {
		return;
	}
	a = ol_attr_new(n, key, OL_AT_STR);
	if (a) {
		a->v.s.p = copy;
		a->v.s.len = (uint32_t) strlen(copy);
	}
}

void ol_attr_cstr(ol_node *n, const char *key, const char *s)
{
	if (s) {
		ol_attr_str(n, key, s, strlen(s));
	}
}

/* s must outlive the request (literal or arena string) and be valid UTF-8 <= 4 KiB. */
void ol_attr_static(ol_node *n, const char *key, const char *s)
{
	ol_attr *a;
	if (s == NULL) {
		return;
	}
	a = ol_attr_new(n, key, OL_AT_STR);
	if (a) {
		a->v.s.p = s;
		a->v.s.len = (uint32_t) strlen(s);
	}
}

void ol_attr_int(ol_node *n, const char *key, int64_t v)
{
	ol_attr *a = ol_attr_new(n, key, OL_AT_INT);
	if (a) {
		a->v.i = v;
	}
}

void ol_attr_dbl(ol_node *n, const char *key, double v)
{
	ol_attr *a = ol_attr_new(n, key, OL_AT_DBL);
	if (a) {
		a->v.d = v;
	}
}

void ol_attr_bool(ol_node *n, const char *key, bool v)
{
	ol_attr *a = ol_attr_new(n, key, OL_AT_BOOL);
	if (a) {
		a->v.b = v;
	}
}

ol_event *ol_event_add(ol_node *n, const char *name)
{
	ol_event *e, **tail;
	int count = 0;
	if (n == NULL) {
		return NULL;
	}
	for (tail = &n->events; *tail; tail = &(*tail)->next) {
		if (++count >= 16) {
			return NULL;
		}
	}
	e = ol_alloc(sizeof(ol_event));
	if (e == NULL) {
		return NULL;
	}
	e->next = NULL;
	e->name = name;
	e->mono = ol_mono_ns();
	e->attrs = NULL;
	*tail = e; /* keep order: the last exception event is the span's error */
	return e;
}

void ol_event_attr_str(ol_event *e, const char *key, const char *s, size_t len)
{
	ol_attr *a;
	const char *copy;
	if (e == NULL || s == NULL) {
		return;
	}
	copy = ol_strdup(s, len, OL_STR_MAX);
	a = copy ? ol_alloc(sizeof(ol_attr)) : NULL;
	if (a == NULL) {
		return;
	}
	a->key = key;
	a->type = OL_AT_STR;
	a->v.s.p = copy;
	a->v.s.len = (uint32_t) strlen(copy);
	a->next = e->attrs;
	e->attrs = a;
}

/* Transaction route; higher priority wins, equal priority: the later call wins. */
void ol_set_route(const char *route, size_t len, int prio, bool http_route)
{
	const char *copy;
	if (!OLG(active) || route == NULL || len == 0 || prio < OLG(route_prio)) {
		return;
	}
	copy = ol_strdup(route, len, 1024);
	if (copy == NULL) {
		return;
	}
	OLG(route) = copy;
	OLG(route_prio) = prio;
	OLG(route_attr) = http_route ? copy : NULL;
}

/* ---------------- fail-open + logging ---------------- */

void ol_log(int level, const char *fmt, ...)
{
	char msg[512], line[600];
	va_list ap;
	long now;

	if (level > OLG(log_level_n)) {
		return;
	}
	now = (long) time(NULL);
	if (now / 60 != OLG(log_window)) {
		OLG(log_window) = now / 60;
		OLG(log_count) = 0;
	}
	if (OLG(log_count)++ >= 10) { /* at most 10 lines per minute per process */
		return;
	}
	va_start(ap, fmt);
	vsnprintf(msg, sizeof(msg), fmt, ap);
	va_end(ap);
	snprintf(line, sizeof(line), "openlog: %s", msg);
	php_log_err(line);
}

/* Any internal inconsistency: stop instrumenting this request, send nothing, log (rate limited). */
void ol_fail(const char *reason)
{
	if (!OLG(active)) {
		return;
	}
	OLG(active) = false;
	OLG(recording) = false;
	OLG(tracing) = false;
	OLG(c_failopen)++;
	OLG(c_dropped_messages)++;
	OLG(p_dropped_messages)++;
	ol_log(OL_LOG_WARNING, "instrumentation disabled for this request: %s", reason);
}

/* ---------------- fibers (PHP >= 8.1) ---------------- */

void ol_fiber_switch(void *from, void *to)
{
	ol_stack *st;
	zval *zv;
	if (!OLG(active)) {
		return;
	}
	if (OLG(fiber_stacks) == NULL) {
		ALLOC_HASHTABLE(OLG(fiber_stacks));
		zend_hash_init(OLG(fiber_stacks), 8, NULL, NULL, 0);
	}
	/* EG(main_fiber_context) is the main stack; every other context gets its own frame stack */
	zv = zend_hash_index_find(OLG(fiber_stacks), (zend_ulong) (uintptr_t) to);
	if (zv) {
		st = Z_PTR_P(zv);
	} else if (zend_hash_num_elements(OLG(fiber_stacks)) == 0 && OLG(stack) == &OLG(main_stack)) {
		/* first switch: remember the current (main) context */
		zend_hash_index_add_ptr(OLG(fiber_stacks), (zend_ulong) (uintptr_t) from, &OLG(main_stack));
		st = ecalloc(1, sizeof(ol_stack));
		zend_hash_index_add_ptr(OLG(fiber_stacks), (zend_ulong) (uintptr_t) to, st);
	} else {
		st = ecalloc(1, sizeof(ol_stack));
		zend_hash_index_add_ptr(OLG(fiber_stacks), (zend_ulong) (uintptr_t) to, st);
	}
	if (st != &OLG(main_stack) && st->depth == 0 && st->f == NULL) {
		st->f = ecalloc(32, sizeof(ol_frame));
		st->cap = 32;
	}
	OLG(stack) = st;
}

void ol_fiber_destroy(void *ctx)
{
	zval *zv;
	if (OLG(fiber_stacks) == NULL) {
		return;
	}
	zv = zend_hash_index_find(OLG(fiber_stacks), (zend_ulong) (uintptr_t) ctx);
	if (zv && Z_PTR_P(zv) != &OLG(main_stack)) {
		ol_stack *st = Z_PTR_P(zv);
		if (OLG(stack) == st) {
			OLG(stack) = &OLG(main_stack);
		}
		/* frame arrays of fibers are emalloc'ed (grown with realloc only for the main stack) */
		zend_hash_index_del(OLG(fiber_stacks), (zend_ulong) (uintptr_t) ctx);
	}
}

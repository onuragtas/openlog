/*
 * Request state: bounded arena, node table, span stack.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Nodes (spans and function segments) live in fixed-size chunks addressed by index; a node's parent is an index,
 * always smaller than the node's own index. Nodes that are not sent (discarded spans, segments of a fast
 * transaction) are skipped at emission and their children re-parented to the nearest emitted ancestor. Function
 * segments come from the sampler (ol_sampler.c); every span start and end takes a sample so that the span's parent
 * is the innermost function segment.
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

/* Copies len bytes (at most max, UTF-8 cleaned, NUL terminated) into the arena; *out_len (optional) = copied length. */
const char *ol_strdup_n(const char *s, size_t len, size_t max, size_t *out_len)
{
	char *d;
	if (out_len) {
		*out_len = 0;
	}
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
	if (out_len) {
		*out_len = len;
	}
	return d;
}

const char *ol_strdup(const char *s, size_t len, size_t max)
{
	return ol_strdup_n(s, len, max, NULL);
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
	n->seg_parent = OL_NONE;
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
	uint32_t parent, eff_span, idx, seg = OL_NONE;
	ol_frame *f;

	if (!OL_REC() || OLG(concurrent)) {
		return OL_NONE;
	}
	parent = ol_current_parent();
	eff_span = ol_current_span();
	if (OLG(tracing)) {
		seg = ol_sample_now(EG(current_execute_data));
	}
	idx = ol_node_new(kind, flags, parent);
	if (idx == OL_NONE) {
		OLG(dropped)++;
	} else {
		ol_node *n = ol_node_at(idx);
		n->name = name;
		/* inside another instrumented span that span stays the parent; else the innermost function segment */
		if (parent == 0) {
			n->seg_parent = seg;
		}
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
	if (OLG(tracing)) {
		/* a call that blocked for at least one interval: extend the functions that waited in it */
		ol_node *top = ol_node_at(st->f[st->depth - 1].node);
		if (top && ol_mono_ns() - top->start >= OLG(sample_interval_ns)) {
			ol_sample_now(EG(current_execute_data));
		}
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
	uint32_t idx, parent, seg = OL_NONE;
	if (!OL_REC() || OLG(concurrent)) {
		return OL_NONE;
	}
	parent = ol_current_parent();
	if (OLG(tracing)) {
		seg = ol_sample_now(EG(current_execute_data));
	}
	idx = ol_node_new(kind, 0, parent);
	if (idx == OL_NONE) {
		OLG(dropped)++;
		return OL_NONE;
	}
	ol_node_at(idx)->name = name;
	if (parent == 0) {
		ol_node_at(idx)->seg_parent = seg;
	}
	return idx;
}

/* Gives back a span that turned out not to be needed (e.g. PDO::prepare without error). */
void ol_node_discard(uint32_t idx)
{
	ol_node *n = ol_node_at(idx);
	if (n == NULL) {
		return;
	}
	if (idx == OLG(nnodes) - 1 && idx > 0) {
		OLG(nnodes)--;
		if (!(n->flags & OL_NF_SEGMENT) && OLG(nspans) > 0) {
			OLG(nspans)--;
		}
	} else {
		n->flags |= OL_NF_DEAD;
	}
}

/* Request end: every node still open ends now; open function segments are discarded. */
void ol_close_open_nodes(void)
{
	uint64_t now = ol_mono_ns();
	uint32_t i;
	for (i = 0; i < OLG(nnodes); i++) {
		ol_node *n = ol_node_at(i);
		if (n->flags & OL_NF_ENDED) {
			continue;
		}
		if ((n->flags & OL_NF_SEGMENT) && n->name == NULL) {
			n->flags |= OL_NF_DEAD;
		}
		n->dur = now - n->start;
		n->flags |= OL_NF_ENDED;
	}
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
	size_t cl;
	if (n == NULL || s == NULL || n->nattrs >= OL_MAX_ATTRS) {
		return;
	}
	copy = ol_strdup_n(s, len, OL_STR_MAX, &cl);
	if (copy == NULL) {
		return;
	}
	a = ol_attr_new(n, key, OL_AT_STR);
	if (a) {
		a->v.s.p = copy;
		a->v.s.len = (uint32_t) cl;
	}
}

/* s: arena string (valid UTF-8, <= 4 KiB, outlives the request) of length len. */
void ol_attr_static_n(ol_node *n, const char *key, const char *s, size_t len)
{
	ol_attr *a;
	if (s == NULL) {
		return;
	}
	a = ol_attr_new(n, key, OL_AT_STR);
	if (a) {
		a->v.s.p = s;
		a->v.s.len = (uint32_t) len;
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
	size_t cl;
	if (e == NULL || s == NULL) {
		return;
	}
	copy = ol_strdup_n(s, len, OL_STR_MAX, &cl);
	a = copy ? ol_alloc(sizeof(ol_attr)) : NULL;
	if (a == NULL) {
		return;
	}
	a->key = key;
	a->type = OL_AT_STR;
	a->v.s.p = copy;
	a->v.s.len = (uint32_t) cl;
	a->next = e->attrs;
	e->attrs = a;
}

/* Transaction route; higher priority wins, equal priority: the later call wins. */
void ol_set_route(const char *route, size_t len, int prio, bool http_route)
{
	const char *copy;
	if (!OLG(active) || OLG(concurrent) || route == NULL || len == 0 || prio < OLG(route_prio)) {
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
		st = calloc(1, sizeof(ol_stack));
		if (st == NULL) {
			return;
		}
		zend_hash_index_add_ptr(OLG(fiber_stacks), (zend_ulong) (uintptr_t) to, st);
	} else {
		st = calloc(1, sizeof(ol_stack));
		if (st == NULL) {
			return;
		}
		zend_hash_index_add_ptr(OLG(fiber_stacks), (zend_ulong) (uintptr_t) to, st);
	}
	OLG(stack) = st;
}

/* Frees the stacks of fibers (malloc'ed, frames grown with realloc like the main stack). */
void ol_fiber_stacks_free(void)
{
	ol_stack *st;
	if (OLG(fiber_stacks) == NULL) {
		return;
	}
	ZEND_HASH_FOREACH_PTR(OLG(fiber_stacks), st) {
		if (st != &OLG(main_stack)) {
			free(st->f);
			free(st);
		}
	} ZEND_HASH_FOREACH_END();
	zend_hash_destroy(OLG(fiber_stacks));
	FREE_HASHTABLE(OLG(fiber_stacks));
	OLG(fiber_stacks) = NULL;
	OLG(stack) = &OLG(main_stack);
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
		zend_hash_index_del(OLG(fiber_stacks), (zend_ulong) (uintptr_t) ctx);
		free(st->f);
		free(st);
	}
}

/*
 * Transaction tracer by wall-clock stack sampling.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Observing every userland call costs ~60 ns per call (measured on PHP 8.3/8.4), i.e. > 1 ms on a framework request
 * with ~20k calls, far above the tracer budget. Instead, a sampler thread (one per PHP process/thread, idle between
 * requests) sets EG(vm_interrupt) every `openlog.transaction_tracer.min_segment_ms`; the engine calls
 * zend_interrupt_function at the next safe point, where the userland call stack is walked and merged into the
 * segment list: frames present in consecutive samples extend their segment, new frames open segments. Every
 * instrumented span (DB, HTTP, sleep, …) also takes a sample at its start (and at its end when it blocked for at
 * least one interval), so time spent blocked in those calls is attributed to the right functions and spans get the
 * innermost function segment as parent.
 *
 * Segments are compact records (id, first/last sample time, referenced name strings); spans and JSON are produced
 * only when the function trace is sent (slow or failed transaction). No per-call cost; a sample costs a stack walk.
 *
 * Limits: calls shorter than the interval appear only when a sample hits them; consecutive calls of the same function
 * from the same frame address without a sample in between merge into one segment (`openlog.php.samples`).
 */
#include "ol.h"

#include <pthread.h>
#include <signal.h>
#include <time.h>
#include <unistd.h>

#if PHP_VERSION_ID >= 80200
# include "zend_atomic.h"
# define OL_SET_INTERRUPT(p) zend_atomic_bool_store_ex((zend_atomic_bool *) (p), true)
#else
# define OL_SET_INTERRUPT(p) (*(zend_bool *) (p) = 1)
#endif

typedef struct ol_sampler {
	pthread_t th;
	pthread_mutex_t mu;
	pthread_cond_t cv;
	int quit;
	volatile int armed;
	volatile int pending;
	uint64_t interval_ns;
	void *vm_interrupt;
	int pid;
} ol_sampler;

static void *ol_sampler_main(void *arg)
{
	ol_sampler *s = arg;
	pthread_mutex_lock(&s->mu);
	while (!s->quit) {
		uint64_t iv;
		void *vi;
		struct timespec ts;
		while (!s->armed && !s->quit) {
			pthread_cond_wait(&s->cv, &s->mu);
		}
		if (s->quit) {
			break;
		}
		iv = s->interval_ns;
		vi = s->vm_interrupt;
		pthread_mutex_unlock(&s->mu);
		ts.tv_sec = (time_t) (iv / 1000000000ULL);
		ts.tv_nsec = (long) (iv % 1000000000ULL);
		nanosleep(&ts, NULL);
		if (s->armed && vi) {
			s->pending = 1;
			OL_SET_INTERRUPT(vi);
		}
		pthread_mutex_lock(&s->mu);
	}
	pthread_mutex_unlock(&s->mu);
	return NULL;
}

/* Starts sampling for this request. Returns false when no sampler thread can be started (tracer then stays off). */
bool ol_sampler_arm(void)
{
	ol_sampler *s = OLG(sampler);
	int pid = (int) getpid();

	if (s && s->pid != pid) {
		s = NULL; /* forked: the thread does not exist in this process (the old struct is left alone) */
	}
	if (s == NULL) {
		sigset_t all, old;
		s = calloc(1, sizeof(*s));
		if (s == NULL) {
			return false;
		}
		pthread_mutex_init(&s->mu, NULL);
		pthread_cond_init(&s->cv, NULL);
		s->pid = pid;
		/* the sampler thread must never receive the process's signals (FPM, timeouts) */
		sigfillset(&all);
		pthread_sigmask(SIG_BLOCK, &all, &old);
		if (pthread_create(&s->th, NULL, ol_sampler_main, s) != 0) {
			pthread_sigmask(SIG_SETMASK, &old, NULL);
			free(s);
			ol_log(OL_LOG_WARNING, "transaction tracer disabled: cannot start the sampler thread");
			return false;
		}
		pthread_sigmask(SIG_SETMASK, &old, NULL);
		OLG(sampler) = s;
	}
	pthread_mutex_lock(&s->mu);
	s->interval_ns = OLG(sample_interval_ns);
	s->vm_interrupt = &EG(vm_interrupt);
	s->pending = 0;
	s->armed = 1;
	pthread_cond_signal(&s->cv);
	pthread_mutex_unlock(&s->mu);
	return true;
}

void ol_sampler_disarm(void)
{
	ol_sampler *s = OLG(sampler);
	if (s && s->pid == (int) getpid()) {
		s->armed = 0;
		s->pending = 0;
	}
}

/* Process/thread end: stop and join the sampler thread. */
void ol_sampler_stop(void *ptr)
{
	ol_sampler *s = ptr;
	if (s == NULL || s->pid != (int) getpid()) {
		return;
	}
	pthread_mutex_lock(&s->mu);
	s->quit = 1;
	s->armed = 0;
	pthread_cond_signal(&s->cv);
	pthread_mutex_unlock(&s->mu);
	pthread_join(s->th, NULL);
	pthread_mutex_destroy(&s->mu);
	pthread_cond_destroy(&s->cv);
	free(s);
}

/* ---------------- samples → segments ---------------- */

static inline bool ol_sampled_frame(zend_execute_data *e)
{
	zend_function *f = e->func;
	return f && f->type == ZEND_USER_FUNCTION && f->common.function_name &&
		!(f->common.fn_flags & ZEND_ACC_CALL_VIA_TRAMPOLINE);
}

static uint32_t ol_segment_open(zend_function *fn, uint32_t parent, uint64_t now)
{
	ol_seg *s;
	if (OLG(tracer_full) || OLG(seg_kept) >= (uint32_t) OLG(tt_max_segments) ||
			OLG(seg_bytes) + sizeof(ol_seg) + 256 > OLG(seg_bytes_cap)) {
		OLG(tracer_full) = true;
		OLG(dropped)++;
		return OL_NONE;
	}
	if (OLG(nsegs) >= OLG(segs_cap)) {
		uint32_t ncap = OLG(segs_cap) ? OLG(segs_cap) * 2 : 256;
		ol_seg *n = realloc(OLG(segs), sizeof(ol_seg) * ncap);
		if (n == NULL) {
			OLG(tracer_full) = true;
			OLG(dropped)++;
			return OL_NONE;
		}
		OLG(segs) = n;
		OLG(segs_cap) = ncap;
	}
	s = &OLG(segs)[OLG(nsegs)];
	s->id = ol_rand64();
	s->first = now;
	s->last = now;
	s->samples = 1;
	s->parent = parent;
	s->fname = zend_string_copy(fn->common.function_name);
	s->cname = fn->common.scope ? zend_string_copy(fn->common.scope->name) : NULL;
	s->file = fn->op_array.filename ? zend_string_copy(fn->op_array.filename) : NULL;
	s->line = fn->op_array.line_start;
	OLG(seg_kept)++;
	/* memory of the segment and of its span when it is sent (strings, attributes, JSON) */
	OLG(seg_bytes) += sizeof(ol_seg) + 256;
	return OLG(nsegs)++;
}

/*
 * Takes a sample of the userland stack starting at ex and merges it into the open segment path. Returns the index of
 * the innermost open segment (OL_NONE when there is none).
 */
uint32_t ol_sample_now(zend_execute_data *ex)
{
	zend_execute_data *chain[OL_PATH_MAX];
	zend_execute_data *e;
	uint32_t total = 0, skip, n = 0, i, k;
	uint64_t now;

	if (!OLG(tracing)) {
		return OL_NONE;
	}
	now = ol_mono_ns();
	for (e = ex; e; e = e->prev_execute_data) {
		if (ol_sampled_frame(e) && ++total > 100000) {
			break;
		}
	}
	/* keep the outermost OL_PATH_MAX frames: the root of the path must stay stable */
	skip = total > OL_PATH_MAX ? total - OL_PATH_MAX : 0;
	for (e = ex; e && n < OL_PATH_MAX; e = e->prev_execute_data) {
		if (!ol_sampled_frame(e)) {
			continue;
		}
		if (skip) {
			skip--;
			continue;
		}
		chain[n++] = e;
	}
	for (i = 0; i < OLG(path_depth) && i < n; i++) {
		ol_pathent *p = &OLG(path)[i];
		e = chain[n - 1 - i];
		if (p->ex != e || p->fn != e->func) {
			break;
		}
		if (p->seg != OL_NONE) {
			OLG(segs)[p->seg].last = now;
			OLG(segs)[p->seg].samples++;
		}
	}
	OLG(path_depth) = i;
	for (; i < n; i++) {
		ol_pathent *p = &OLG(path)[i];
		e = chain[n - 1 - i];
		p->ex = e;
		p->fn = e->func;
		p->seg = ol_segment_open(e->func, i ? OLG(path)[i - 1].seg : OL_NONE, now);
		OLG(path_depth) = i + 1;
	}
	for (k = OLG(path_depth); k > 0; k--) {
		if (OLG(path)[k - 1].seg != OL_NONE) {
			return OLG(path)[k - 1].seg;
		}
	}
	return OL_NONE;
}

/* zend_interrupt_function: a sample is due */
void ol_sampler_interrupt(zend_execute_data *ex)
{
	ol_sampler *s = OLG(sampler);
	if (s && s->pending) {
		s->pending = 0;
		if (OLG(active) && OLG(tracing)) {
			ol_sample_now(ex);
		}
	}
}

void ol_sampler_finish(void)
{
	OLG(path_depth) = 0;
}

void ol_segs_release(void)
{
	uint32_t i;
	for (i = 0; i < OLG(nsegs); i++) {
		ol_seg *s = &OLG(segs)[i];
		zend_string_release(s->fname);
		if (s->cname) {
			zend_string_release(s->cname);
		}
		if (s->file) {
			zend_string_release(s->file);
		}
	}
	OLG(nsegs) = 0;
	if (OLG(segs_cap) > 4096) {
		free(OLG(segs));
		OLG(segs) = NULL;
		OLG(segs_cap) = 0;
	}
}

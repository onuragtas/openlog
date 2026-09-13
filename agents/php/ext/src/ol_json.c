/*
 * JSON encoder (contract §2), datagram splitting (§2.3) and non-blocking transport (§1).
 * SPDX-License-Identifier: Apache-2.0
 */
#include "ol.h"

#include <arpa/inet.h>
#include <errno.h>
#include <netinet/in.h>
#include <sys/un.h>
#include <time.h>
#include <unistd.h>

#ifndef MSG_NOSIGNAL
# define MSG_NOSIGNAL 0
#endif

typedef struct {
	char *p;
	size_t len;
	size_t cap;
	bool ok;
} ol_w;

static inline void w_raw(ol_w *w, const char *s, size_t n)
{
	if (!w->ok || w->len + n > w->cap) {
		w->ok = false;
		return;
	}
	memcpy(w->p + w->len, s, n);
	w->len += n;
}

#define W_LIT(w, lit) w_raw((w), (lit), sizeof(lit) - 1)

static void w_strn(ol_w *w, const char *s, size_t n)
{
	static const char hex[] = "0123456789abcdef";
	size_t i, run = 0;
	W_LIT(w, "\"");
	for (i = 0; i < n && w->ok; i++) {
		unsigned char c = (unsigned char) s[i];
		if (c == '"' || c == '\\' || c < 0x20 || c == 0x7f) {
			if (run) {
				w_raw(w, s + i - run, run);
				run = 0;
			}
			if (c == '"' || c == '\\') {
				char e[2] = {'\\', (char) c};
				w_raw(w, e, 2);
			} else if (c == '\n') {
				W_LIT(w, "\\n");
			} else if (c == '\t') {
				W_LIT(w, "\\t");
			} else if (c == '\r') {
				W_LIT(w, "\\r");
			} else {
				char e[6] = {'\\', 'u', '0', '0', hex[c >> 4], hex[c & 15]};
				w_raw(w, e, 6);
			}
		} else {
			run++;
		}
	}
	if (run) {
		w_raw(w, s + n - run, run);
	}
	W_LIT(w, "\"");
}

static void w_str(ol_w *w, const char *s)
{
	w_strn(w, s ? s : "", s ? strlen(s) : 0);
}

static void w_u64(ol_w *w, uint64_t v)
{
	char b[24];
	int n = snprintf(b, sizeof(b), "%llu", (unsigned long long) v);
	w_raw(w, b, (size_t) n);
}

static void w_i64(ol_w *w, int64_t v)
{
	char b[24];
	int n = snprintf(b, sizeof(b), "%lld", (long long) v);
	w_raw(w, b, (size_t) n);
}

static void w_dbl(ol_w *w, double v)
{
	char b[40];
	int n;
	if (v != v || v > 1.7976931348623157e308 || v < -1.7976931348623157e308) {
		W_LIT(w, "0.0");
		return;
	}
	n = snprintf(b, sizeof(b), "%.17g", v);
	w_raw(w, b, (size_t) n);
	if (!memchr(b, '.', (size_t) n) && !memchr(b, 'e', (size_t) n) && !memchr(b, 'n', (size_t) n)) {
		W_LIT(w, ".0");
	}
}

static void w_id(ol_w *w, uint64_t id)
{
	char b[19];
	if (id == 0) {
		W_LIT(w, "\"\"");
		return;
	}
	snprintf(b, sizeof(b), "\"%016llx\"", (unsigned long long) id);
	w_raw(w, b, 18);
}

static void w_attrs(ol_w *w, ol_attr *a, bool *first)
{
	for (; a && w->ok; a = a->next) {
		if (!*first) {
			W_LIT(w, ",");
		}
		*first = false;
		w_str(w, a->key);
		W_LIT(w, ":");
		switch (a->type) {
			case OL_AT_STR: w_strn(w, a->v.s.p, a->v.s.len); break;
			case OL_AT_INT: w_i64(w, a->v.i); break;
			case OL_AT_DBL: w_dbl(w, a->v.d); break;
			default: if (a->v.b) { W_LIT(w, "true"); } else { W_LIT(w, "false"); } break;
		}
	}
}

static inline uint64_t ol_unix_of(uint64_t mono)
{
	return OLG(req_unix) + (mono >= OLG(req_mono) ? mono - OLG(req_mono) : 0);
}

static void w_span(ol_w *w, ol_node *n, uint32_t idx, bool with_fast)
{
	ol_node *p = ol_node_at(n->emit_parent);
	bool first = true;
	ol_event *e;

	W_LIT(w, "{\"id\":");
	w_id(w, n->id);
	W_LIT(w, ",\"parent\":");
	if (with_fast && n->seg_parent != OL_NONE && n->seg_parent < OLG(nsegs)) {
		w_id(w, OLG(segs)[n->seg_parent].id); /* function trace sent: the innermost function segment */
	} else {
		w_id(w, p ? p->id : (idx == 0 ? OLG(remote_parent) : 0));
	}
	W_LIT(w, ",\"name\":");
	w_str(w, n->name && *n->name ? n->name : "php");
	W_LIT(w, ",\"kind\":");
	w_u64(w, n->kind);
	W_LIT(w, ",\"start\":");
	w_u64(w, ol_unix_of(n->start));
	W_LIT(w, ",\"dur\":");
	w_u64(w, n->dur);
	W_LIT(w, ",\"status\":");
	w_u64(w, n->status);
	if (n->status_msg && *n->status_msg) {
		W_LIT(w, ",\"status_msg\":");
		w_str(w, n->status_msg);
	}
	W_LIT(w, ",\"attrs\":{");
	w_attrs(w, n->attrs, &first);
	if (with_fast && n->fast_calls) {
		if (!first) {
			W_LIT(w, ",");
		}
		first = false;
		W_LIT(w, "\"openlog.php.fast_calls\":");
		w_u64(w, n->fast_calls);
		W_LIT(w, ",\"openlog.php.fast_calls_ns\":");
		w_u64(w, n->fast_ns);
	}
	W_LIT(w, "}");
	if (n->events) {
		W_LIT(w, ",\"events\":[");
		for (e = n->events; e && w->ok; e = e->next) {
			bool f2 = true;
			if (e != n->events) {
				W_LIT(w, ",");
			}
			W_LIT(w, "{\"name\":");
			w_str(w, e->name);
			W_LIT(w, ",\"time\":");
			w_u64(w, ol_unix_of(e->mono));
			W_LIT(w, ",\"attrs\":{");
			w_attrs(w, e->attrs, &f2);
			W_LIT(w, "}}");
		}
		W_LIT(w, "]");
	}
	W_LIT(w, "}");
}

/* ---------------- transport ---------------- */

bool ol_transport_parse(const char *spec)
{
	OLG(addr_state) = -1;
	memset(&OLG(addr), 0, sizeof(OLG(addr)));
	if (spec == NULL || *spec == '\0') {
		return false;
	}
	if (strncmp(spec, "unix://", 7) == 0 || spec[0] == '/') {
		const char *path = spec[0] == '/' ? spec : spec + 7;
		struct sockaddr_un *un = (struct sockaddr_un *) &OLG(addr);
		size_t plen = strlen(path);
		if (plen == 0 || plen >= sizeof(un->sun_path)) {
			return false;
		}
		un->sun_family = AF_UNIX;
		memcpy(un->sun_path, path, plen + 1);
		OLG(addrlen) = (socklen_t) (offsetof(struct sockaddr_un, sun_path) + plen + 1);
		OLG(addr_state) = 1;
		return true;
	}
	if (strncmp(spec, "udp://", 6) == 0) {
		char host[128];
		const char *h = spec + 6, *colon;
		long port;
		size_t hl;
		if (*h == '[') {
			const char *rb = strchr(h, ']');
			if (rb == NULL || rb[1] != ':') {
				return false;
			}
			hl = (size_t) (rb - h - 1);
			if (hl >= sizeof(host)) return false;
			memcpy(host, h + 1, hl);
			host[hl] = '\0';
			colon = rb + 1;
		} else {
			colon = strrchr(h, ':');
			if (colon == NULL) return false;
			hl = (size_t) (colon - h);
			if (hl >= sizeof(host)) return false;
			memcpy(host, h, hl);
			host[hl] = '\0';
		}
		port = strtol(colon + 1, NULL, 10);
		if (port <= 0 || port > 65535) {
			return false;
		}
		if (strcmp(host, "localhost") == 0) {
			strcpy(host, "127.0.0.1");
		}
		{
			struct sockaddr_in *in4 = (struct sockaddr_in *) &OLG(addr);
			struct sockaddr_in6 *in6 = (struct sockaddr_in6 *) &OLG(addr);
			if (inet_pton(AF_INET, host, &in4->sin_addr) == 1) {
				in4->sin_family = AF_INET;
				in4->sin_port = htons((uint16_t) port);
				OLG(addrlen) = sizeof(*in4);
			} else if (inet_pton(AF_INET6, host, &in6->sin6_addr) == 1) {
				in6->sin6_family = AF_INET6;
				in6->sin6_port = htons((uint16_t) port);
				OLG(addrlen) = sizeof(*in6);
			} else {
				return false; /* numeric addresses only: no DNS on the request path */
			}
		}
		OLG(addr_state) = 1;
		return true;
	}
	return false;
}

static bool ol_send(const char *buf, size_t len)
{
	ssize_t n;
	int pid = (int) getpid();
	if (OLG(addr_state) == 0) {
		if (!ol_transport_parse(OLG(transport))) {
			ol_log(OL_LOG_ERROR, "invalid openlog.transport \"%s\"", OLG(transport) ? OLG(transport) : "");
		}
	}
	if (OLG(addr_state) != 1) {
		return false;
	}
	if (OLG(fd) >= 0 && OLG(fd_pid) != pid) {
		/* inherited through fork: use our own socket */
		OLG(fd) = -1;
	}
	if (OLG(fd) < 0) {
		OLG(fd) = socket(OLG(addr).ss_family, SOCK_DGRAM | SOCK_NONBLOCK | SOCK_CLOEXEC, 0);
		if (OLG(fd) < 0) {
			return false;
		}
		OLG(fd_pid) = pid;
	}
	/* Unconnected sendto: a restarted forwarder (new socket inode) is picked up without reconnecting. */
	n = sendto(OLG(fd), buf, len, MSG_DONTWAIT | MSG_NOSIGNAL, (struct sockaddr *) &OLG(addr), OLG(addrlen));
	if (n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK) && OLG(retry_budget) > 0) {
		/*
		 * The receiver's datagram queue is short (net.unix.max_dgram_qlen, often 10): a split trace arrives as a
		 * burst. Give a live forwarder a moment to drain, bounded to a total of ~2 ms per message; a dead or
		 * stuck forwarder (queue never drains) costs at most that once per request.
		 */
		while (n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK) && OLG(retry_budget) > 0) {
			struct timespec ts = {0, 100000}; /* 100 µs */
			OLG(retry_budget)--;
			nanosleep(&ts, NULL);
			n = sendto(OLG(fd), buf, len, MSG_DONTWAIT | MSG_NOSIGNAL, (struct sockaddr *) &OLG(addr), OLG(addrlen));
		}
		if (n < 0) {
			OLG(retry_budget) = 0;
		}
	}
	if (n < 0) {
		int err = errno;
		if (err != ENOENT && err != ECONNREFUSED && err != EAGAIN && err != EWOULDBLOCK) {
			ol_log(OL_LOG_INFO, "sendto failed: %s", strerror(err));
		}
		return false;
	}
	return true;
}

/* ---------------- message ---------------- */

static void w_resource(ol_w *w)
{
	const char *v;
	W_LIT(w, "\"resource\":{\"service.name\":");
	v = getenv("OPENLOG_SERVICE_NAME");
	if (v && *v) {
		w_strn(w, v, strlen(v) > 256 ? 256 : strlen(v));
	} else if (OLG(service_name) && *OLG(service_name)) {
		w_strn(w, OLG(service_name), strlen(OLG(service_name)) > 256 ? 256 : strlen(OLG(service_name)));
	} else {
		w_str(w, OLG(is_cli) ? "php-cli" : "php-app");
	}
#define OL_RES_OPT(key, env, ini) do { \
		const char *e_ = getenv(env); \
		const char *val_ = (e_ && *e_) ? e_ : (ini); \
		if (val_ && *val_) { W_LIT(w, ",\"" key "\":"); w_strn(w, val_, strlen(val_) > 256 ? 256 : strlen(val_)); } \
	} while (0)
	OL_RES_OPT("service.namespace", "OPENLOG_SERVICE_NAMESPACE", OLG(service_namespace));
	OL_RES_OPT("service.version", "OPENLOG_SERVICE_VERSION", OLG(service_version));
	OL_RES_OPT("deployment.environment.name", "OPENLOG_ENVIRONMENT", OLG(environment));
#undef OL_RES_OPT
	W_LIT(w, ",\"process.runtime.name\":\"php\",\"process.runtime.version\":");
	w_str(w, PHP_VERSION);
	W_LIT(w, ",\"php.sapi\":");
	w_str(w, sapi_module.name);
	W_LIT(w, ",\"telemetry.distro.name\":\"openlog-php\",\"telemetry.distro.version\":\"" PHP_OPENLOG_VERSION "\"");
	if (OLG(container_id)[0]) {
		W_LIT(w, ",\"container.id\":");
		w_str(w, OLG(container_id));
	}
	W_LIT(w, "}");
}

static size_t w_header(ol_w *w, bool function_trace)
{
	static const char hex[] = "0123456789abcdef";
	char tid[34];
	int i;
	w->len = 0;
	w->ok = true;
	W_LIT(w, "{\"v\":1,\"pid\":");
	w_u64(w, (uint64_t) getpid());
	tid[0] = '"';
	for (i = 0; i < 16; i++) {
		tid[1 + 2 * i] = hex[OLG(trace_id)[i] >> 4];
		tid[2 + 2 * i] = hex[OLG(trace_id)[i] & 15];
	}
	tid[33] = '"';
	W_LIT(w, ",\"trace_id\":");
	w_raw(w, tid, 34);
	W_LIT(w, ",");
	w_resource(w);
	W_LIT(w, ",\"sampling_ratio\":");
	w_dbl(w, OLG(applied_ratio) > 0 ? OLG(applied_ratio) : 1.0);
	if (function_trace) {
		W_LIT(w, ",\"function_trace\":true");
	} else {
		W_LIT(w, ",\"function_trace\":false");
	}
	W_LIT(w, ",\"spans\":[");
	return w->len;
}

static bool ol_send_part(ol_w *w, int seq, bool last)
{
	char tail[96];
	int n = snprintf(tail, sizeof(tail), "],\"seq\":%d,\"last\":%s,\"dropped_spans\":%u}", seq, last ? "true" : "false", OLG(dropped));
	w->cap = OL_DGRAM_MAX;
	w_raw(w, tail, (size_t) n);
	if (!w->ok) {
		return false;
	}
	OLG(c_parts)++;
	return ol_send(w->p, w->len);
}

#define OL_TAIL_RESERVE 96

/* zend_string value, UTF-8 cleaned and truncated to 4 KiB */
static void w_zstr(ol_w *w, zend_string *s)
{
	char buf[OL_STR_MAX + 1];
	size_t n = ol_utf8_clean(buf, ZSTR_VAL(s), ZSTR_LEN(s), OL_STR_MAX);
	w_strn(w, buf, n);
}

/* A sampled function segment (ol_sampler.c) as a kind 1 span. Times are ± half a sampling interval. */
static void w_segment(ol_w *w, ol_seg *s, ol_node *root)
{
	uint64_t half = OLG(sample_interval_ns) / 2;
	uint64_t start = s->first > OLG(req_mono) + half ? s->first - half : OLG(req_mono);
	uint64_t end = s->last + half;
	char name[1100];
	size_t nl;

	W_LIT(w, "{\"id\":");
	w_id(w, s->id);
	W_LIT(w, ",\"parent\":");
	w_id(w, s->parent != OL_NONE && s->parent < OLG(nsegs) ? OLG(segs)[s->parent].id : root->id);
	W_LIT(w, ",\"name\":");
	if (s->cname) {
		nl = (size_t) snprintf(name, sizeof(name), "%.*s::%.*s", (int) (ZSTR_LEN(s->cname) > 512 ? 512 : ZSTR_LEN(s->cname)),
			ZSTR_VAL(s->cname), (int) (ZSTR_LEN(s->fname) > 512 ? 512 : ZSTR_LEN(s->fname)), ZSTR_VAL(s->fname));
	} else {
		nl = (size_t) snprintf(name, sizeof(name), "%.*s", (int) (ZSTR_LEN(s->fname) > 1024 ? 1024 : ZSTR_LEN(s->fname)), ZSTR_VAL(s->fname));
	}
	if (nl >= sizeof(name)) {
		nl = sizeof(name) - 1;
	}
	{
		char clean[sizeof(name)];
		size_t cl = ol_utf8_clean(clean, name, nl, sizeof(name) - 1);
		w_strn(w, clean, cl);
	}
	W_LIT(w, ",\"kind\":1,\"start\":");
	w_u64(w, ol_unix_of(start));
	W_LIT(w, ",\"dur\":");
	w_u64(w, end > start ? end - start : 0);
	W_LIT(w, ",\"status\":0,\"attrs\":{\"code.function.name\":");
	w_zstr(w, s->fname);
	if (s->cname) {
		W_LIT(w, ",\"code.namespace\":");
		w_zstr(w, s->cname);
	}
	if (s->file) {
		W_LIT(w, ",\"code.file.path\":");
		w_zstr(w, s->file);
		W_LIT(w, ",\"code.line.number\":");
		w_u64(w, s->line);
	}
	W_LIT(w, ",\"openlog.php.segment\":\"function\",\"openlog.php.samples\":");
	w_u64(w, s->samples);
	W_LIT(w, "}}");
}

static inline bool ol_emitted(ol_node *n, bool segs)
{
	return !(n->flags & OL_NF_DEAD) && (!(n->flags & OL_NF_SEGMENT) || segs);
}

void ol_emit(void)
{
	ol_node *root = ol_node_at(0);
	uint32_t i, nn = OLG(nnodes);
	bool segs, send_ok = true, any_in_part = false;
	ol_w w;
	size_t header_len;
	int seq = 0;

	if (root == NULL) {
		return;
	}
	segs = (OLG(seg_kept) > 0 || OLG(nsegs) > 0) &&
		(root->dur >= (uint64_t) OLG(tt_threshold_ms) * 1000000ULL || root->status == OL_STATUS_ERROR);

	/* effective parents: nearest emitted ancestor (parents always have smaller indices) */
	root->emit_parent = OL_NONE;
	for (i = 1; i < nn; i++) {
		ol_node *n = ol_node_at(i);
		ol_node *p = ol_node_at(n->parent);
		if (p == NULL) {
			n->emit_parent = 0;
		} else if (ol_emitted(p, segs)) {
			n->emit_parent = n->parent;
		} else {
			n->emit_parent = p->emit_parent != OL_NONE ? p->emit_parent : 0;
		}
	}
	if (segs) {
		/* fast calls of skipped segments count towards their nearest emitted ancestor */
		for (i = nn; i-- > 1;) {
			ol_node *n = ol_node_at(i);
			if (!ol_emitted(n, segs) && n->fast_calls) {
				ol_node *p = ol_node_at(n->emit_parent);
				if (p) {
					p->fast_calls += n->fast_calls;
					p->fast_ns += n->fast_ns;
				}
			}
		}
	} else {
		/* segments are not sent: their drops do not count */
	}

	OLG(retry_budget) = 20;
	w.p = OLG(out);
	w.cap = OL_DGRAM_MAX - OL_TAIL_RESERVE;
	header_len = w_header(&w, segs);
	if (!w.ok) {
		OLG(c_dropped_messages)++;
		OLG(p_dropped_messages)++;
		return;
	}
	for (i = 0; i < nn + (segs ? OLG(nsegs) : 0); i++) {
		size_t mark;
		if (i < nn) {
			ol_node *n = ol_node_at(i);
			if (!ol_emitted(n, segs)) {
				continue;
			}
			mark = w.len;
			if (any_in_part) {
				W_LIT(&w, ",");
			}
			w_span(&w, n, i, segs);
		} else {
			mark = w.len;
			if (any_in_part) {
				W_LIT(&w, ",");
			}
			w_segment(&w, &OLG(segs)[i - nn], root);
		}
		if (w.ok) {
			any_in_part = true;
			continue;
		}
		/* part full */
		w.len = mark;
		w.ok = true;
		if (!any_in_part) {
			OLG(dropped)++; /* a single span larger than a datagram */
			continue;
		}
		if (seq >= 255) {
			OLG(dropped) += 1;
			continue;
		}
		if (!ol_send_part(&w, seq, false)) {
			send_ok = false;
		}
		seq++;
		w.cap = OL_DGRAM_MAX - OL_TAIL_RESERVE;
		w.len = header_len;
		w.ok = true;
		any_in_part = false;
		i--; /* retry this span in the next part */
	}
	if (!ol_send_part(&w, seq, true)) {
		send_ok = false;
	}
	OLG(c_messages)++;
	OLG(c_dropped_spans) += OLG(dropped);
	if (send_ok) {
		OLG(p_send_errors) = 0;
		OLG(p_dropped_messages) = 0;
	} else {
		OLG(c_send_errors)++;
		OLG(p_send_errors)++;
		OLG(c_dropped_messages)++;
		OLG(p_dropped_messages)++;
	}
}

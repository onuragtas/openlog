/*
 * Message encoding (contract §2), datagram splitting (§2.3) and non-blocking transport (§1). The JSON writer itself
 * is in ol_text.c.
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

static inline void w_str(ol_w *w, const char *s)
{
	ol_w_strn(w, s ? s : "", s ? strlen(s) : 0);
}

/* A string from outside the arena (environment, ini): UTF-8 cleaned and truncated. */
static void w_ext_str(ol_w *w, const char *s, size_t max)
{
	char buf[OL_STR_MAX + 1];
	size_t n = strlen(s);
	if (max > OL_STR_MAX) {
		max = OL_STR_MAX;
	}
	n = ol_utf8_clean(buf, s, n, max);
	ol_w_strn(w, buf, n);
}

static void w_attrs(ol_w *w, ol_attr *a, bool *first)
{
	for (; a && w->ok; a = a->next) {
		if (!*first) {
			OL_W_LIT(w, ",");
		}
		*first = false;
		ol_w_key(w, a->key);
		switch (a->type) {
			case OL_AT_STR: ol_w_strn(w, a->v.s.p, a->v.s.len); break;
			case OL_AT_INT: ol_w_i64(w, a->v.i); break;
			case OL_AT_DBL: ol_w_dbl(w, a->v.d); break;
			default: if (a->v.b) { OL_W_LIT(w, "true"); } else { OL_W_LIT(w, "false"); } break;
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

	OL_W_LIT(w, "{\"id\":");
	ol_w_id(w, n->id);
	OL_W_LIT(w, ",\"parent\":");
	if (with_fast && n->seg_parent != OL_NONE && n->seg_parent < OLG(nsegs)) {
		ol_w_id(w, OLG(segs)[n->seg_parent].id); /* function trace sent: the innermost function segment */
	} else {
		ol_w_id(w, p ? p->id : (idx == 0 ? OLG(remote_parent) : 0));
	}
	OL_W_LIT(w, ",\"name\":");
	w_str(w, n->name && *n->name ? n->name : "php");
	OL_W_LIT(w, ",\"kind\":");
	ol_w_u64(w, n->kind);
	OL_W_LIT(w, ",\"start\":");
	ol_w_u64(w, ol_unix_of(n->start));
	OL_W_LIT(w, ",\"dur\":");
	ol_w_u64(w, n->dur);
	OL_W_LIT(w, ",\"status\":");
	ol_w_u64(w, n->status);
	if (n->status_msg && *n->status_msg) {
		OL_W_LIT(w, ",\"status_msg\":");
		w_str(w, n->status_msg);
	}
	OL_W_LIT(w, ",\"attrs\":{");
	w_attrs(w, n->attrs, &first);
	if (with_fast && n->fast_calls) {
		if (!first) {
			OL_W_LIT(w, ",");
		}
		first = false;
		OL_W_LIT(w, "\"openlog.php.fast_calls\":");
		ol_w_u64(w, n->fast_calls);
		OL_W_LIT(w, ",\"openlog.php.fast_calls_ns\":");
		ol_w_u64(w, n->fast_ns);
	}
	OL_W_LIT(w, "}");
	if (n->events) {
		OL_W_LIT(w, ",\"events\":[");
		for (e = n->events; e && w->ok; e = e->next) {
			bool f2 = true;
			if (e != n->events) {
				OL_W_LIT(w, ",");
			}
			OL_W_LIT(w, "{\"name\":");
			w_str(w, e->name);
			OL_W_LIT(w, ",\"time\":");
			ol_w_u64(w, ol_unix_of(e->mono));
			OL_W_LIT(w, ",\"attrs\":{");
			w_attrs(w, e->attrs, &f2);
			OL_W_LIT(w, "}}");
		}
		OL_W_LIT(w, "]");
	}
	OL_W_LIT(w, "}");
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
	OL_W_LIT(w, "\"resource\":{\"service.name\":");
	v = getenv("OPENLOG_SERVICE_NAME");
	if (v && *v) {
		w_ext_str(w, v, 256);
	} else if (OLG(service_name) && *OLG(service_name)) {
		w_ext_str(w, OLG(service_name), 256);
	} else {
		w_str(w, OLG(txn_web) ? "php-app" : "php-cli");
	}
#define OL_RES_OPT(key, env, ini) do { \
		const char *e_ = getenv(env); \
		const char *val_ = (e_ && *e_) ? e_ : (ini); \
		if (val_ && *val_) { OL_W_LIT(w, ",\"" key "\":"); w_ext_str(w, val_, 256); } \
	} while (0)
	OL_RES_OPT("service.namespace", "OPENLOG_SERVICE_NAMESPACE", OLG(service_namespace));
	OL_RES_OPT("service.version", "OPENLOG_SERVICE_VERSION", OLG(service_version));
	OL_RES_OPT("deployment.environment.name", "OPENLOG_ENVIRONMENT", OLG(environment));
#undef OL_RES_OPT
	OL_W_LIT(w, ",\"process.runtime.name\":\"php\",\"process.runtime.version\":");
	w_str(w, PHP_VERSION);
	OL_W_LIT(w, ",\"php.sapi\":");
	w_str(w, sapi_module.name);
	OL_W_LIT(w, ",\"telemetry.distro.name\":\"openlog-php\",\"telemetry.distro.version\":\"" PHP_OPENLOG_VERSION "\"");
	if (OLG(container_id)[0]) {
		OL_W_LIT(w, ",\"container.id\":");
		w_str(w, OLG(container_id));
	}
	OL_W_LIT(w, "}");
}

static size_t w_header(ol_w *w, const uint8_t *trace_id, double ratio, bool function_trace)
{
	static const char hex[] = "0123456789abcdef";
	char tid[34];
	int i;
	w->len = 0;
	w->ok = true;
	OL_W_LIT(w, "{\"v\":1,\"pid\":");
	ol_w_u64(w, (uint64_t) getpid());
	tid[0] = '"';
	for (i = 0; i < 16; i++) {
		tid[1 + 2 * i] = hex[trace_id[i] >> 4];
		tid[2 + 2 * i] = hex[trace_id[i] & 15];
	}
	tid[33] = '"';
	OL_W_LIT(w, ",\"trace_id\":");
	ol_w_raw(w, tid, 34);
	OL_W_LIT(w, ",");
	w_resource(w);
	OL_W_LIT(w, ",\"sampling_ratio\":");
	ol_w_dbl(w, ratio > 0 ? ratio : 1.0);
	if (function_trace) {
		OL_W_LIT(w, ",\"function_trace\":true");
	} else {
		OL_W_LIT(w, ",\"function_trace\":false");
	}
	OL_W_LIT(w, ",\"spans\":[");
	return w->len;
}

static bool ol_send_part(ol_w *w, int seq, bool last, uint32_t dropped)
{
	w->cap = OL_DGRAM_MAX;
	OL_W_LIT(w, "],\"seq\":");
	ol_w_u64(w, (uint64_t) seq);
	if (last) {
		OL_W_LIT(w, ",\"last\":true,\"dropped_spans\":");
	} else {
		OL_W_LIT(w, ",\"last\":false,\"dropped_spans\":");
	}
	ol_w_u64(w, dropped);
	OL_W_LIT(w, "}");
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
	ol_w_strn(w, buf, n);
}

/* A sampled function segment (ol_sampler.c) as a kind 1 span. Times are ± half a sampling interval. */
static void w_segment(ol_w *w, ol_seg *s, ol_node *root)
{
	uint64_t half = OLG(sample_interval_ns) / 2;
	uint64_t start = s->first > OLG(req_mono) + half ? s->first - half : OLG(req_mono);
	uint64_t end = s->last + half;
	char name[1100];
	size_t nl;

	OL_W_LIT(w, "{\"id\":");
	ol_w_id(w, s->id);
	OL_W_LIT(w, ",\"parent\":");
	ol_w_id(w, s->parent != OL_NONE && s->parent < OLG(nsegs) ? OLG(segs)[s->parent].id : root->id);
	OL_W_LIT(w, ",\"name\":");
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
		ol_w_strn(w, clean, cl);
	}
	OL_W_LIT(w, ",\"kind\":1,\"start\":");
	ol_w_u64(w, ol_unix_of(start));
	OL_W_LIT(w, ",\"dur\":");
	ol_w_u64(w, end > start ? end - start : 0);
	OL_W_LIT(w, ",\"status\":0,\"attrs\":{\"code.function.name\":");
	w_zstr(w, s->fname);
	if (s->cname) {
		OL_W_LIT(w, ",\"code.namespace\":");
		w_zstr(w, s->cname);
	}
	if (s->file) {
		OL_W_LIT(w, ",\"code.file.path\":");
		w_zstr(w, s->file);
		OL_W_LIT(w, ",\"code.line.number\":");
		ol_w_u64(w, s->line);
	}
	OL_W_LIT(w, ",\"openlog.php.segment\":\"function\",\"openlog.php.samples\":");
	ol_w_u64(w, s->samples);
	OL_W_LIT(w, "}}");
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
	}

	OLG(retry_budget) = 20;
	w.p = OLG(out);
	w.cap = OL_DGRAM_MAX - OL_TAIL_RESERVE;
	header_len = w_header(&w, OLG(trace_id), OLG(applied_ratio), segs);
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
				OL_W_LIT(&w, ",");
			}
			w_span(&w, n, i, segs);
		} else {
			mark = w.len;
			if (any_in_part) {
				OL_W_LIT(&w, ",");
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
		if (!ol_send_part(&w, seq, false, OLG(dropped))) {
			send_ok = false;
		}
		seq++;
		w.cap = OL_DGRAM_MAX - OL_TAIL_RESERVE;
		w.len = header_len;
		w.ok = true;
		any_in_part = false;
		i--; /* retry this span in the next part */
	}
	if (!ol_send_part(&w, seq, true, OLG(dropped))) {
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

static void w_light_attr(ol_w *w, const char *key, const char *value)
{
	if (value && *value) {
		OL_W_LIT(w, ",");
		ol_w_key(w, key);
		w_str(w, value);
	}
}

/* A concurrent worker request (inst_workers.c): one message with its root span only. Strings are UTF-8 clean. */
void ol_emit_light(const ol_wtx *t)
{
	ol_w w;
	char norm[1024], name[1100], clean[1100];
	const char *method = t->method[0] ? t->method : "GET";
	size_t cl;
	int nl;

	w.p = OLG(out);
	w.cap = OL_DGRAM_MAX - OL_TAIL_RESERVE;
	w_header(&w, t->trace_id, t->ratio, false);
	ol_normalize_path(norm, sizeof(norm), t->path, strlen(t->path));
	nl = snprintf(name, sizeof(name), "%s %s", method, norm);
	nl = nl < 0 ? 0 : ((size_t) nl >= sizeof(name) ? (int) sizeof(name) - 1 : nl);
	cl = ol_utf8_clean(clean, name, (size_t) nl, sizeof(clean) - 1);
	OL_W_LIT(&w, "{\"id\":");
	ol_w_id(&w, t->span_id);
	OL_W_LIT(&w, ",\"parent\":");
	ol_w_id(&w, t->remote_parent);
	OL_W_LIT(&w, ",\"name\":");
	ol_w_strn(&w, clean, cl);
	OL_W_LIT(&w, ",\"kind\":2,\"start\":");
	ol_w_u64(&w, t->start_unix);
	OL_W_LIT(&w, ",\"dur\":");
	ol_w_u64(&w, ol_mono_ns() - t->start_mono);
	OL_W_LIT(&w, ",\"status\":");
	ol_w_u64(&w, t->status >= 500 ? OL_STATUS_ERROR : OL_STATUS_UNSET);
	OL_W_LIT(&w, ",\"attrs\":{\"openlog.php.concurrent\":true");
	w_light_attr(&w, "http.request.method", method);
	w_light_attr(&w, "url.path", t->path);
	w_light_attr(&w, "url.scheme", t->https ? "https" : "http");
	w_light_attr(&w, "server.address", t->host);
	if (t->port > 0) {
		OL_W_LIT(&w, ",\"server.port\":");
		ol_w_i64(&w, t->port);
	}
	w_light_attr(&w, "client.address", t->client);
	w_light_attr(&w, "user_agent.original", t->ua);
	w_light_attr(&w, "network.protocol.version", t->proto);
	if (t->status > 0) {
		OL_W_LIT(&w, ",\"http.response.status_code\":");
		ol_w_i64(&w, t->status);
	}
	OL_W_LIT(&w, "}}");
	OLG(retry_budget) = 20;
	OLG(c_messages)++;
	if (!w.ok || !ol_send_part(&w, 0, true, 0)) {
		OLG(c_send_errors)++;
		OLG(p_send_errors)++;
		OLG(c_dropped_messages)++;
		OLG(p_dropped_messages)++;
	}
}

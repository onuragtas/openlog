/*
 * W3C trace context, sampling, request root span, process info.
 * SPDX-License-Identifier: Apache-2.0
 *
 * A transaction starts from an ol_reqinfo: the SAPI request (FPM, CGI, Apache, CLI) at RINIT, or a request object of a
 * long-running worker (inst_workers.c).
 */
#include "ol.h"
#include "ext/standard/php_string.h"

#include <ctype.h>
#include <stdio.h>

/* traceparent parsing: ol_text.c (ol_parse_traceparent) */

size_t ol_format_traceparent(char *buf, size_t cap, uint64_t span_id)
{
	static const char hex[] = "0123456789abcdef";
	const uint8_t *tid = OLG(trace_id);
	unsigned flags = OLG(recording) ? 1 : (OLG(trace_flags) & 1);
	char t[33];
	int i, n;

	if (OLG(concurrent)) {
		const ol_wtx *w = NULL;
		int which = ol_worker_context(&w);
		if (which < 0) {
			return 0; /* concurrent requests and this code belongs to none of them: nothing is propagated */
		}
		if (which == 1) {
			tid = w->trace_id;
			span_id = w->span_id;
			flags = w->sampled ? 1 : (w->trace_flags & 1);
		}
	}
	for (i = 0; i < 16; i++) {
		t[2 * i] = hex[tid[i] >> 4];
		t[2 * i + 1] = hex[tid[i] & 15];
	}
	t[32] = '\0';
	n = snprintf(buf, cap, "00-%s-%016llx-%02x", t, (unsigned long long) span_id, flags);
	return n > 0 && (size_t) n < cap ? (size_t) n : 0;
}

/* tracestate of the request the current code belongs to ("" when none) */
const char *ol_tracestate(void)
{
	if (OLG(concurrent)) {
		const ol_wtx *w = NULL;
		int which = ol_worker_context(&w);
		if (which < 0) {
			return "";
		}
		if (which == 1) {
			return w->tracestate;
		}
	}
	return OLG(tracestate);
}

/* Request variable: FastCGI/CGI params through sapi_getenv, else $_SERVER (e.g. Apache), else process env (CLI). */
char *ol_server_var(const char *name, size_t len)
{
	char *v;
	if (OLG(is_cli)) {
		v = getenv(name);
		return v ? estrdup(v) : NULL;
	}
	v = sapi_getenv((char *) name, len);
	if (v) {
		return v;
	}
	if (zend_is_auto_global_str((char *) ZEND_STRL("_SERVER")) && Z_TYPE(PG(http_globals)[TRACK_VARS_SERVER]) == IS_ARRAY) {
		zval *zv = zend_hash_str_find(Z_ARRVAL(PG(http_globals)[TRACK_VARS_SERVER]), name, len);
		if (zv && Z_TYPE_P(zv) == IS_STRING) {
			return estrndup(Z_STRVAL_P(zv), Z_STRLEN_P(zv));
		}
	}
	return NULL;
}

/* Request description of the SAPI request. Strings stored in owned[] are emalloc'ed; the caller frees them. */
size_t ol_reqinfo_sapi(ol_reqinfo *ri, char **owned, size_t max)
{
	size_t n = 0;
	char *v;

	memset(ri, 0, sizeof(*ri));
	ri->web = !OLG(is_cli);
#define OL_VAR(field, name) do { \
		v = n < max ? ol_server_var(name, sizeof(name) - 1) : NULL; \
		if (v) { owned[n++] = v; ri->field.p = v; ri->field.len = strlen(v); } \
	} while (0)
	if (!ri->web) {
		OL_VAR(traceparent, "TRACEPARENT");
		OL_VAR(tracestate, "TRACESTATE");
		return n;
	}
	OL_VAR(traceparent, "HTTP_TRACEPARENT");
	OL_VAR(tracestate, "HTTP_TRACESTATE");
	if (SG(request_info).request_method) {
		ri->method.p = SG(request_info).request_method;
		ri->method.len = strlen(SG(request_info).request_method);
	}
	/* Under FPM request_uri is the script; the client's path is REQUEST_URI. */
	OL_VAR(uri, "REQUEST_URI");
	if (ri->uri.p == NULL && SG(request_info).request_uri) {
		ri->uri.p = SG(request_info).request_uri;
		ri->uri.len = strlen(SG(request_info).request_uri);
	}
	v = n < max ? ol_server_var("HTTPS", 5) : NULL;
	if (v) {
		owned[n++] = v;
		ri->https = *v && strcasecmp(v, "off") != 0;
	}
	OL_VAR(host, "HTTP_HOST");
	OL_VAR(remote_addr, "REMOTE_ADDR");
	OL_VAR(user_agent, "HTTP_USER_AGENT");
	OL_VAR(protocol, "SERVER_PROTOCOL");
#undef OL_VAR
	return n;
}

static void ol_random_trace_id(uint8_t *tid)
{
	uint64_t a = ol_rand64(), b = ol_rand64();
	int i;
	for (i = 0; i < 8; i++) {
		tid[i] = (uint8_t) (a >> (56 - 8 * i));
		tid[8 + i] = (uint8_t) (b >> (56 - 8 * i));
	}
}

/* OTel probability threshold for tracestate "ot=th:<hex>" (rejection threshold, trailing zeros trimmed). */
static void ol_threshold_hex(double ratio, char *out, size_t cap)
{
	static const char hex[] = "0123456789abcdef";
	uint64_t t = (uint64_t) ((1.0 - ratio) * 72057594037927936.0); /* 2^56 */
	char digits[15];
	int i, n = 14;
	if (t >= 72057594037927936ULL) {
		t = 72057594037927935ULL;
	}
	for (i = 13; i >= 0; i--) {
		digits[i] = hex[t & 15];
		t >>= 4;
	}
	while (n > 1 && digits[n - 1] == '0') n--;
	digits[n] = '\0';
	snprintf(out, cap, "%s", digits);
}

/*
 * Trace id, remote parent, flags, tracestate and the sampling decision of a new transaction: a valid traceparent is
 * continued (parent based), otherwise a new trace is head-sampled with openlog.sampling_ratio. Returns "record".
 */
bool ol_trace_decision(const ol_str *tp, const ol_str *ts, uint8_t *tid, uint64_t *parent, uint8_t *flags,
	char *tracestate, size_t tscap, double *ratio, bool *parent_ok)
{
	double r = OLG(sampling_ratio);
	bool rec;

	*parent = 0;
	*flags = 0;
	*ratio = 1.0;
	tracestate[0] = '\0';
	*parent_ok = tp->p != NULL && ol_parse_traceparent(tp->p, tp->len, tid, parent, flags);
	if (*parent_ok) {
		if (ts->p && ts->len < tscap) {
			memcpy(tracestate, ts->p, ts->len);
			tracestate[ts->len] = '\0';
		}
		return (*flags & 1) != 0;
	}
	*parent = 0;
	ol_random_trace_id(tid);
	if (r >= 1.0) {
		rec = true;
	} else if (r <= 0.0) {
		rec = false;
	} else {
		rec = (double) (ol_rand64() >> 11) / 9007199254740992.0 < r;
		*ratio = r;
		if (tscap > 6 + 15) {
			ol_threshold_hex(r, tracestate + 6, tscap - 6);
			memcpy(tracestate, "ot=th:", 6);
		}
	}
	*flags = rec ? 1 : 0;
	return rec;
}

/* Path of a request target: query removed, absolute-form ("http://host/path") reduced to the path. */
size_t ol_req_path(const ol_str *uri, const char **path)
{
	const char *u = uri->p, *q;
	size_t l = uri->len;
	q = memchr(u, '?', l);
	if (q) {
		l = (size_t) (q - u);
	}
	if (ol_str_starts_ci(u, l, "http://") || ol_str_starts_ci(u, l, "https://")) {
		const char *slash = memchr(u + 8, '/', l > 8 ? l - 8 : 0);
		if (slash) {
			l -= (size_t) (slash - u);
			u = slash;
		}
	}
	*path = u;
	return l;
}

/* "host[:port]" / "[v6]:port": length of the host part; *port = the port or 0. */
size_t ol_req_host(const ol_str *host, int64_t *port)
{
	const char *h = host->p, *c = NULL, *rb = NULL;
	size_t i, l = host->len;
	*port = 0;
	for (i = 0; i < l; i++) {
		if (h[i] == ':') {
			c = h + i;
		} else if (h[i] == ']' && rb == NULL) {
			rb = h + i;
		}
	}
	if (c && (rb == NULL || rb < c)) {
		const char *d;
		int64_t p = 0;
		for (d = c + 1; d < h + l && OL_ISDIGIT(*d) && p < 100000; d++) {
			p = p * 10 + (*d - '0');
		}
		*port = p;
		return (size_t) (c - h);
	}
	return l;
}

void ol_request_context(const ol_reqinfo *ri)
{
	bool parent_ok;
	uint32_t idx;
	ol_node *root;

	OLG(txn_web) = ri->web;
	OLG(txn_method)[0] = '\0';
	if (ri->web) {
		const char *m = ri->method.p ? ri->method.p : "GET";
		size_t ml = ri->method.p ? ri->method.len : 3, i;
		for (i = 0; i < 16 && i < ml; i++) {
			OLG(txn_method)[i] = OL_TOUPPER(m[i]);
		}
		OLG(txn_method)[i] = '\0';
	}
	OLG(recording) = ol_trace_decision(&ri->traceparent, &ri->tracestate, OLG(trace_id), &OLG(remote_parent),
		&OLG(trace_flags), OLG(tracestate), sizeof(OLG(tracestate)), &OLG(applied_ratio), &parent_ok);
	OLG(local_root_id) = ol_rand64();
	OLG(propagate) = true;
	if (!OLG(recording)) {
		/* unsampled: nothing recorded, but outgoing requests carry the (unsampled) context */
		if (parent_ok) {
			OLG(local_root_id) = OLG(remote_parent);
		}
		return;
	}

	idx = ol_node_new(ri->web ? OL_KIND_SERVER : OL_KIND_INTERNAL, OL_NF_ROOT, OL_NONE);
	root = ol_node_at(idx);
	if (root == NULL) {
		OLG(recording) = false;
		return;
	}
	root->start = OLG(req_mono);
	OLG(local_root_id) = root->id;

	if (!ri->web) {
		ol_attr_bool(root, "openlog.php.cli", true);
		return;
	}
	if (ri->method.p) {
		ol_attr_str(root, "http.request.method", ri->method.p, ri->method.len > 16 ? 16 : ri->method.len);
	}
	if (ri->uri.p) {
		const char *p;
		size_t l = ol_req_path(&ri->uri, &p);
		ol_attr_str(root, "url.path", p, l > 2048 ? 2048 : l);
	}
	ol_attr_static(root, "url.scheme", ri->https ? "https" : "http");
	if (ri->host.p) {
		int64_t port;
		size_t hl = ol_req_host(&ri->host, &port);
		if (port > 0) {
			ol_attr_int(root, "server.port", port);
		}
		ol_attr_str(root, "server.address", ri->host.p, hl > 256 ? 256 : hl);
	}
	if (ri->remote_addr.p) {
		ol_attr_str(root, "client.address", ri->remote_addr.p, ri->remote_addr.len > 64 ? 64 : ri->remote_addr.len);
	}
	if (ri->user_agent.p) {
		ol_attr_str(root, "user_agent.original", ri->user_agent.p, ri->user_agent.len > 512 ? 512 : ri->user_agent.len);
	}
	if (ri->protocol.p && ri->protocol.len > 5 && strncmp(ri->protocol.p, "HTTP/", 5) == 0) {
		ol_attr_str(root, "network.protocol.version", ri->protocol.p + 5, ri->protocol.len - 5 > 8 ? 8 : ri->protocol.len - 5);
	}
}

/* container.id from cgroup v1 paths or the docker mountinfo entries (read once per process). */
void ol_process_info(void)
{
	static const char *files[] = {"/proc/self/cgroup", "/proc/self/mountinfo", NULL};
	char line[1024];
	int f;

	if (OLG(proc_info_done)) {
		return;
	}
	OLG(proc_info_done) = true;
	OLG(container_id)[0] = '\0';
	for (f = 0; files[f] && !OLG(container_id)[0]; f++) {
		FILE *fp = fopen(files[f], "r");
		if (fp == NULL) {
			continue;
		}
		while (fgets(line, sizeof(line), fp)) {
			char *p = line;
			/* look for a run of exactly 64 hex characters after "containers/", "docker-", "docker/" or "/" */
			while (*p) {
				size_t k = 0;
				while (isxdigit((unsigned char) p[k]) && !isupper((unsigned char) p[k])) k++;
				if (k == 64 && (p == line || !isxdigit((unsigned char) p[-1]))) {
					bool ok = f == 0 || (p - line > 11 && strncmp(p - 11, "containers/", 11) == 0);
					if (ok) {
						memcpy(OLG(container_id), p, 64);
						OLG(container_id)[64] = '\0';
						break;
					}
				}
				p += k ? k : 1;
			}
			if (OLG(container_id)[0]) {
				break;
			}
		}
		fclose(fp);
	}
}

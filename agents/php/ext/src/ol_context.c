/*
 * W3C trace context, sampling, request root span, process info.
 * SPDX-License-Identifier: Apache-2.0
 */
#include "ol.h"
#include "ext/standard/php_string.h"

#include <ctype.h>
#include <stdio.h>

static int ol_hex(char c)
{
	if (c >= '0' && c <= '9') return c - '0';
	if (c >= 'a' && c <= 'f') return c - 'a' + 10;
	return -1; /* upper case hex is invalid in traceparent */
}

/*
 * traceparent = version "-" trace-id "-" parent-id "-" flags. Version 00 must be exactly 55 characters; higher
 * versions may append "-..." fields. All-zero ids and version ff are invalid.
 */
bool ol_parse_traceparent(const char *tp, size_t len, uint8_t *trace_id, uint64_t *parent, uint8_t *flags)
{
	uint8_t tid[16];
	uint64_t pid = 0;
	int v0, v1, f0, f1, i;
	bool nz = false;

	while (len > 0 && (tp[0] == ' ' || tp[0] == '\t')) {
		tp++;
		len--;
	}
	while (len > 0 && (tp[len - 1] == ' ' || tp[len - 1] == '\t')) {
		len--;
	}
	if (len < 55 || tp[2] != '-' || tp[35] != '-' || tp[52] != '-') {
		return false;
	}
	v0 = ol_hex(tp[0]);
	v1 = ol_hex(tp[1]);
	if (v0 < 0 || v1 < 0 || (v0 == 15 && v1 == 15)) {
		return false;
	}
	if (v0 == 0 && v1 == 0 && len != 55) {
		return false;
	}
	if (len > 55 && tp[55] != '-') {
		return false;
	}
	for (i = 0; i < 16; i++) {
		int hi = ol_hex(tp[3 + 2 * i]), lo = ol_hex(tp[4 + 2 * i]);
		if (hi < 0 || lo < 0) return false;
		tid[i] = (uint8_t) (hi << 4 | lo);
		nz = nz || tid[i];
	}
	if (!nz) {
		return false;
	}
	for (i = 0; i < 16; i++) {
		int h = ol_hex(tp[36 + i]);
		if (h < 0) return false;
		pid = pid << 4 | (uint64_t) h;
	}
	if (pid == 0) {
		return false;
	}
	f0 = ol_hex(tp[53]);
	f1 = ol_hex(tp[54]);
	if (f0 < 0 || f1 < 0) {
		return false;
	}
	memcpy(trace_id, tid, 16);
	*parent = pid;
	*flags = (uint8_t) (f0 << 4 | f1);
	return true;
}

size_t ol_format_traceparent(char *buf, size_t cap, uint64_t span_id)
{
	static const char hex[] = "0123456789abcdef";
	char tid[33];
	int i, n;
	for (i = 0; i < 16; i++) {
		tid[2 * i] = hex[OLG(trace_id)[i] >> 4];
		tid[2 * i + 1] = hex[OLG(trace_id)[i] & 15];
	}
	tid[32] = '\0';
	n = snprintf(buf, cap, "00-%s-%016llx-%02x", tid, (unsigned long long) span_id, OLG(recording) ? 1 : (OLG(trace_flags) & 1));
	return n > 0 && (size_t) n < cap ? (size_t) n : 0;
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

static void ol_random_trace_id(void)
{
	uint64_t a = ol_rand64(), b = ol_rand64();
	int i;
	for (i = 0; i < 8; i++) {
		OLG(trace_id)[i] = (uint8_t) (a >> (56 - 8 * i));
		OLG(trace_id)[8 + i] = (uint8_t) (b >> (56 - 8 * i));
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

static void ol_root_attr_var(ol_node *root, const char *key, const char *var, size_t var_len, size_t max)
{
	char *v = ol_server_var(var, var_len);
	if (v) {
		ol_attr_str(root, key, v, strlen(v) > max ? max : strlen(v));
		efree(v);
	}
}

void ol_request_context(void)
{
	char *tp, *ts;
	bool parent_ok = false;
	uint32_t idx;
	ol_node *root;

	OLG(remote_parent) = 0;
	OLG(trace_flags) = 0;
	OLG(tracestate)[0] = '\0';

	tp = ol_server_var(OLG(is_cli) ? "TRACEPARENT" : "HTTP_TRACEPARENT", OLG(is_cli) ? 11 : 16);
	if (tp) {
		parent_ok = ol_parse_traceparent(tp, strlen(tp), OLG(trace_id), &OLG(remote_parent), &OLG(trace_flags));
		efree(tp);
	}
	if (parent_ok) {
		ts = ol_server_var(OLG(is_cli) ? "TRACESTATE" : "HTTP_TRACESTATE", OLG(is_cli) ? 10 : 15);
		if (ts) {
			size_t l = strlen(ts);
			if (l < sizeof(OLG(tracestate))) {
				memcpy(OLG(tracestate), ts, l + 1);
			}
			efree(ts);
		}
		OLG(recording) = (OLG(trace_flags) & 1) != 0; /* parent based */
		OLG(applied_ratio) = 1.0;
	} else {
		double r = OLG(sampling_ratio);
		ol_random_trace_id();
		if (r >= 1.0) {
			OLG(recording) = true;
			OLG(applied_ratio) = 1.0;
		} else if (r <= 0.0) {
			OLG(recording) = false;
		} else {
			OLG(recording) = (double) (ol_rand64() >> 11) / 9007199254740992.0 < r;
			OLG(applied_ratio) = r;
			ol_threshold_hex(r, OLG(tracestate) + 6, sizeof(OLG(tracestate)) - 6);
			memcpy(OLG(tracestate), "ot=th:", 6);
		}
		OLG(trace_flags) = OLG(recording) ? 1 : 0;
	}
	OLG(local_root_id) = ol_rand64();
	OLG(propagate) = true;
	if (!OLG(recording)) {
		/* unsampled: nothing recorded, but outgoing requests carry the (unsampled) context */
		if (parent_ok) {
			OLG(local_root_id) = OLG(remote_parent);
		}
		return;
	}

	idx = ol_node_new(OLG(is_cli) ? OL_KIND_INTERNAL : OL_KIND_SERVER, OL_NF_ROOT, OL_NONE);
	root = ol_node_at(idx);
	if (root == NULL) {
		OLG(recording) = false;
		return;
	}
	root->start = OLG(req_mono);
	OLG(local_root_id) = root->id;

	if (OLG(is_cli)) {
		ol_attr_bool(root, "openlog.php.cli", true);
		return;
	}
	if (SG(request_info).request_method) {
		ol_attr_str(root, "http.request.method", SG(request_info).request_method, strlen(SG(request_info).request_method) > 16 ? 16 : strlen(SG(request_info).request_method));
	}
	{
		/* Under FPM request_uri is the script; the client's path is REQUEST_URI. */
		char *uri = ol_server_var("REQUEST_URI", sizeof("REQUEST_URI") - 1);
		const char *u = uri ? uri : SG(request_info).request_uri;
		if (u) {
			const char *q = strchr(u, '?');
			size_t l = q ? (size_t) (q - u) : strlen(u);
			/* absolute-form request target */
			if (ol_str_starts_ci(u, l, "http://") || ol_str_starts_ci(u, l, "https://")) {
				const char *slash = memchr(u + 8, '/', l > 8 ? l - 8 : 0);
				if (slash) {
					l -= (size_t) (slash - u);
					u = slash;
				}
			}
			ol_attr_str(root, "url.path", u, l > 2048 ? 2048 : l);
		}
		if (uri) {
			efree(uri);
		}
	}
	{
		char *https = ol_server_var("HTTPS", 5);
		ol_attr_static(root, "url.scheme", (https && *https && strcasecmp(https, "off") != 0) ? "https" : "http");
		if (https) {
			efree(https);
		}
	}
	{
		char *host = ol_server_var("HTTP_HOST", 9);
		if (host) {
			size_t l = strlen(host);
			char *c = strrchr(host, ':');
			if (c && strchr(host, ']') < c) {
				zend_long port = ZEND_STRTOL(c + 1, NULL, 10);
				l = (size_t) (c - host);
				if (port > 0) {
					ol_attr_int(root, "server.port", port);
				}
			}
			ol_attr_str(root, "server.address", host, l > 256 ? 256 : l);
			efree(host);
		}
	}
	ol_root_attr_var(root, "client.address", "REMOTE_ADDR", 11, 64);
	ol_root_attr_var(root, "user_agent.original", "HTTP_USER_AGENT", 15, 512);
	{
		char *proto = ol_server_var("SERVER_PROTOCOL", 15);
		if (proto) {
			if (strncmp(proto, "HTTP/", 5) == 0 && proto[5]) {
				ol_attr_str(root, "network.protocol.version", proto + 5, strlen(proto + 5) > 8 ? 8 : strlen(proto + 5));
			}
			efree(proto);
		}
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

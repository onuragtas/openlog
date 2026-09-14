/*
 * libFuzzer target for the PHP-independent text code (src/ol_text.c): UTF-8 cleaning + truncation (every span
 * attribute and resource value goes through it), the JSON string / number writer, SQL sanitizing and operation
 * extraction, path and route normalization, traceparent parsing.
 *
 * Input: byte 0 selects the function, byte 1 is a parameter (caps / limits), the rest is the text.
 * Invariants (abort on violation, besides ASan/UBSan): outputs stay inside their buffers and are NUL terminated,
 * cleaned text is valid UTF-8 and at most `max` bytes, JSON strings decode back to exactly their input, a writer
 * with a too small capacity stops cleanly, numbers round-trip, parsed traceparents re-parse identically.
 *
 * Build/run: ext/fuzz/run.sh (clang, -fsanitize=fuzzer,address,undefined).
 * SPDX-License-Identifier: Apache-2.0
 */
#include "ol_text.h"

#include <inttypes.h>
#include <stdio.h>
#include <stdlib.h>

#define CHECK(c) do { if (!(c)) { fprintf(stderr, "invariant failed: %s (%s:%d)\n", #c, __FILE__, __LINE__); abort(); } } while (0)
#define STR_MAX 4096

static bool valid_utf8(const unsigned char *s, size_t n)
{
	size_t i = 0;
	while (i < n) {
		unsigned char c = s[i];
		size_t need, k;
		unsigned char lo = 0x80, hi = 0xBF;
		if (c < 0x80) { i++; continue; }
		if (c >= 0xC2 && c <= 0xDF) need = 1;
		else if (c == 0xE0) { need = 2; lo = 0xA0; }
		else if (c == 0xED) { need = 2; hi = 0x9F; }
		else if (c >= 0xE1 && c <= 0xEF) need = 2;
		else if (c == 0xF0) { need = 3; lo = 0x90; }
		else if (c == 0xF4) { need = 3; hi = 0x8F; }
		else if (c >= 0xF1 && c <= 0xF3) need = 3;
		else return false;
		if (i + need >= n + 0 && i + need > n - 1) return false;
		if (s[i + 1] < lo || s[i + 1] > hi) return false;
		for (k = 2; k <= need; k++) {
			if ((s[i + k] & 0xC0) != 0x80) return false;
		}
		i += need + 1;
	}
	return true;
}

static int hexval(char c)
{
	if (c >= '0' && c <= '9') return c - '0';
	if (c >= 'a' && c <= 'f') return c - 'a' + 10;
	if (c >= 'A' && c <= 'F') return c - 'A' + 10;
	return -1;
}

/* Strict decoder for the JSON strings the writer produces; returns the decoded length or SIZE_MAX. */
static size_t json_decode(const char *p, size_t n, char *out)
{
	size_t i, o = 0;
	if (n < 2 || p[0] != '"' || p[n - 1] != '"') return SIZE_MAX;
	for (i = 1; i + 1 < n; i++) {
		unsigned char c = (unsigned char) p[i];
		if (c == '"' || c < 0x20) return SIZE_MAX;
		if (c != '\\') { out[o++] = (char) c; continue; }
		if (i + 2 >= n) return SIZE_MAX;
		c = (unsigned char) p[++i];
		switch (c) {
			case '"': out[o++] = '"'; break;
			case '\\': out[o++] = '\\'; break;
			case '/': out[o++] = '/'; break;
			case 'b': out[o++] = '\b'; break;
			case 'f': out[o++] = '\f'; break;
			case 'n': out[o++] = '\n'; break;
			case 'r': out[o++] = '\r'; break;
			case 't': out[o++] = '\t'; break;
			case 'u': {
				int v = 0, k;
				if (i + 5 >= n) return SIZE_MAX;
				for (k = 1; k <= 4; k++) {
					int h = hexval(p[i + k]);
					if (h < 0) return SIZE_MAX;
					v = v * 16 + h;
				}
				if (v >= 0x80) return SIZE_MAX; /* the writer only escapes ASCII control characters */
				out[o++] = (char) v;
				i += 4;
				break;
			}
			default: return SIZE_MAX;
		}
	}
	return o;
}

static void fuzz_clean_json(const char *s, size_t n, uint8_t param)
{
	size_t max = (size_t) param * 41u % (STR_MAX + 1), cl, cap, cap2, dl;
	char *clean = malloc(max + 1), *out, *out2, *dec;
	ol_w w, w2;

	if (n <= STR_MAX && (param & 0x80)) {
		max = n; /* also exercise "no truncation" */
		free(clean);
		clean = malloc(max + 1);
	}
	cl = ol_utf8_clean(clean, s, n, max);
	CHECK(cl <= max);
	CHECK(valid_utf8((const unsigned char *) clean, cl));
	if (n <= max && valid_utf8((const unsigned char *) s, n)) {
		CHECK(cl == n && memcmp(clean, s, n) == 0);
	}

	cap = cl * 6 + 2;
	out = malloc(cap);
	w.p = out; w.len = 0; w.cap = cap; w.ok = true;
	ol_w_strn(&w, clean, cl);
	CHECK(w.ok && w.len <= cap);
	dec = malloc(w.len + 1);
	dl = json_decode(out, w.len, dec);
	CHECK(dl == cl && memcmp(dec, clean, cl) == 0);

	cap2 = cap ? (size_t) param * 7u % cap : 0;
	out2 = malloc(cap2 ? cap2 : 1);
	w2.p = out2; w2.len = 0; w2.cap = cap2; w2.ok = true;
	ol_w_strn(&w2, clean, cl);
	CHECK(w2.len <= cap2);
	if (w2.ok) {
		CHECK(w2.len == w.len && memcmp(out2, out, w.len) == 0);
	}
	free(clean); free(out); free(out2); free(dec);
}

static void fuzz_numbers(const char *s, size_t n)
{
	uint64_t u = 0;
	char buf[64], chk[64];
	ol_w w;
	memcpy(&u, s, n < 8 ? n : 8);
	w.p = buf; w.len = 0; w.cap = sizeof(buf); w.ok = true;
	ol_w_u64(&w, u);
	CHECK(w.ok);
	snprintf(chk, sizeof(chk), "%" PRIu64, u);
	CHECK(w.len == strlen(chk) && memcmp(buf, chk, w.len) == 0);
	w.len = 0;
	ol_w_i64(&w, (int64_t) u);
	snprintf(chk, sizeof(chk), "%" PRId64, (int64_t) u);
	CHECK(w.ok && w.len == strlen(chk) && memcmp(buf, chk, w.len) == 0);
	w.len = 0;
	ol_w_id(&w, u);
	if (u == 0) {
		CHECK(w.len == 2);
	} else {
		snprintf(chk, sizeof(chk), "\"%016" PRIx64 "\"", u);
		CHECK(w.len == 18 && memcmp(buf, chk, 18) == 0);
	}
	if (n >= 8) {
		double d;
		memcpy(&d, s, 8);
		w.len = 0;
		ol_w_dbl(&w, d);
		CHECK(w.ok && w.len > 0 && w.len < sizeof(buf));
	}
}

int LLVMFuzzerTestOneInput(const uint8_t *data, size_t size)
{
	uint8_t sel, param;
	const char *s;
	size_t n;

	if (size < 2) {
		return 0;
	}
	sel = data[0];
	param = data[1];
	s = (const char *) data + 2;
	n = size - 2;

	switch (sel % 7) {
		case 0:
			fuzz_clean_json(s, n, param);
			break;
		case 1: {
			size_t cap = 1 + (size_t) param * 23u % (STR_MAX + 1), o;
			char *dst = malloc(cap);
			o = ol_sql_sanitize(dst, cap, s, n, param & 1);
			CHECK(o < cap && dst[o] == '\0');
			free(dst);
			break;
		}
		case 2: {
			size_t opcap = 1 + param % 40, collcap = 1 + (size_t) (param * 13u) % 140, r;
			char *op = malloc(opcap), *coll = malloc(collcap);
			r = ol_sql_operation(s, n, op, opcap, coll, collcap);
			CHECK(r < opcap && op[r] == '\0' || r == 0);
			CHECK(memchr(op, '\0', opcap) != NULL);
			CHECK(memchr(coll, '\0', collcap) != NULL);
			free(op); free(coll);
			break;
		}
		case 3: {
			size_t cap = 16 + (size_t) param * 5u % 1100, o;
			char *dst = malloc(cap);
			o = ol_normalize_path(dst, cap, s, n);
			CHECK(o > 0 && o < cap && dst[o] == '\0' && dst[0] == '/');
			free(dst);
			break;
		}
		case 4: {
			size_t cap = 3 + (size_t) param * 7u % 1100, o;
			char *dst = malloc(cap);
			o = ol_route_from_pattern(dst, cap, s, n);
			CHECK(o < cap && dst[o] == '\0');
			free(dst);
			break;
		}
		case 5: {
			uint8_t tid[16], tid2[16], flags = 0, flags2 = 0;
			uint64_t parent = 0, parent2 = 0;
			if (ol_parse_traceparent(s, n, tid, &parent, &flags)) {
				char tp[56];
				int i;
				CHECK(parent != 0);
				snprintf(tp, sizeof(tp), "00-");
				for (i = 0; i < 16; i++) {
					snprintf(tp + 3 + 2 * i, 3, "%02x", tid[i]);
				}
				snprintf(tp + 35, sizeof(tp) - 35, "-%016" PRIx64 "-%02x", parent, flags);
				CHECK(ol_parse_traceparent(tp, 55, tid2, &parent2, &flags2));
				CHECK(memcmp(tid, tid2, 16) == 0 && parent == parent2 && flags == flags2);
			}
			break;
		}
		default:
			fuzz_numbers(s, n);
			break;
	}
	return 0;
}

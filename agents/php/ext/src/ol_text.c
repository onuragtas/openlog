/*
 * Text helpers without PHP dependencies (see ol_text.h): used on every span, so the common cases (ASCII, nothing to
 * escape) are handled 8 bytes at a time and character classes come from one table instead of the locale functions.
 * SPDX-License-Identifier: Apache-2.0
 */
#include "ol_text.h"

#include <stdio.h>

#define OL_CT_NUMHEX (OL_CT_DIGIT | OL_CT_XDIGIT | OL_CT_IDENT)
#define OL_CT_UPHEX  (OL_CT_ALPHA | OL_CT_XDIGIT | OL_CT_IDENT)
#define OL_CT_LETTER (OL_CT_ALPHA | OL_CT_IDENT)

const unsigned char ol_ctype[256] = {
	[0x00 ... 0x08] = OL_CT_JSONESC,
	[0x09 ... 0x0d] = OL_CT_SPACE | OL_CT_JSONESC,
	[0x0e ... 0x1f] = OL_CT_JSONESC,
	[' '] = OL_CT_SPACE,
	['"'] = OL_CT_JSONESC,
	['$'] = OL_CT_IDENT,
	['0' ... '9'] = OL_CT_NUMHEX,
	[':'] = OL_CT_IDENT,
	['@'] = OL_CT_IDENT,
	['A' ... 'F'] = OL_CT_UPHEX,
	['G' ... 'Z'] = OL_CT_LETTER,
	['\\'] = OL_CT_JSONESC,
	['_'] = OL_CT_IDENT,
	['a' ... 'f'] = OL_CT_UPHEX,
	['g' ... 'z'] = OL_CT_LETTER,
	[0x7f] = OL_CT_JSONESC,
	[0x80 ... 0xff] = OL_CT_IDENT,
};

#define OL_REP8(b) (0x0101010101010101ULL * (uint64_t) (b))
#define OL_HASZERO8(v) (((v) - OL_REP8(0x01)) & ~(v) & OL_REP8(0x80))

static inline uint64_t ol_load8(const char *p)
{
	uint64_t v;
	memcpy(&v, p, 8);
	return v;
}

bool ol_ascii_ncaseeq(const char *a, const char *b, size_t n)
{
	size_t i;
	for (i = 0; i < n; i++) {
		char x = OL_TOUPPER(a[i]), y = OL_TOUPPER(b[i]);
		if (x != y) {
			return false;
		}
	}
	return true;
}

bool ol_str_starts_ci(const char *s, size_t len, const char *prefix)
{
	size_t pl = strlen(prefix);
	return len >= pl && ol_ascii_ncaseeq(s, prefix, pl);
}

/* ---------------- UTF-8 ---------------- */

size_t ol_utf8_clean(char *dst, const char *src, size_t len, size_t max)
{
	size_t i = 0, o = 0;
	if (len > max) {
		len = max;
	}
	while (i < len) {
		unsigned char c;
		size_t need;
		if (i + 8 <= len && !(ol_load8(src + i) & OL_REP8(0x80))) {
			memcpy(dst + o, src + i, 8);
			o += 8;
			i += 8;
			continue;
		}
		c = (unsigned char) src[i];
		if (c < 0x80) {
			dst[o++] = (char) c;
			i++;
			continue;
		}
		if (c >= 0xC2 && c <= 0xDF) {
			need = 1;
		} else if (c >= 0xE0 && c <= 0xEF) {
			need = 2;
		} else if (c >= 0xF0 && c <= 0xF4) {
			need = 3;
		} else {
			dst[o++] = '?';
			i++;
			continue;
		}
		if (i + need >= len) {
			break; /* sequence runs past the (possibly truncated) end */
		}
		{
			size_t k;
			bool ok = true;
			unsigned char c1 = (unsigned char) src[i + 1];
			for (k = 1; k <= need; k++) {
				if (((unsigned char) src[i + k] & 0xC0) != 0x80) {
					ok = false;
					break;
				}
			}
			if (ok && need == 2 && ((c == 0xE0 && c1 < 0xA0) || (c == 0xED && c1 > 0x9F))) {
				ok = false; /* overlong / surrogate */
			}
			if (ok && need == 3 && ((c == 0xF0 && c1 < 0x90) || (c == 0xF4 && c1 > 0x8F))) {
				ok = false;
			}
			if (!ok) {
				dst[o++] = '?';
				i++;
				continue;
			}
			memcpy(dst + o, src + i, need + 1);
			o += need + 1;
			i += need + 1;
		}
	}
	return o;
}

/* ---------------- JSON writer ---------------- */

static inline uint64_t ol_json_special8(uint64_t v)
{
	uint64_t q = v ^ OL_REP8('"'), b = v ^ OL_REP8('\\'), d = v ^ OL_REP8(0x7f);
	uint64_t lt = (v - OL_REP8(0x20)) & ~v & OL_REP8(0x80); /* a byte < 0x20 */
	return lt | OL_HASZERO8(q) | OL_HASZERO8(b) | OL_HASZERO8(d);
}

void ol_w_strn(ol_w *w, const char *s, size_t n)
{
	static const char hex[] = "0123456789abcdef";
	size_t i = 0, run = 0;

	OL_W_LIT(w, "\"");
	while (i < n) {
		unsigned char c;
		if (i + 8 <= n && !ol_json_special8(ol_load8(s + i))) {
			i += 8;
			continue;
		}
		c = (unsigned char) s[i];
		if (!(ol_ctype[c] & OL_CT_JSONESC)) {
			i++;
			continue;
		}
		if (i > run) {
			ol_w_raw(w, s + run, i - run);
		}
		if (c == '"' || c == '\\') {
			char e[2] = {'\\', (char) c};
			ol_w_raw(w, e, 2);
		} else if (c == '\n') {
			OL_W_LIT(w, "\\n");
		} else if (c == '\t') {
			OL_W_LIT(w, "\\t");
		} else if (c == '\r') {
			OL_W_LIT(w, "\\r");
		} else {
			char e[6] = {'\\', 'u', '0', '0', hex[c >> 4], hex[c & 15]};
			ol_w_raw(w, e, 6);
		}
		run = ++i;
		if (!w->ok) {
			return;
		}
	}
	if (n > run) {
		ol_w_raw(w, s + run, n - run);
	}
	OL_W_LIT(w, "\"");
}

void ol_w_key(ol_w *w, const char *key)
{
	size_t kl = strlen(key);
	if (!w->ok || w->len + kl + 3 > w->cap) {
		w->ok = false;
		return;
	}
	w->p[w->len] = '"';
	memcpy(w->p + w->len + 1, key, kl);
	w->p[w->len + 1 + kl] = '"';
	w->p[w->len + 2 + kl] = ':';
	w->len += kl + 3;
}

void ol_w_u64(ol_w *w, uint64_t v)
{
	char b[20];
	size_t i = sizeof(b);
	do {
		b[--i] = (char) ('0' + v % 10);
		v /= 10;
	} while (v);
	ol_w_raw(w, b + i, sizeof(b) - i);
}

void ol_w_i64(ol_w *w, int64_t v)
{
	if (v < 0) {
		OL_W_LIT(w, "-");
		ol_w_u64(w, (uint64_t) 0 - (uint64_t) v);
	} else {
		ol_w_u64(w, (uint64_t) v);
	}
}

void ol_w_dbl(ol_w *w, double v)
{
	char b[40];
	int n;
	if (v != v || v > 1.7976931348623157e308 || v < -1.7976931348623157e308) {
		OL_W_LIT(w, "0.0");
		return;
	}
	n = snprintf(b, sizeof(b), "%.17g", v);
	if (n <= 0 || (size_t) n >= sizeof(b)) {
		OL_W_LIT(w, "0.0");
		return;
	}
	ol_w_raw(w, b, (size_t) n);
	if (!memchr(b, '.', (size_t) n) && !memchr(b, 'e', (size_t) n) && !memchr(b, 'n', (size_t) n)) {
		OL_W_LIT(w, ".0");
	}
}

void ol_w_id(ol_w *w, uint64_t id)
{
	static const char hex[] = "0123456789abcdef";
	char b[18];
	int i;
	if (id == 0) {
		OL_W_LIT(w, "\"\"");
		return;
	}
	b[0] = '"';
	for (i = 0; i < 16; i++) {
		b[1 + i] = hex[(id >> (60 - 4 * i)) & 15];
	}
	b[17] = '"';
	ol_w_raw(w, b, sizeof(b));
}

/* ---------------- W3C traceparent ---------------- */

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

/* ---------------- SQL ---------------- */

/*
 * Replaces literals with '?': quoted strings ('' and backslash escapes; "..." only when double_quote_strings, i.e.
 * MySQL), dollar-quoted strings, numbers and hex literals. Removes comments, collapses whitespace. Placeholders
 * ($1, :name, ?) stay. Output is NUL terminated, at most cap - 1 bytes.
 */
size_t ol_sql_sanitize(char *dst, size_t cap, const char *s, size_t n, bool dq)
{
	size_t i = 0, o = 0;
	bool space = false;

#define OL_PUT(ch) do { if (o + 1 < cap) { dst[o++] = (char) (ch); } } while (0)
#define OL_FLUSH_SPACE() do { if (space && o > 0) { OL_PUT(' '); } space = false; } while (0)

	if (cap == 0) {
		return 0;
	}
	while (i < n && o + 8 < cap) {
		unsigned char c = (unsigned char) s[i];
		if (c == '-' && i + 1 < n && s[i + 1] == '-') {
			while (i < n && s[i] != '\n') i++;
			space = true;
			continue;
		}
		if (c == '#' && dq) {
			while (i < n && s[i] != '\n') i++;
			space = true;
			continue;
		}
		if (c == '/' && i + 1 < n && s[i + 1] == '*') {
			i += 2;
			while (i + 1 < n && !(s[i] == '*' && s[i + 1] == '/')) i++;
			i += 2;
			space = true;
			continue;
		}
		if (OL_ISSPACE(c)) {
			space = true;
			i++;
			continue;
		}
		OL_FLUSH_SPACE();
		if (c == '\'' || (dq && c == '"')) {
			char q = (char) c;
			/* E'...' / N'...' / X'...' prefixes */
			if (o > 0 && (dst[o - 1] == 'E' || dst[o - 1] == 'e' || dst[o - 1] == 'N' || dst[o - 1] == 'n' ||
					dst[o - 1] == 'X' || dst[o - 1] == 'x' || dst[o - 1] == 'B' || dst[o - 1] == 'b') &&
					(o == 1 || !OL_ISIDENT(dst[o - 2]))) {
				o--;
			}
			i++;
			while (i < n) {
				if (s[i] == '\\' && i + 1 < n) {
					i += 2;
					continue;
				}
				if (s[i] == q) {
					if (i + 1 < n && s[i + 1] == q) {
						i += 2;
						continue;
					}
					i++;
					break;
				}
				i++;
			}
			OL_PUT('?');
			continue;
		}
		if (c == '$') {
			if (i + 1 < n && OL_ISDIGIT(s[i + 1])) { /* $1 placeholder */
				OL_PUT('$');
				i++;
				while (i < n && OL_ISDIGIT(s[i])) {
					OL_PUT(s[i]);
					i++;
				}
				continue;
			}
			/* $tag$ ... $tag$ */
			{
				size_t j = i + 1;
				while (j < n && (OL_ISALNUM(s[j]) || s[j] == '_')) j++;
				if (j < n && s[j] == '$' && (o == 0 || !OL_ISIDENT(dst[o - 1]))) {
					size_t taglen = j - i + 1, k = j + 1;
					bool found = false;
					while (k + taglen <= n) {
						if (memcmp(s + k, s + i, taglen) == 0) {
							found = true;
							break;
						}
						k++;
					}
					i = found ? k + taglen : n;
					OL_PUT('?');
					continue;
				}
			}
		}
		if (OL_ISDIGIT(c) && (o == 0 || !OL_ISIDENT(dst[o - 1]))) {
			if (c == '0' && i + 1 < n && (s[i + 1] == 'x' || s[i + 1] == 'X')) {
				i += 2;
				while (i < n && OL_ISXDIGIT(s[i])) i++;
			} else {
				while (i < n && OL_ISDIGIT(s[i])) i++;
				if (i < n && s[i] == '.') {
					i++;
					while (i < n && OL_ISDIGIT(s[i])) i++;
				}
				if (i < n && (s[i] == 'e' || s[i] == 'E')) {
					size_t j = i + 1;
					if (j < n && (s[j] == '+' || s[j] == '-')) j++;
					if (j < n && OL_ISDIGIT(s[j])) {
						i = j;
						while (i < n && OL_ISDIGIT(s[i])) i++;
					}
				}
			}
			OL_PUT('?');
			continue;
		}
		OL_PUT(c);
		i++;
	}
	if (i < n && o + 4 < cap) {
		/* truncated */
		dst[o++] = '.';
		dst[o++] = '.';
		dst[o++] = '.';
	}
	dst[o] = '\0';
	return o;
#undef OL_PUT
#undef OL_FLUSH_SPACE
}

static size_t ol_read_word(const char *s, size_t n, size_t *pos, char *out, size_t cap, bool ident)
{
	size_t i = *pos, k = 0;
	while (i < n && (OL_ISSPACE(s[i]) || s[i] == '(')) i++;
	while (i < n && k + 1 < cap) {
		char c = s[i];
		if (ident ? (OL_ISALNUM(c) || c == '_' || c == '.' || c == '`' || c == '"' || c == '[' || c == ']')
				: OL_ISALPHA(c)) {
			if (c != '`' && c != '"' && c != '[' && c != ']') {
				out[k++] = ident ? c : OL_TOUPPER(c);
			}
			i++;
		} else {
			break;
		}
	}
	out[k] = '\0';
	*pos = i;
	return k;
}

/* First keyword (upper case) and, for simple statements, the table ("collection"). */
size_t ol_sql_operation(const char *sql, size_t len, char *op, size_t opcap, char *coll, size_t collcap)
{
	size_t pos = 0, oplen;
	char w[32];
	coll[0] = '\0';
	/* skip leading comments */
	while (pos < len) {
		while (pos < len && OL_ISSPACE(sql[pos])) pos++;
		if (pos + 1 < len && sql[pos] == '/' && sql[pos + 1] == '*') {
			pos += 2;
			while (pos + 1 < len && !(sql[pos] == '*' && sql[pos + 1] == '/')) pos++;
			pos += 2;
		} else if (pos + 1 < len && sql[pos] == '-' && sql[pos + 1] == '-') {
			while (pos < len && sql[pos] != '\n') pos++;
		} else {
			break;
		}
	}
	oplen = ol_read_word(sql, len, &pos, op, opcap, false);
	if (oplen == 0) {
		return 0;
	}
	if (strcmp(op, "SELECT") == 0 || strcmp(op, "DELETE") == 0) {
		/* find FROM at nesting level 0 */
		int depth = 0;
		size_t i = pos;
		while (i < len) {
			char c = sql[i];
			if (c == '(') depth++;
			else if (c == ')') depth--;
			else if (c == '\'' ) { i++; while (i < len && sql[i] != '\'') i++; }
			else if (depth == 0 && (c == 'f' || c == 'F') && i + 4 < len && (i == 0 || !OL_ISIDENT(sql[i - 1])) &&
					ol_ascii_ncaseeq(sql + i, "from", 4) && OL_ISSPACE(sql[i + 4])) {
				size_t p = i + 4;
				ol_read_word(sql, len, &p, coll, collcap, true);
				break;
			}
			i++;
		}
	} else if (strcmp(op, "INSERT") == 0 || strcmp(op, "REPLACE") == 0) {
		size_t p = pos;
		ol_read_word(sql, len, &p, w, sizeof(w), false);
		if (strcmp(w, "INTO") == 0) {
			ol_read_word(sql, len, &p, coll, collcap, true);
		} else if (strcmp(w, "IGNORE") == 0) {
			ol_read_word(sql, len, &p, w, sizeof(w), false);
			ol_read_word(sql, len, &p, coll, collcap, true);
		}
	} else if (strcmp(op, "UPDATE") == 0) {
		size_t p = pos;
		ol_read_word(sql, len, &p, coll, collcap, true);
	}
	return oplen;
}

/* ---------------- paths / routes ---------------- */

static bool ol_seg_all(const char *s, size_t n, unsigned char mask)
{
	size_t i;
	for (i = 0; i < n; i++) {
		if (!(ol_ctype[(unsigned char) s[i]] & mask)) return false;
	}
	return n > 0;
}

static const char *ol_seg_replacement(const char *s, size_t n)
{
	size_t i, digits = 0, hexletters = 0;
	bool allhex = true, token = true, email_at = false;

	/* UUID */
	if (n == 36 && s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-') {
		bool ok = true;
		for (i = 0; i < 36 && ok; i++) {
			if (i == 8 || i == 13 || i == 18 || i == 23) continue;
			ok = OL_ISXDIGIT(s[i]);
		}
		if (ok) return "{uuid}";
	}
	/* decimal, optionally signed */
	if ((s[0] == '-' || s[0] == '+') && n > 1 && ol_seg_all(s + 1, n - 1, OL_CT_DIGIT)) return "{id}";
	if (ol_seg_all(s, n, OL_CT_DIGIT)) return "{id}";
	for (i = 0; i < n; i++) {
		unsigned char c = (unsigned char) s[i];
		if (OL_ISDIGIT(c)) digits++;
		if (!OL_ISXDIGIT(c)) allhex = false;
		else if (!OL_ISDIGIT(c)) hexletters++;
		if (!(OL_ISALNUM(c) || c == '_' || c == '-')) token = false;
		if (c == '@') email_at = true;
	}
	if (allhex && (n >= 16 || (n >= 8 && digits > 0 && hexletters > 0))) return "{hex}";
	if (token && n >= 20 && digits > 0) return "{token}";
	if (email_at) {
		const char *at = memchr(s, '@', n);
		if (at && at > s && memchr(at, '.', n - (size_t) (at - s)) != NULL) return "{email}";
	}
	return NULL;
}

/* apm.md §2.2 path normalization. */
size_t ol_normalize_path(char *dst, size_t cap, const char *path, size_t len)
{
	size_t i = 0, o = 0;
	int segs = 0;
	const char *q = memchr(path, '?', len);
	const char *h = memchr(path, '#', len);
	if (q) len = (size_t) (q - path);
	if (h && (size_t) (h - path) < len) len = (size_t) (h - path);

	while (i < len && o + 8 < cap) {
		size_t start, n;
		const char *rep;
		while (i < len && path[i] == '/') i++;
		if (i >= len) break;
		start = i;
		while (i < len && path[i] != '/') i++;
		n = i - start;
		if (++segs > 8) {
			memcpy(dst + o, "/\xE2\x80\xA6", 4); /* "/…" */
			o += 4;
			break;
		}
		dst[o++] = '/';
		rep = ol_seg_replacement(path + start, n);
		if (rep) {
			size_t rl = strlen(rep);
			if (o + rl + 1 >= cap) break;
			memcpy(dst + o, rep, rl);
			o += rl;
		} else {
			if (o + n + 1 >= cap) n = cap - o - 2;
			memcpy(dst + o, path + start, n);
			o += n;
		}
	}
	if (o == 0) {
		dst[o++] = '/';
	}
	dst[o] = '\0';
	return o;
}

/*
 * Route template from a regex/placeholder pattern: "(?P<id>\d+)" -> "{id}", CodeIgniter "(:num)" -> "{num}",
 * other groups -> "{param}"; anchors dropped.
 */
size_t ol_route_from_pattern(char *dst, size_t cap, const char *s, size_t n)
{
	size_t i = 0, o = 0;
	while (i < n && o + 2 < cap) {
		char c = s[i];
		if (c == '(') {
			size_t j = i + 1;
			int depth = 1;
			const char *name = "param";
			size_t nlen = 5;
			while (j < n && depth > 0) {
				if (s[j] == '\\') { j += 2; continue; }
				if (s[j] == '(') depth++;
				else if (s[j] == ')') depth--;
				j++;
			}
			if (i + 3 < n && s[i + 1] == '?' && (s[i + 2] == 'P' || s[i + 2] == '<')) {
				size_t k = i + (s[i + 2] == 'P' ? 4 : 3);
				size_t e = k;
				while (e < n && s[e] != '>') e++;
				if (e < n && e > k) {
					name = s + k;
					nlen = e - k;
				}
			} else if (i + 1 < n && s[i + 1] == ':') {
				size_t k = i + 2, e = k;
				while (e < n && s[e] != ')') e++;
				if (e > k) {
					name = s + k;
					nlen = e - k;
				}
			}
			if (o + nlen + 3 < cap) {
				dst[o++] = '{';
				memcpy(dst + o, name, nlen);
				o += nlen;
				dst[o++] = '}';
			}
			i = j;
			if (i < n && (s[i] == '?' || s[i] == '+' || s[i] == '*')) i++;
			continue;
		}
		if (c == '^' || c == '$') { i++; continue; }
		if (c == '\\' && i + 1 < n) { dst[o++] = s[i + 1]; i += 2; continue; }
		if (c == '/' && o > 0 && dst[o - 1] == '/') { i++; continue; }
		dst[o++] = c;
		i++;
	}
	dst[o] = '\0';
	return o;
}

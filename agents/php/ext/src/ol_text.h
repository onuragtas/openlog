/*
 * openlog PHP agent: text helpers without any PHP/Zend dependency — UTF-8 cleaning, the JSON writer, SQL
 * sanitizing, path/route normalization, traceparent parsing. Built into openlog.so and, standalone, into the fuzz
 * targets (../fuzz). ASCII only (independent of the C locale).
 * SPDX-License-Identifier: Apache-2.0
 */
#ifndef OL_TEXT_H
#define OL_TEXT_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <string.h>

#define OL_CT_SPACE   0x01 /* \t \n \v \f \r and space */
#define OL_CT_DIGIT   0x02
#define OL_CT_ALPHA   0x04
#define OL_CT_XDIGIT  0x08
#define OL_CT_IDENT   0x10 /* SQL identifier character: alnum _ $ @ : or >= 0x80 */
#define OL_CT_JSONESC 0x20 /* escaped inside a JSON string: " \ < 0x20 0x7f */

extern const unsigned char ol_ctype[256];

#define OL_CT(c, m)     ((ol_ctype[(unsigned char) (c)] & (m)) != 0)
#define OL_ISSPACE(c)   OL_CT(c, OL_CT_SPACE)
#define OL_ISDIGIT(c)   OL_CT(c, OL_CT_DIGIT)
#define OL_ISALPHA(c)   OL_CT(c, OL_CT_ALPHA)
#define OL_ISALNUM(c)   OL_CT(c, OL_CT_ALPHA | OL_CT_DIGIT)
#define OL_ISXDIGIT(c)  OL_CT(c, OL_CT_XDIGIT)
#define OL_ISIDENT(c)   OL_CT(c, OL_CT_IDENT)
#define OL_TOUPPER(c)   ((char) (((c) >= 'a' && (c) <= 'z') ? (c) - 32 : (c)))

bool ol_ascii_ncaseeq(const char *a, const char *b, size_t n);
bool ol_str_starts_ci(const char *s, size_t len, const char *prefix);

/* Copies at most max bytes of src replacing invalid UTF-8 bytes with '?'; never cuts a character in half. */
size_t ol_utf8_clean(char *dst, const char *src, size_t len, size_t max);
size_t ol_sql_sanitize(char *dst, size_t cap, const char *sql, size_t len, bool double_quote_strings);
size_t ol_sql_operation(const char *sql, size_t len, char *op, size_t opcap, char *coll, size_t collcap);
size_t ol_normalize_path(char *dst, size_t cap, const char *path, size_t len);
size_t ol_route_from_pattern(char *dst, size_t cap, const char *pat, size_t len);
bool ol_parse_traceparent(const char *tp, size_t len, uint8_t *trace_id, uint64_t *parent, uint8_t *flags);

/* JSON writer into a fixed buffer: ok turns false (and stays false) when cap would be exceeded. */
typedef struct {
	char *p;
	size_t len;
	size_t cap;
	bool ok;
} ol_w;

static inline void ol_w_raw(ol_w *w, const char *s, size_t n)
{
	if (!w->ok || w->len + n > w->cap) {
		w->ok = false;
		return;
	}
	memcpy(w->p + w->len, s, n);
	w->len += n;
}

#define OL_W_LIT(w, lit) ol_w_raw((w), (lit), sizeof(lit) - 1)

void ol_w_strn(ol_w *w, const char *s, size_t n); /* quoted, escaped; s must be valid UTF-8 */
void ol_w_key(ol_w *w, const char *key);          /* "key": — trusted literal keys only (not escaped) */
void ol_w_u64(ol_w *w, uint64_t v);
void ol_w_i64(ol_w *w, int64_t v);
void ol_w_dbl(ol_w *w, double v);
void ol_w_id(ol_w *w, uint64_t id);               /* "016x" or "" for 0 */

#endif

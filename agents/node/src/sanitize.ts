// DB statement sanitization, a port of the Go agent's openlogsql.Sanitize (agents/go/openlogsql/sanitize.go)
// so both agents send the same db.query.text for the same statement (apm.md §7 normalizes again on the backend).

/** Upper bound of db.query.text (bytes of UTF-16 code units; statements are ASCII in practice), like the Go agent. */
export const MAX_QUERY_TEXT = 4096;

const isDigit = (c: number): boolean => c >= 48 && c <= 57;
const isHex = (c: number): boolean => isDigit(c) || (c >= 97 && c <= 102) || (c >= 65 && c <= 70);
const isIdentStart = (c: number): boolean => c === 95 || (c >= 97 && c <= 122) || (c >= 65 && c <= 90);
const isIdent = (c: number): boolean => isIdentStart(c) || isDigit(c) || c === 36;
const isNonASCII = (c: number): boolean => c >= 128;

function identBefore(q: string, i: number): boolean {
  if (i === 0) return false;
  const p = q.charCodeAt(i - 1);
  return isIdent(p) || isNonASCII(p);
}

/**
 * Normalizes a SQL statement so it carries no literal values and statements differing only in values group
 * together: string literals ('…', E'…', and "…" for MySQL) → ?; numeric and hex literals → ? (identifiers such as
 * t1 or col_2 are kept); IN (?, ?) and VALUES (?), (?) lists collapse to (?); comments are removed and whitespace
 * is collapsed. Placeholders ($1, ?, :name, @p1) are kept. `system` is the db.system.name value; it only changes
 * how double-quoted text (identifier except for mysql) and `#` comments (mysql) are treated.
 */
export function sanitizeSQL(q: string, system = ''): string {
  const mysql = system === 'mysql' || system === 'mariadb';
  const out: string[] = [];
  let len = 0;
  let space = false;
  const emit = (s: string): void => {
    if (space && len > 0) {
      out.push(' ');
      len++;
    }
    space = false;
    out.push(s);
    len += s.length;
  };
  const n = q.length;
  let i = 0;
  while (i < n) {
    const c = q.charCodeAt(i);
    const next = i + 1 < n ? q.charCodeAt(i + 1) : -1;
    if (c === 32 || c === 9 || c === 10 || c === 13 || c === 12) {
      space = true;
      i++;
    } else if (c === 45 /* - */ && next === 45) {
      while (i < n && q.charCodeAt(i) !== 10) i++;
      space = true;
    } else if (c === 35 /* # */ && mysql) {
      while (i < n && q.charCodeAt(i) !== 10) i++;
      space = true;
    } else if (c === 47 /* / */ && next === 42 /* * */) {
      const j = q.indexOf('*/', i + 2);
      i = j < 0 ? n : j + 2;
      space = true;
    } else if (
      c === 39 ||
      (c === 34 && mysql) ||
      ('EeNnXxBb'.includes(q[i]) && next === 39 && !identBefore(q, i))
    ) {
      const prefix = c !== 39 && c !== 34 ? q[i] : '';
      if (prefix) i++;
      const quote = q.charCodeAt(i);
      i++;
      while (i < n) {
        const d = q.charCodeAt(i);
        if (d === 92 /* \ */ && i + 1 < n && quote === 39 && (prefix === 'E' || prefix === 'e' || mysql)) {
          i += 2;
          continue;
        }
        if (d === quote) {
          if (i + 1 < n && q.charCodeAt(i + 1) === quote) {
            i += 2;
            continue;
          }
          i++;
          break;
        }
        i++;
      }
      emit('?');
    } else if (c === 36 /* $ */ && next === 36) {
      const j = q.indexOf('$$', i + 2);
      i = j < 0 ? n : j + 2;
      emit('?');
    } else if (c === 34 || c === 96 || c === 91) {
      const closer = c === 91 ? ']' : q[i];
      const j = q.indexOf(closer, i + 1);
      if (j < 0) {
        emit(q.slice(i));
        i = n;
      } else {
        emit(q.slice(i, j + 1));
        i = j + 1;
      }
    } else if (isDigit(c) || (c === 46 && next >= 0 && isDigit(next))) {
      if (identBefore(q, i)) {
        emit(q[i]);
        i++;
        continue;
      }
      if (c === 48 && (next === 120 || next === 88)) {
        i += 2;
        while (i < n && isHex(q.charCodeAt(i))) i++;
      } else {
        while (i < n && (isDigit(q.charCodeAt(i)) || q.charCodeAt(i) === 46)) i++;
        if (i < n && (q[i] === 'e' || q[i] === 'E')) {
          let k = i + 1;
          if (k < n && (q[k] === '+' || q[k] === '-')) k++;
          if (k < n && isDigit(q.charCodeAt(k))) {
            i = k;
            while (i < n && isDigit(q.charCodeAt(i))) i++;
          }
        }
      }
      emit('?');
    } else if (
      (c === 36 || c === 64 || c === 58) &&
      next >= 0 &&
      (isDigit(next) || isIdentStart(next)) &&
      !(c === 58 && i > 0 && q.charCodeAt(i - 1) === 58)
    ) {
      let j = i + 1;
      while (j < n && isIdent(q.charCodeAt(j))) j++;
      emit(q.slice(i, j));
      i = j;
    } else if (isIdentStart(c) || isNonASCII(c)) {
      let j = i + 1;
      while (j < n && (isIdent(q.charCodeAt(j)) || isNonASCII(q.charCodeAt(j)))) j++;
      emit(q.slice(i, j));
      i = j;
    } else {
      if (c === 44 || c === 41) space = false;
      emit(q[i]);
      if (c === 40) space = false;
      i++;
    }
  }
  return collapseLists(out.join(''));
}

/** "(?, ?, ?)" → "(?)" and "VALUES (?), (?)" → "VALUES (?)". */
function collapseLists(s: string): string {
  if (!s.includes('?,')) return s;
  let b = '';
  let i = 0;
  while (i < s.length) {
    if (s[i] === '(') {
      let j = i + 1;
      let ok = false;
      while (j < s.length) {
        if (s[j] === '?') {
          j++;
          ok = true;
          if (j < s.length && s[j] === ',') {
            j++;
            if (j < s.length && s[j] === ' ') j++;
            continue;
          }
        }
        break;
      }
      if (ok && j < s.length && s[j] === ')') {
        b += '(?)';
        i = j + 1;
        for (;;) {
          let k = i;
          if (k < s.length && s[k] === ',') {
            k++;
            if (k < s.length && s[k] === ' ') k++;
            if (k < s.length && s[k] === '(') {
              let m = k + 1;
              while (m < s.length && (s[m] === '?' || s[m] === ',' || s[m] === ' ')) m++;
              if (m < s.length && s[m] === ')' && m > k + 1) {
                i = m + 1;
                continue;
              }
            }
          }
          break;
        }
        continue;
      }
    }
    b += s[i];
    i++;
  }
  return b;
}

/**
 * Key/value store statement (redis, memcached): the command word upper-cased and every argument replaced by `?`
 * (apm.md §7: `GET products:42` → `GET ?`). Long argument lists keep at most 32 placeholders plus `…`.
 */
export function sanitizeKeyValue(command: string, args: ReadonlyArray<unknown> = []): string {
  const parts = [String(command).toUpperCase()];
  const shown = Math.min(args.length, 32);
  for (let k = 0; k < shown; k++) parts.push('?');
  if (args.length > shown) parts.push('…');
  return parts.join(' ');
}

/** Truncates to MAX_QUERY_TEXT without splitting a surrogate pair. */
export function truncateQueryText(s: string, max = MAX_QUERY_TEXT): string {
  if (s.length <= max) return s;
  let end = max;
  const code = s.charCodeAt(end - 1);
  if (code >= 0xd800 && code <= 0xdbff) end--;
  return s.slice(0, end);
}

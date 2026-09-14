/*
 * Copyright The openlog Authors
 * SPDX-License-Identifier: Apache-2.0
 */
package io.github.onuragtas.openlog.javaagent.db;

import java.util.Locale;

/**
 * DB statement sanitization, a port of the Go agent's openlogsql.Sanitize (agents/go/openlogsql/sanitize.go, via
 * agents/node/src/sanitize.ts) so every openlog agent sends the same db.query.text for the same statement (apm.md §7
 * normalizes again on the backend).
 */
public final class SqlSanitizer {
  /** Upper bound of db.query.text in UTF-16 code units, like the Node.js agent. */
  public static final int MAX_QUERY_TEXT = 4096;

  private SqlSanitizer() {}

  private static boolean isDigit(int c) {
    return c >= '0' && c <= '9';
  }

  private static boolean isHex(int c) {
    return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F');
  }

  private static boolean isIdentStart(int c) {
    return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z');
  }

  private static boolean isIdent(int c) {
    return isIdentStart(c) || isDigit(c) || c == '$';
  }

  private static boolean isNonAscii(int c) {
    return c >= 128;
  }

  private static boolean identBefore(String q, int i) {
    if (i == 0) {
      return false;
    }
    char p = q.charAt(i - 1);
    return isIdent(p) || isNonAscii(p);
  }

  private static final class Out {
    final StringBuilder b;
    boolean space;

    Out(int cap) {
      b = new StringBuilder(cap);
    }

    void emit(String s) {
      if (space && b.length() > 0) {
        b.append(' ');
      }
      space = false;
      b.append(s);
    }

    void emit(char c) {
      if (space && b.length() > 0) {
        b.append(' ');
      }
      space = false;
      b.append(c);
    }
  }

  /**
   * Normalizes a SQL statement so it carries no literal values and statements differing only in values group together:
   * string literals ('…', E'…', and "…" for MySQL) → ?; numeric and hex literals → ? (identifiers such as t1 or col_2
   * are kept); IN (?, ?) and VALUES (?), (?) lists collapse to (?); comments are removed and whitespace is collapsed.
   * Placeholders ($1, ?, :name, @p1) are kept. {@code system} is the db.system.name value; it only changes how
   * double-quoted text (identifier except for mysql) and {@code #} comments (mysql) are treated.
   */
  public static String sanitizeSql(String q, String system) {
    boolean mysql = "mysql".equals(system) || "mariadb".equals(system);
    Out out = new Out(q.length());
    int n = q.length();
    int i = 0;
    while (i < n) {
      char c = q.charAt(i);
      int next = i + 1 < n ? q.charAt(i + 1) : -1;
      if (c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f') {
        out.space = true;
        i++;
      } else if (c == '-' && next == '-') {
        while (i < n && q.charAt(i) != '\n') {
          i++;
        }
        out.space = true;
      } else if (c == '#' && mysql) {
        while (i < n && q.charAt(i) != '\n') {
          i++;
        }
        out.space = true;
      } else if (c == '/' && next == '*') {
        int j = q.indexOf("*/", i + 2);
        i = j < 0 ? n : j + 2;
        out.space = true;
      } else if (c == '\'' || (c == '"' && mysql) || ("EeNnXxBb".indexOf(c) >= 0 && next == '\'' && !identBefore(q, i))) {
        char prefix = (c != '\'' && c != '"') ? c : 0;
        if (prefix != 0) {
          i++;
        }
        char quote = q.charAt(i);
        i++;
        while (i < n) {
          char d = q.charAt(i);
          if (d == '\\' && i + 1 < n && quote == '\'' && (prefix == 'E' || prefix == 'e' || mysql)) {
            i += 2;
            continue;
          }
          if (d == quote) {
            if (i + 1 < n && q.charAt(i + 1) == quote) {
              i += 2;
              continue;
            }
            i++;
            break;
          }
          i++;
        }
        out.emit('?');
      } else if (c == '$' && next == '$') {
        int j = q.indexOf("$$", i + 2);
        i = j < 0 ? n : j + 2;
        out.emit('?');
      } else if (c == '"' || c == '`' || c == '[') {
        char closer = c == '[' ? ']' : c;
        int j = q.indexOf(closer, i + 1);
        if (j < 0) {
          out.emit(q.substring(i));
          i = n;
        } else {
          out.emit(q.substring(i, j + 1));
          i = j + 1;
        }
      } else if (isDigit(c) || (c == '.' && next >= 0 && isDigit(next))) {
        if (identBefore(q, i)) {
          out.emit(c);
          i++;
          continue;
        }
        if (c == '0' && (next == 'x' || next == 'X')) {
          i += 2;
          while (i < n && isHex(q.charAt(i))) {
            i++;
          }
        } else {
          while (i < n && (isDigit(q.charAt(i)) || q.charAt(i) == '.')) {
            i++;
          }
          if (i < n && (q.charAt(i) == 'e' || q.charAt(i) == 'E')) {
            int k = i + 1;
            if (k < n && (q.charAt(k) == '+' || q.charAt(k) == '-')) {
              k++;
            }
            if (k < n && isDigit(q.charAt(k))) {
              i = k;
              while (i < n && isDigit(q.charAt(i))) {
                i++;
              }
            }
          }
        }
        out.emit('?');
      } else if ((c == '$' || c == '@' || c == ':')
          && next >= 0
          && (isDigit(next) || isIdentStart(next))
          && !(c == ':' && i > 0 && q.charAt(i - 1) == ':')) {
        int j = i + 1;
        while (j < n && isIdent(q.charAt(j))) {
          j++;
        }
        out.emit(q.substring(i, j));
        i = j;
      } else if (isIdentStart(c) || isNonAscii(c)) {
        int j = i + 1;
        while (j < n && (isIdent(q.charAt(j)) || isNonAscii(q.charAt(j)))) {
          j++;
        }
        out.emit(q.substring(i, j));
        i = j;
      } else {
        if (c == ',' || c == ')') {
          out.space = false;
        }
        out.emit(c);
        if (c == '(') {
          out.space = false;
        }
        i++;
      }
    }
    return collapseLists(out.b.toString());
  }

  /** "(?, ?, ?)" → "(?)" and "VALUES (?), (?)" → "VALUES (?)". */
  static String collapseLists(String s) {
    if (!s.contains("?,")) {
      return s;
    }
    StringBuilder b = new StringBuilder(s.length());
    int n = s.length();
    int i = 0;
    while (i < n) {
      if (s.charAt(i) == '(') {
        int j = i + 1;
        boolean ok = false;
        while (j < n) {
          if (s.charAt(j) == '?') {
            j++;
            ok = true;
            if (j < n && s.charAt(j) == ',') {
              j++;
              if (j < n && s.charAt(j) == ' ') {
                j++;
              }
              continue;
            }
          }
          break;
        }
        if (ok && j < n && s.charAt(j) == ')') {
          b.append("(?)");
          i = j + 1;
          for (;;) {
            int k = i;
            if (k < n && s.charAt(k) == ',') {
              k++;
              if (k < n && s.charAt(k) == ' ') {
                k++;
              }
              if (k < n && s.charAt(k) == '(') {
                int m = k + 1;
                while (m < n && (s.charAt(m) == '?' || s.charAt(m) == ',' || s.charAt(m) == ' ')) {
                  m++;
                }
                if (m < n && s.charAt(m) == ')' && m > k + 1) {
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
      b.append(s.charAt(i));
      i++;
    }
    return b.toString();
  }

  /**
   * Key/value store statement (redis, memcached): the command word upper-cased and every argument replaced by {@code
   * ?} (apm.md §7: {@code GET products:42} → {@code GET ?}). Long argument lists keep at most 32 placeholders plus
   * {@code …}. The Java instrumentations report the command as one space-separated text, so arguments are the
   * whitespace-separated words after the command.
   */
  public static String sanitizeKeyValue(String text) {
    String t = text.trim();
    if (t.isEmpty()) {
      return t;
    }
    String[] parts = t.split("\\s+");
    StringBuilder b = new StringBuilder(parts[0].toUpperCase(Locale.ROOT));
    int args = parts.length - 1;
    int shown = Math.min(args, 32);
    for (int k = 0; k < shown; k++) {
      b.append(" ?");
    }
    if (args > shown) {
      b.append(" …");
    }
    return b.toString();
  }

  /** Truncates to {@code max} UTF-16 code units without splitting a surrogate pair. */
  public static String truncate(String s, int max) {
    if (s.length() <= max) {
      return s;
    }
    int end = max;
    if (Character.isHighSurrogate(s.charAt(end - 1))) {
      end--;
    }
    return s.substring(0, end);
  }
}

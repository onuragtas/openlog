"""DB statement sanitization.

A port of the Go agent's ``openlogsql.Sanitize`` (agents/go/openlogsql/sanitize.go) and the Node.js agent's
``sanitizeSQL`` so every openlog agent sends the same ``db.query.text`` for the same statement (apm.md §7 normalizes
again on the backend). Shared test cases: tests/unit/test_sanitize.py.
"""

from __future__ import annotations

from typing import Sequence

#: Upper bound of db.query.text in UTF-8 bytes, like the Go agent.
MAX_QUERY_TEXT_BYTES = 4096

_SPACE = frozenset(" \t\n\r\f")
_PREFIXES = frozenset("EeNnXxBb")


def _is_digit(c: str) -> bool:
    return "0" <= c <= "9"


def _is_hex(c: str) -> bool:
    return _is_digit(c) or "a" <= c <= "f" or "A" <= c <= "F"


def _is_ident_start(c: str) -> bool:
    return c == "_" or "a" <= c <= "z" or "A" <= c <= "Z"


def _is_ident(c: str) -> bool:
    return _is_ident_start(c) or _is_digit(c) or c == "$"


def _is_non_ascii(c: str) -> bool:
    return ord(c) >= 128


def _ident_before(q: str, i: int) -> bool:
    if i == 0:
        return False
    p = q[i - 1]
    return _is_ident(p) or _is_non_ascii(p)


def sanitize_sql(q: str, system: str = "") -> str:
    """Normalizes a SQL statement so it carries no literal values.

    String literals ('…', E'…', and "…" for MySQL) become ``?``; numeric and hex literals become ``?`` (identifiers
    such as ``t1`` or ``col_2`` are kept); ``IN (?, ?)`` and ``VALUES (?), (?)`` lists collapse to ``(?)``; comments
    are removed and whitespace is collapsed. Placeholders (``$1``, ``?``, ``:name``, ``@p1``, ``%s``) are kept.
    ``system`` is the db.system.name value; it only changes how double-quoted text (identifier except for mysql)
    and ``#`` comments (mysql) are treated.
    """
    mysql = system in ("mysql", "mariadb")
    out: list[str] = []
    length = 0
    space = False
    n = len(q)
    i = 0

    def emit(s: str) -> None:
        nonlocal space, length
        if space and length > 0:
            out.append(" ")
            length += 1
        space = False
        out.append(s)
        length += len(s)

    while i < n:
        c = q[i]
        nxt = q[i + 1] if i + 1 < n else ""
        if c in _SPACE:
            space = True
            i += 1
        elif c == "-" and nxt == "-":
            j = q.find("\n", i)
            i = n if j < 0 else j
            space = True
        elif c == "#" and mysql:
            j = q.find("\n", i)
            i = n if j < 0 else j
            space = True
        elif c == "/" and nxt == "*":
            j = q.find("*/", i + 2)
            i = n if j < 0 else j + 2
            space = True
        elif c == "'" or (c == '"' and mysql) or (c in _PREFIXES and nxt == "'" and not _ident_before(q, i)):
            prefix = c if c not in ("'", '"') else ""
            if prefix:
                i += 1
            quote = q[i]
            escapes = quote == "'" and (prefix in ("E", "e") or mysql)
            i += 1
            while i < n:
                d = q[i]
                if d == "\\" and i + 1 < n and escapes:
                    i += 2
                    continue
                if d == quote:
                    if i + 1 < n and q[i + 1] == quote:
                        i += 2
                        continue
                    i += 1
                    break
                i += 1
            emit("?")
        elif c == "$" and nxt == "$":
            j = q.find("$$", i + 2)
            i = n if j < 0 else j + 2
            emit("?")
        elif c in ('"', "`", "["):
            closer = "]" if c == "[" else c
            j = q.find(closer, i + 1)
            if j < 0:
                emit(q[i:])
                i = n
            else:
                emit(q[i : j + 1])
                i = j + 1
        elif _is_digit(c) or (c == "." and nxt != "" and _is_digit(nxt)):
            if _ident_before(q, i):
                emit(c)
                i += 1
                continue
            if c == "0" and nxt in ("x", "X"):
                i += 2
                while i < n and _is_hex(q[i]):
                    i += 1
            else:
                while i < n and (_is_digit(q[i]) or q[i] == "."):
                    i += 1
                if i < n and q[i] in ("e", "E"):
                    k = i + 1
                    if k < n and q[k] in ("+", "-"):
                        k += 1
                    if k < n and _is_digit(q[k]):
                        i = k
                        while i < n and _is_digit(q[i]):
                            i += 1
            emit("?")
        elif (
            c in ("$", "@", ":")
            and nxt != ""
            and (_is_digit(nxt) or _is_ident_start(nxt))
            and not (c == ":" and i > 0 and q[i - 1] == ":")
        ):
            j = i + 1
            while j < n and _is_ident(q[j]):
                j += 1
            emit(q[i:j])
            i = j
        elif _is_ident_start(c) or _is_non_ascii(c):
            j = i + 1
            while j < n and (_is_ident(q[j]) or _is_non_ascii(q[j])):
                j += 1
            emit(q[i:j])
            i = j
        else:
            if c in (",", ")"):
                space = False
            emit(c)
            if c == "(":
                space = False
            i += 1
    return _collapse_lists("".join(out))


def _collapse_lists(s: str) -> str:
    """``(?, ?, ?)`` → ``(?)`` and ``VALUES (?), (?)`` → ``VALUES (?)``."""
    if "?," not in s:
        return s
    b: list[str] = []
    n = len(s)
    i = 0
    while i < n:
        if s[i] == "(":
            j = i + 1
            ok = False
            while j < n:
                if s[j] == "?":
                    j += 1
                    ok = True
                    if j < n and s[j] == ",":
                        j += 1
                        if j < n and s[j] == " ":
                            j += 1
                        continue
                break
            if ok and j < n and s[j] == ")":
                b.append("(?)")
                i = j + 1
                while True:
                    k = i
                    if k < n and s[k] == ",":
                        k += 1
                        if k < n and s[k] == " ":
                            k += 1
                        if k < n and s[k] == "(":
                            m = k + 1
                            while m < n and s[m] in "?, ":
                                m += 1
                            if m < n and s[m] == ")" and m > k + 1:
                                i = m + 1
                                continue
                    break
                continue
        b.append(s[i])
        i += 1
    return "".join(b)


def sanitize_key_value(command: str, args: Sequence[object] = ()) -> str:
    """Key/value store statement (redis, memcached): ``GET products:42`` → ``GET ?``.

    The command word is upper-cased and every argument replaced by ``?``; long argument lists keep at most 32
    placeholders plus ``…`` (apm.md §7).
    """
    parts = [str(command).upper()]
    shown = min(len(args), 32)
    parts.extend("?" * shown)
    if len(args) > shown:
        parts.append("…")
    return " ".join(parts)


def truncate_query_text(s: str, max_bytes: int = MAX_QUERY_TEXT_BYTES) -> str:
    """Truncates to ``max_bytes`` UTF-8 bytes without splitting a character (like the Go agent)."""
    if len(s) * 4 <= max_bytes:
        return s
    b = s.encode("utf-8", "surrogatepass")
    if len(b) <= max_bytes:
        return s
    return b[:max_bytes].decode("utf-8", "ignore")

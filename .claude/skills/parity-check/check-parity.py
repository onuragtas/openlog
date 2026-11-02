#!/usr/bin/env python3
"""Lists what the web console and the mobile console do not both do, judged by the API calls each one makes.

An endpoint only one of them calls is a feature only one of them has, or a thing only one of them can see. A sort or page parameter only
one of them sends is the same feature working differently. It cannot see logic inside a screen: docs-check-style reading is still needed.

Exceptions live in parity-exceptions.txt, one `METHOD /path  # why` per line. A line with no reason is an error, because a gap nobody can
explain is not an exception. Run from the repository root. Exits 1 when there is a gap.
"""
import re
import sys
from pathlib import Path

HERE = Path(__file__).parent
LIST_PARAMS = ("sort", "dir", "offset", "days", "compare")


def normal(path: str) -> str:
    path = path.split("?")[0]
    # {id} on the web, $id or ${...} on mobile: only that a value goes there matters.
    path = re.sub(r"\$\{[^}]*(?:\{[^}]*\}[^}]*)*\}|\$\w+|\{[^}]+\}", "{}", path)
    # A query appended as a variable, as in '/api/v1/hosts$query', is no part of the path.
    path = re.sub(r"(?<=[^/])\{\}$", "", path)
    return path.rstrip("/") or "/"


def call_arguments(text: str, start: int) -> str:
    """The rest of a call whose opening parenthesis is before `start`: its own arguments and nobody else's."""
    depth = 1
    for i in range(start, len(text)):
        if text[i] == "(":
            depth += 1
        elif text[i] == ")":
            depth -= 1
            if not depth:
                return text[start:i]
    return text[start:]


def web_calls() -> dict[str, set[str]]:
    calls: dict[str, set[str]] = {}
    for f in Path("web/src").rglob("*.ts*"):
        name = f.name
        if ".test." in name or name.endswith(".gen.ts") or "mocks" in f.parts:
            continue
        text = f.read_text()
        for m in re.finditer(r'api\.(GET|POST|PUT|PATCH|DELETE)\(\s*"(/api/v1/[^"]+)"', text):
            key = f"{m.group(1)} {normal(m.group(2))}"
            params = calls.setdefault(key, set())
            near = call_arguments(text, m.end())
            params.update(p for p in LIST_PARAMS if re.search(rf"\b{p}\b", near))
            # A list's state is spread into the query, as ...list or ...p.list, and carries all of these.
            if re.search(r"\.\.\.(?:\w+\.)?(list|state)\b", near):
                params.update(("sort", "dir", "offset"))
    return calls


def dart_paths(text: str) -> list[str]:
    """Every /api/v1 string in this text, reading each to its real end: `${...}` can hold quotes of its own."""
    out = []
    for m in re.finditer(r"'(?=/api/v1/)", text):
        i, depth, chars = m.end(), 0, []
        while i < len(text):
            c = text[i]
            if text.startswith("${", i):
                depth += 1
                chars.append(c)
            elif depth and c == "}":
                depth -= 1
                chars.append(c)
            elif not depth and c == "'":
                # Dart joins adjacent strings, and a long path is written as two: '/api/v1/x/$id'\n  '/comments'.
                j = i + 1
                while j < len(text) and text[j].isspace():
                    j += 1
                if j >= len(text) or text[j] != "'":
                    break
                i = j
            else:
                chars.append(c)
            i += 1
        path = "".join(chars)
        # /rules/$id/${enabled ? 'enable' : 'disable'} is two endpoints.
        choice = re.search(r"\$\{[^{}]*\?\s*'([^']*)'\s*:\s*'([^']*)'\s*\}", path)
        if choice:
            out += [path.replace(choice.group(0), choice.group(1)), path.replace(choice.group(0), choice.group(2))]
        else:
            out.append(path)
    return out


def mobile_calls() -> dict[str, set[str]]:
    calls: dict[str, set[str]] = {}
    text = Path("mobile/lib/src/api/client.dart").read_text()
    # Read a method at a time. Its path is often built first, in a variable or through _listPath, and only then sent.
    chunks = re.split(r"\n  (?=(?:static )?(?:Future|Stream|String|Uri|void|Map|List|bool|int|[A-Z]\w*)[<\w, ?>]* \w+[(<{ ])", text)
    for chunk in chunks:
        # A doc comment belongs to the next method but lands in this chunk, and "sorted by [sort]" sends nothing.
        chunk = re.sub(r"^\s*//.*$", "", chunk, flags=re.M)
        verbs = re.findall(r"'(GET|POST|PUT|PATCH|DELETE)'", chunk)
        if not verbs:
            continue
        for path in dart_paths(chunk):
            for verb in set(verbs):
                params = calls.setdefault(f"{verb} {normal(path)}", set())
                # The path itself is not a parameter: /deployments/compare sends no `compare`.
                sent = re.sub(r"'/[^'?]*'", "", chunk)
                params.update(p for p in LIST_PARAMS if re.search(rf"\b{p}\b", sent))
    return calls


def exceptions() -> tuple[set[str], list[str]]:
    known, problems = set(), []
    path = HERE / "parity-exceptions.txt"
    if not path.exists():
        return known, problems
    for line in path.read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        entry, _, why = line.partition("#")
        if not why.strip():
            problems.append(f"parity-exceptions.txt: no reason given for {entry.strip()}")
        known.add(" ".join(entry.split()))
    return known, problems


def main() -> int:
    web, mobile = web_calls(), mobile_calls()
    known, problems = exceptions()
    web_only = sorted(k for k in web if k not in mobile and k not in known)
    mobile_only = sorted(k for k in mobile if k not in web and k not in known)
    differ = sorted(
        f"{k}: web sends {sorted(web[k] - mobile[k])}, mobile sends {sorted(mobile[k] - web[k])}"
        for k in web.keys() & mobile.keys()
        if web[k] != mobile[k] and f"params {k}" not in known
    )
    print(f"endpoints: web {len(web)}, mobile {len(mobile)}, both {len(web.keys() & mobile.keys())}")
    for title, rows in (("only on the web", web_only), ("only on mobile", mobile_only), ("both, but with different list parameters", differ)):
        print(f"\n{title}: {len(rows)}")
        for row in rows:
            print(f"  {row}")
    for p in problems:
        print(p)
    return 1 if web_only or mobile_only or differ or problems else 0


if __name__ == "__main__":
    sys.exit(main())

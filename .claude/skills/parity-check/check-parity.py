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
            near = text[m.end() : m.end() + 400]
            params.update(p for p in LIST_PARAMS if re.search(rf"\b{p}\b", near))
            # A list's state is spread into the query, and carries all of these.
            if re.search(r"\.\.\.(list|state)\b", near):
                params.update(("sort", "dir", "offset"))
    return calls


def mobile_calls() -> dict[str, set[str]]:
    calls: dict[str, set[str]] = {}
    text = Path("mobile/lib/src/api/client.dart").read_text()
    # _send('GET', '/api/v1/x') and its shapes across lines. A query is often built just before the call.
    for m in re.finditer(r"""_send\w*(?:<[^>]*>)?\(\s*'(GET|POST|PUT|PATCH|DELETE)',\s*['"](/api/v1/[^'"]*)['"]""", text):
        key = f"{m.group(1)} {normal(m.group(2))}"
        params = calls.setdefault(key, set())
        # The method this call is in, and no further: the one before it has parameters of its own.
        start = text.rfind("Future<", 0, m.start())
        window = text[max(start, 0) : m.end() + 200]
        params.update(p for p in LIST_PARAMS if re.search(rf"\b{p}\b", window))
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

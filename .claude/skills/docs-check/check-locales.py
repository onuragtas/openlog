#!/usr/bin/env python3
"""Fails when a language of the documentation site is missing something English has.

Languages and excluded pages come from the site's own configuration rather than a list here, so a language added later is held to
this too. The console needs no such check: a catalog is typed as `Messages`, so `npm run typecheck` fails on a missing string.
Run from the repository root.
"""
import re
import sys
from fnmatch import fnmatch
from pathlib import Path

DOCS = Path("docs")
# Written for people changing openlog, and in English on purpose (docs-check step 3).
ENGLISH_ONLY = {"contracts", "node_modules", "images", "public"}


def site_locales() -> list[str]:
    config = (DOCS / ".vitepress/config.ts").read_text()
    block = config[config.index("locales:") :]
    return [m for m in re.findall(r'^\s{4}"?([a-z]{2}(?:-[A-Za-z]+)?)"?: \{', block, re.M) if m != "root"]


def excluded() -> list[str]:
    config = (DOCS / ".vitepress/config.ts").read_text()
    m = re.search(r"srcExclude:\s*\[(.*?)\]", config, re.S)
    return re.findall(r'"([^"]+)"', m.group(1)) if m else []


def pages() -> list[Path]:
    out = []
    skip = excluded()
    for p in sorted(DOCS.rglob("*.md")):
        rel = p.relative_to(DOCS)
        if rel.parts[0] in ENGLISH_ONLY or ".vitepress" in rel.parts or any(fnmatch(str(rel), g) for g in skip):
            continue
        out.append(rel)
    return out


def shape(path: Path) -> tuple[int, int]:
    """Headings and code blocks, which a translation keeps even when every sentence differs."""
    text = path.read_text()
    # An English integration page is one line that includes a README, and what counts is what it renders.
    text = re.sub(r"<!--@include: (.+?)-->", lambda m: (path.parent / m.group(1)).read_text(), text)
    fences = len(re.findall(r"^\s*```", text, re.M)) // 2
    prose = re.sub(r"^\s*```.*?^\s*```", "", text, flags=re.M | re.S)
    return len(re.findall(r"^#{1,4} ", prose, re.M)), fences


def main() -> int:
    problems = []
    locales = site_locales()
    all_pages = pages()
    english = [p for p in all_pages if p.parts[0] not in locales]
    for locale in locales:
        for page in english:
            twin = DOCS / locale / page
            if not twin.exists():
                problems.append(f"{twin}: missing (English: docs/{page})")
                continue
            en, other = shape(DOCS / page), shape(twin)
            if en != other:
                problems.append(f"{twin}: {other[0]} headings and {other[1]} code blocks, English has {en[0]} and {en[1]}")
        for page in (p for p in all_pages if p.parts[0] == locale):
            if not (DOCS / Path(*page.parts[1:])).exists():
                problems.append(f"docs/{page}: no English page, and English is the default")

    print(f"site languages: en, {', '.join(locales)}; {len(english)} English pages")
    for problem in problems:
        print(problem)
    print(f"{len(problems)} problem(s)")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""Fails when a language of the documentation site or the console is missing something another one has.

Languages come from the projects' own configuration rather than a list here, so a language added later is held to this too.
Run from the repository root.
"""
import re
import sys
from pathlib import Path

DOCS = Path("docs")
# Written for people changing openlog, and in English on purpose (docs-check step 3).
ENGLISH_ONLY = {"contracts", "plan", "node_modules", "images", "public"}


def site_locales() -> list[str]:
    config = (DOCS / ".vitepress/config.ts").read_text()
    block = config[config.index("locales:") :]
    return [m for m in re.findall(r'^\s{4}"?([a-z]{2}(?:-[A-Za-z]+)?)"?: \{', block, re.M) if m != "root"]


def pages() -> list[Path]:
    out = []
    for p in sorted(DOCS.rglob("*.md")):
        rel = p.relative_to(DOCS)
        if rel.parts[0] in ENGLISH_ONLY or ".vitepress" in rel.parts:
            continue
        out.append(rel)
    return out


def shape(path: Path) -> tuple[int, int]:
    """Headings and code blocks, which a translation keeps even when every sentence differs."""
    text = path.read_text()
    fences = len(re.findall(r"^\s*```", text, re.M)) // 2
    prose = re.sub(r"^\s*```.*?^\s*```", "", text, flags=re.M | re.S)
    return len(re.findall(r"^#{1,4} ", prose, re.M)), fences


def console_keys(path: Path) -> set[str]:
    keys, stack = set(), []
    text = re.sub(r"^\s*//.*$", "", path.read_text(), flags=re.M)
    # A quoted key stays a key, and every other string goes: a translation is free to contain "Confirm: {".
    text = re.sub(r"""(["'])((?:(?!\1)[^\\\n])+)\1(?=\s*:)""", r"\2", text)
    text = re.sub(r'''"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'|`(?:[^`\\]|\\.)*`''', '""', text)
    for line in text.splitlines():
        for m in re.finditer(r"([\w.-]+):\s*(\{)?|(\})", line):
            if m.group(3):
                if stack:
                    stack.pop()
            elif m.group(2):
                stack.append(m.group(1))
            else:
                keys.add(".".join(stack + [m.group(1)]))
    return keys


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

    catalogs = sorted(Path("web/src/i18n/locales").glob("*.ts"))
    if catalogs:
        base = next((c for c in catalogs if c.stem == "en"), catalogs[0])
        base_keys = console_keys(base)
        for catalog in catalogs:
            if catalog == base:
                continue
            keys = console_keys(catalog)
            for key in sorted(base_keys - keys):
                problems.append(f"{catalog}: missing {key}")
            for key in sorted(keys - base_keys):
                problems.append(f"{catalog}: {key} is not in {base.name}")

    print(f"site languages: en, {', '.join(locales)}; {len(english)} English pages; console: {', '.join(c.stem for c in catalogs)}")
    for problem in problems:
        print(problem)
    print(f"{len(problems)} problem(s)")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())

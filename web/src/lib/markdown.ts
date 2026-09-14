// A deliberately tiny, safe Markdown parser for dashboard text widgets. It produces a small AST that
// components/oql/Markdown.tsx renders as React elements (never HTML strings): headings, paragraphs, bold,
// italic, inline code, fenced code blocks, http(s) links and bullet/numbered lists. Unit-tested in markdown.test.ts.

export type Inline =
  | { type: "text"; text: string }
  | { type: "strong"; children: Inline[] }
  | { type: "em"; children: Inline[] }
  | { type: "code"; text: string }
  | { type: "link"; href: string; children: Inline[] };

export type Block =
  | { type: "heading"; level: 1 | 2 | 3 | 4 | 5 | 6; children: Inline[] }
  | { type: "paragraph"; children: Inline[] }
  | { type: "code"; text: string; lang: string }
  | { type: "list"; ordered: boolean; items: Inline[][] }
  | { type: "hr" };

/** Only absolute http(s) URLs become links. */
export function safeHref(url: string): string | null {
  const u = url.trim();
  if (!/^https?:\/\/[^\s]+$/i.test(u)) return null;
  try {
    const parsed = new URL(u);
    return parsed.protocol === "http:" || parsed.protocol === "https:" ? parsed.href : null;
  } catch {
    return null;
  }
}

function pushText(out: Inline[], text: string) {
  if (!text) return;
  const last = out[out.length - 1];
  if (last?.type === "text") last.text += text;
  else out.push({ type: "text", text });
}

/** Parses inline markup of one block. */
export function parseInline(src: string, depth = 0): Inline[] {
  const out: Inline[] = [];
  let i = 0;
  while (i < src.length) {
    const c = src[i]!;
    if (c === "\\" && i + 1 < src.length && /[\\`*_[\]()#-]/.test(src[i + 1]!)) {
      pushText(out, src[i + 1]!);
      i += 2;
      continue;
    }
    if (c === "`") {
      const end = src.indexOf("`", i + 1);
      if (end > i + 1) {
        out.push({ type: "code", text: src.slice(i + 1, end) });
        i = end + 1;
        continue;
      }
    }
    if (depth < 4 && (src.startsWith("**", i) || src.startsWith("__", i))) {
      const mark = src.slice(i, i + 2);
      const end = src.indexOf(mark, i + 2);
      if (end > i + 2) {
        out.push({ type: "strong", children: parseInline(src.slice(i + 2, end), depth + 1) });
        i = end + 2;
        continue;
      }
    }
    if (depth < 4 && (c === "*" || c === "_") && src[i + 1] !== c && src[i + 1] !== " ") {
      const end = src.indexOf(c, i + 1);
      // _ inside words (snake_case) is not emphasis
      const wordBefore = c === "_" && i > 0 && /\w/.test(src[i - 1]!);
      if (end > i + 1 && src[end - 1] !== " " && !wordBefore) {
        out.push({ type: "em", children: parseInline(src.slice(i + 1, end), depth + 1) });
        i = end + 1;
        continue;
      }
    }
    if (c === "[") {
      const m = /^\[([^\]]+)\]\(([^)\s]+)\)/.exec(src.slice(i));
      if (m) {
        const href = safeHref(m[2]!);
        const children = parseInline(m[1]!, depth + 1);
        if (href) out.push({ type: "link", href, children });
        else {
          for (const ch of children) {
            if (ch.type === "text") pushText(out, ch.text);
            else out.push(ch);
          }
        }
        i += m[0].length;
        continue;
      }
    }
    pushText(out, c);
    i++;
  }
  return out;
}

const HEADING = /^(#{1,6})\s+(.*?)\s*#*\s*$/;
const BULLET = /^\s*[-*+]\s+(.*)$/;
const ORDERED = /^\s*\d+[.)]\s+(.*)$/;
const FENCE = /^\s*(```|~~~)\s*([\w-]*)\s*$/;
const HR = /^\s*([-*_])(\s*\1){2,}\s*$/;

/** Parses a Markdown document into blocks. */
export function parseMarkdown(src: string): Block[] {
  const lines = src.replace(/\r\n?/g, "\n").split("\n");
  const blocks: Block[] = [];
  let para: string[] = [];
  const flush = () => {
    if (para.length) blocks.push({ type: "paragraph", children: parseInline(para.join(" ")) });
    para = [];
  };
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]!;
    const fence = FENCE.exec(line);
    if (fence) {
      flush();
      const body: string[] = [];
      i++;
      while (i < lines.length && !new RegExp(`^\\s*${fence[1]}\\s*$`).test(lines[i]!)) body.push(lines[i++]!);
      blocks.push({ type: "code", text: body.join("\n"), lang: fence[2] ?? "" });
      continue;
    }
    if (line.trim() === "") {
      flush();
      continue;
    }
    const h = HEADING.exec(line);
    if (h) {
      flush();
      blocks.push({ type: "heading", level: h[1]!.length as 1 | 2 | 3 | 4 | 5 | 6, children: parseInline(h[2]!) });
      continue;
    }
    if (HR.test(line)) {
      flush();
      blocks.push({ type: "hr" });
      continue;
    }
    const bullet = BULLET.exec(line);
    const ordered = bullet ? null : ORDERED.exec(line);
    if (bullet || ordered) {
      flush();
      const isOrdered = !!ordered;
      const items: Inline[][] = [];
      let j = i;
      while (j < lines.length) {
        const m = isOrdered ? ORDERED.exec(lines[j]!) : BULLET.exec(lines[j]!);
        if (!m) break;
        items.push(parseInline(m[1]!));
        j++;
      }
      blocks.push({ type: "list", ordered: isOrdered, items });
      i = j - 1;
      continue;
    }
    para.push(line.trim());
  }
  flush();
  return blocks;
}

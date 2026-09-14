import { Fragment, useMemo, type ReactNode } from "react";
import { parseMarkdown, type Block, type Inline } from "@/lib/markdown";
import { cn } from "@/lib/utils";

function inline(nodes: Inline[]): ReactNode {
  return nodes.map((n, i) => {
    switch (n.type) {
      case "text":
        return <Fragment key={i}>{n.text}</Fragment>;
      case "strong":
        return <strong key={i}>{inline(n.children)}</strong>;
      case "em":
        return <em key={i}>{inline(n.children)}</em>;
      case "code":
        return (
          <code key={i} className="rounded bg-muted px-1 py-0.5 font-mono text-[0.85em]">
            {n.text}
          </code>
        );
      case "link":
        return (
          <a key={i} href={n.href} target="_blank" rel="noopener noreferrer" className="text-primary underline underline-offset-2 hover:no-underline">
            {inline(n.children)}
          </a>
        );
    }
  });
}

const HEADING_CLASS = ["text-lg font-semibold", "text-base font-semibold", "text-sm font-semibold", "text-sm font-medium", "text-sm font-medium", "text-sm font-medium"];

function block(b: Block, i: number): ReactNode {
  switch (b.type) {
    case "heading": {
      // Widgets sit below the page's h1/h2: shift levels so the outline stays valid.
      const Tag = `h${Math.min(6, b.level + 2)}` as "h3";
      return (
        <Tag key={i} className={HEADING_CLASS[b.level - 1]}>
          {inline(b.children)}
        </Tag>
      );
    }
    case "paragraph":
      return <p key={i}>{inline(b.children)}</p>;
    case "code":
      return (
        <pre key={i} className="overflow-x-auto rounded-md bg-muted p-2 font-mono text-xs">
          <code>{b.text}</code>
        </pre>
      );
    case "list": {
      const Tag = b.ordered ? "ol" : "ul";
      return (
        <Tag key={i} className={cn("flex flex-col gap-0.5 pl-5", b.ordered ? "list-decimal" : "list-disc")}>
          {b.items.map((item, j) => (
            <li key={j}>{inline(item)}</li>
          ))}
        </Tag>
      );
    }
    case "hr":
      return <hr key={i} className="border-border" />;
  }
}

/** Safe Markdown (lib/markdown.ts) rendered as React elements. */
export function Markdown({ text, className }: { text: string; className?: string }) {
  const blocks = useMemo(() => parseMarkdown(text), [text]);
  return <div className={cn("flex min-w-0 flex-col gap-2 text-sm break-words", className)}>{blocks.map(block)}</div>;
}

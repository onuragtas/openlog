import { describe, expect, it } from "vitest";
import { parseInline, parseMarkdown, safeHref } from "./markdown";

describe("parseMarkdown", () => {
  it("parses headings, paragraphs, lists, code blocks and rules", () => {
    const doc = "# Title\n\nSome **bold** and *italic* text\ncontinues here.\n\n- one\n- `two`\n\n1. first\n2. second\n\n```sql\nSELECT 1\n  -- x\n```\n---";
    expect(parseMarkdown(doc)).toEqual([
      { type: "heading", level: 1, children: [{ type: "text", text: "Title" }] },
      {
        type: "paragraph",
        children: [
          { type: "text", text: "Some " },
          { type: "strong", children: [{ type: "text", text: "bold" }] },
          { type: "text", text: " and " },
          { type: "em", children: [{ type: "text", text: "italic" }] },
          { type: "text", text: " text continues here." },
        ],
      },
      { type: "list", ordered: false, items: [[{ type: "text", text: "one" }], [{ type: "code", text: "two" }]] },
      { type: "list", ordered: true, items: [[{ type: "text", text: "first" }], [{ type: "text", text: "second" }]] },
      { type: "code", lang: "sql", text: "SELECT 1\n  -- x" },
      { type: "hr" },
    ]);
  });

  it("keeps only http(s) links and never produces markup from text", () => {
    expect(parseInline("[docs](https://example.com/a?b=1) [bad](javascript:alert(1)) <script>x</script>")).toEqual([
      { type: "link", href: "https://example.com/a?b=1", children: [{ type: "text", text: "docs" }] },
      // the unsafe link keeps its text only; the rest stays plain text
      { type: "text", text: " bad) <script>x</script>" },
    ]);
    expect(safeHref("data:text/html,x")).toBeNull();
    expect(safeHref("//example.com")).toBeNull();
    expect(safeHref("http://example.com")).toBe("http://example.com/");
  });

  it("leaves snake_case words and unmatched markers alone", () => {
    expect(parseInline("host_name_x * 2 \\*literal\\*")).toEqual([{ type: "text", text: "host_name_x * 2 *literal*" }]);
  });
});

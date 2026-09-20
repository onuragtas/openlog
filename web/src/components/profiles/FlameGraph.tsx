// The flame graph of one profile (docs/contracts/profiles.md §5). Each row is a stack depth and each block a
// frame, as wide as the share of the value below it — so a wide block is where the time (or the memory) went.
//
// The layout model is the trace waterfall's: rows carry (depth, left, width) as fractions of the container,
// so the graph is absolutely positioned divs rather than a canvas, keeps text selectable and stays legible
// when the container is narrow. Selecting a frame re-roots the graph on it, which is the only way to read the
// narrow blocks — at 1000 distinct stacks the deepest frames are a fraction of a pixel wide.
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { FlameNode } from "@/api/profiles";
import { EmptyState } from "@/components/StateViews";
import { Button } from "@/components/ui/button";
import { formatValue } from "@/lib/profile-format";

const ROW_H = 20;

/**
 * pct renders a fraction as a percentage, rounded. Widths are products of fractions, so accumulation gives
 * values like 60.00000000000001 — harmless to look at, but it lands in the style attribute of every one of
 * the thousands of nodes a flame graph can hold.
 */
const pct = (v: number) => `${Number((v * 100).toFixed(4))}%`;

interface Row {
  node: FlameNode;
  depth: number;
  left: number;
  width: number;
}

/** flatten lays a tree out left to right: a child's width is its share of its parent's value. */
function flatten(node: FlameNode, depth: number, left: number, width: number, out: Row[]): void {
  out.push({ node, depth, left, width });
  const total = node.value || 1;
  let x = left;
  for (const child of node.children ?? []) {
    const w = (child.value / total) * width;
    flatten(child, depth + 1, x, w, out);
    x += w;
  }
}

/** A deterministic warm hue per frame, so the same function keeps its colour across reloads and zooms. */
function hue(name: string): number {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) % 360;
  // 0–60 is the red-to-yellow band a flame graph is conventionally drawn in.
  return h % 55;
}

export function FlameGraph({ flame, unit }: { flame: FlameNode; unit: string }) {
  const { t, i18n } = useTranslation();
  const [focus, setFocus] = useState<FlameNode | null>(null);
  const root = focus ?? flame;

  const rows = useMemo(() => {
    const out: Row[] = [];
    flatten(root, 0, 0, 1, out);
    return out;
  }, [root]);

  if (flame.value === 0 || rows.length === 0) {
    return <EmptyState>{t("profiles.flame.empty")}</EmptyState>;
  }

  const depth = rows.reduce((d, r) => Math.max(d, r.depth), 0) + 1;

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between gap-2">
        <p className="text-xs text-muted-foreground">{t("profiles.flame.zoomHint")}</p>
        {focus && (
          <Button variant="outline" size="sm" onClick={() => setFocus(null)}>
            {t("profiles.flame.reset")}
          </Button>
        )}
      </div>
      <div className="overflow-x-auto">
        <div className="relative min-w-[480px]" style={{ height: depth * ROW_H }} role="tree" aria-label={t("profiles.tabs.flame")}>
          {rows.map((row, i) => {
            const share = root.value ? (row.node.value / root.value) * 100 : 0;
            const label = `${row.node.name} · ${formatValue(row.node.value, unit, i18n.language)} · ${share.toFixed(1)}%`;
            return (
              <button
                key={`${row.depth}-${row.left}-${row.node.name}-${i}`}
                type="button"
                role="treeitem"
                aria-level={row.depth + 1}
                title={label}
                onClick={() => setFocus(row.node)}
                className="absolute overflow-hidden rounded-[2px] border border-background/60 px-1 text-left text-[11px] leading-[18px] whitespace-nowrap text-foreground/90 hover:brightness-110"
                style={{
                  top: row.depth * ROW_H,
                  left: pct(row.left),
                  width: pct(row.width),
                  height: ROW_H - 1,
                  minWidth: 2,
                  background: `hsl(${hue(row.node.name)} 85% 62%)`,
                }}
              >
                {row.width > 0.02 ? row.node.name : ""}
              </button>
            );
          })}
        </div>
      </div>
    </div>
  );
}

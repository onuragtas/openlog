// Virtualized inventory table (TanStack Table + TanStack Virtual). Rows can be
// expanded to show the item's JSON data; row heights are measured.
import { Link } from "@tanstack/react-router";
import { createColumnHelper, flexRender, getCoreRowModel, useReactTable, type ExpandedState } from "@tanstack/react-table";
import { useVirtualizer } from "@tanstack/react-virtual";
import { ChevronDown, ChevronRight } from "lucide-react";
import { useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { InventoryItem, InventorySearchItem } from "@/api/types";
import { JsonView } from "@/components/JsonView";
import { translateOptional } from "@/i18n/dynamic";
import { displayKey, summarize } from "@/lib/inventory";
import { cn } from "@/lib/utils";

type Row = InventoryItem & Partial<Pick<InventorySearchItem, "host_id" | "host_name">>;

const col = createColumnHelper<Row>();

export function InventoryTable({ items, showCategory = true, showHost = false, height = "60vh" }: { items: Row[]; showCategory?: boolean; showHost?: boolean; height?: string }) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState<ExpandedState>({});
  const scrollRef = useRef<HTMLDivElement>(null);

  const columns = useMemo(
    () => [
      col.display({
        id: "expand",
        header: () => <span className="sr-only">{t("common.expand")}</span>,
        cell: ({ row }) => (
          <button
            type="button"
            onClick={row.getToggleExpandedHandler()}
            aria-expanded={row.getIsExpanded()}
            aria-label={row.getIsExpanded() ? t("common.collapse") : t("common.expand")}
            className="rounded p-1 hover:bg-accent"
          >
            {row.getIsExpanded() ? <ChevronDown className="size-4" aria-hidden="true" /> : <ChevronRight className="size-4" aria-hidden="true" />}
          </button>
        ),
      }),
      ...(showHost
        ? [
            col.accessor((r) => r.host_name || r.host_id || "", {
              id: "host",
              header: () => t("inventory.columns.host"),
              cell: ({ row }) =>
                row.original.host_id ? (
                  <Link
                    to="/hosts/$hostId"
                    params={{ hostId: row.original.host_id }}
                    search={{ tab: "inventory", category: row.original.category, iq: row.original.key }}
                    className="truncate font-medium text-primary hover:underline"
                  >
                    {row.original.host_name || row.original.host_id}
                  </Link>
                ) : null,
            }),
          ]
        : []),
      ...(showCategory
        ? [
            col.accessor("category", {
              header: () => t("inventory.columns.category"),
              cell: (info) => <span className="truncate text-muted-foreground">{translateOptional(`inventory.categories.${info.getValue()}`, info.getValue())}</span>,
            }),
          ]
        : []),
      col.accessor("key", {
        header: () => t("inventory.columns.key"),
        cell: (info) => (
          <span className="truncate font-mono" title={info.getValue()}>
            {displayKey(info.row.original)}
          </span>
        ),
      }),
      col.accessor((r) => summarize(r.data), {
        id: "summary",
        header: () => t("inventory.columns.summary"),
        cell: (info) => <span className="truncate font-mono text-muted-foreground">{info.getValue()}</span>,
      }),
    ],
    [t, showCategory, showHost],
  );

  const gridTemplate = ["2.5rem", showHost ? "minmax(7rem,12rem)" : null, showCategory ? "minmax(7rem,11rem)" : null, "minmax(10rem,1fr)", "minmax(8rem,1.3fr)"]
    .filter(Boolean)
    .join(" ");

  // eslint-disable-next-line react-hooks/incompatible-library -- TanStack Table is not compiler-compatible yet
  const table = useReactTable({
    data: items,
    columns,
    state: { expanded },
    onExpandedChange: setExpanded,
    getRowCanExpand: () => true,
    getCoreRowModel: getCoreRowModel(),
    getRowId: (r, i) => `${r.host_id ?? ""}|${r.category}|${r.key}|${i}`,
  });
  const rows = table.getRowModel().rows;

  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => 36,
    overscan: 12,
    getItemKey: (i) => rows[i]!.id,
  });

  return (
    <div role="table" aria-rowcount={rows.length + 1} className="rounded-xl border bg-card text-sm">
      <div role="rowgroup" className="border-b">
        {table.getHeaderGroups().map((hg) => (
          <div role="row" key={hg.id} className="grid items-center px-2" style={{ gridTemplateColumns: gridTemplate }}>
            {hg.headers.map((h) => (
              <div role="columnheader" key={h.id} className="h-9 content-center px-2 text-xs font-medium text-muted-foreground">
                {flexRender(h.column.columnDef.header, h.getContext())}
              </div>
            ))}
          </div>
        ))}
      </div>
      <div ref={scrollRef} role="rowgroup" className="overflow-auto" style={{ height }} data-testid="inventory-scroll">
        <div className="relative w-full" style={{ height: virtualizer.getTotalSize() }}>
          {virtualizer.getVirtualItems().map((vi) => {
            const row = rows[vi.index]!;
            return (
              <div
                key={vi.key}
                data-index={vi.index}
                ref={virtualizer.measureElement}
                role="row"
                aria-rowindex={vi.index + 2}
                className={cn("absolute top-0 left-0 w-full border-b border-border/60", row.getIsExpanded() && "bg-muted/40")}
                style={{ transform: `translateY(${vi.start}px)` }}
              >
                <div className="grid items-center px-2 hover:bg-muted/50" style={{ gridTemplateColumns: gridTemplate }}>
                  {row.getVisibleCells().map((cell) => (
                    <div role="cell" key={cell.id} className="flex h-9 min-w-0 items-center px-2 text-xs">
                      {flexRender(cell.column.columnDef.cell, cell.getContext())}
                    </div>
                  ))}
                </div>
                {row.getIsExpanded() && (
                  <div className="px-4 pb-3 pl-12">
                    <JsonView value={row.original.data} label={row.original.key} />
                  </div>
                )}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}

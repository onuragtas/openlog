import { useQuery } from "@tanstack/react-query";
import { Copy, GripVertical, MoreVertical, Pencil, Trash2 } from "lucide-react";
import { Popover } from "radix-ui";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import type { DashboardWidget } from "@/api/dashboards";
import { oqlQuery, type OqlVariables } from "@/api/oql";
import { Markdown } from "@/components/oql/Markdown";
import { QueryResult } from "@/components/oql/QueryResult";
import { Button } from "@/components/ui/button";
import { ROW_HEIGHT } from "@/lib/dashboards";
import { isCustomRange, type RangeSpec } from "@/lib/time";
import { cn } from "@/lib/utils";

/** Plot height for a widget of `h` grid rows (card header and legend subtracted). */
const chartHeightFor = (h: number) => Math.max(80, h * ROW_HEIGHT + (h - 1) * 12 - 110);

export interface WidgetBodyProps {
  widget: DashboardWidget;
  range: RangeSpec;
  variables: OqlVariables;
  height?: number;
}

/** Runs the widget's query (refreshed every minute for relative ranges) and draws it; markdown needs no query. */
export function WidgetBody({ widget, range, variables, height }: WidgetBodyProps) {
  const { t } = useTranslation();
  const markdown = widget.visualization === "markdown";
  const q = useQuery({ ...oqlQuery({ query: widget.query, range, variables }, { refetchMs: isCustomRange(range) ? false : 60_000 }), enabled: !markdown && widget.query.trim() !== "" });
  if (markdown) return <Markdown text={widget.markdown} />;
  if (!widget.query.trim()) return <p className="py-4 text-center text-sm text-muted-foreground">{t("dashboards.widget.noQuery")}</p>;
  return (
    <QueryResult
      result={q.data}
      visualization={widget.visualization}
      title={widget.title || widget.query}
      unit={widget.unit}
      thresholds={widget.thresholds}
      options={widget.options}
      height={height ?? chartHeightFor(widget.layout.h)}
      isLoading={q.isFetching}
      error={q.error}
      onRetry={() => void q.refetch()}
    />
  );
}

export interface WidgetCardProps extends Omit<WidgetBodyProps, "height"> {
  edit: boolean;
  onEdit: () => void;
  onDuplicate: () => void;
  onDelete: () => void;
  /** Fill the grid cell (desktop) instead of a height derived from the layout (stacked). */
  fill?: boolean;
}

export function WidgetCard({ widget, range, variables, edit, onEdit, onDuplicate, onDelete, fill }: WidgetCardProps) {
  const { t } = useTranslation();
  const titleId = useId();
  const title = widget.title || t("dashboards.widget.untitled");
  return (
    <section
      aria-labelledby={titleId}
      data-testid="dashboard-widget"
      data-visualization={widget.visualization}
      className={cn("flex min-w-0 flex-col rounded-xl border bg-card text-card-foreground shadow-xs", fill ? "h-full" : "")}
      style={fill ? undefined : { minHeight: widget.layout.h * ROW_HEIGHT }}
    >
      <header className="flex min-h-10 items-center gap-1 border-b px-3 py-1.5">
        {edit && (
          <span className="widget-drag-handle -ml-1 hidden cursor-move text-muted-foreground lg:inline-flex" title={t("dashboards.widget.drag")} aria-hidden="true">
            <GripVertical className="size-4" />
          </span>
        )}
        <h3 id={titleId} className="min-w-0 flex-1 truncate text-sm font-semibold" title={title}>
          {title}
        </h3>
        {edit && <WidgetMenu title={title} onEdit={onEdit} onDuplicate={onDuplicate} onDelete={onDelete} />}
      </header>
      <div className={cn("min-h-0 min-w-0 flex-1 overflow-auto p-3", widget.visualization === "billboard" && "flex flex-col justify-center")}>
        <WidgetBody widget={widget} range={range} variables={variables} />
      </div>
    </section>
  );
}

function WidgetMenu({ title, onEdit, onDuplicate, onDelete }: { title: string; onEdit: () => void; onDuplicate: () => void; onDelete: () => void }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const item = "flex min-h-9 w-full items-center gap-2 rounded px-2 text-left text-sm hover:bg-accent";
  const close = (fn: () => void) => () => {
    setOpen(false);
    setConfirm(false);
    fn();
  };
  return (
    <Popover.Root open={open} onOpenChange={(o) => (setOpen(o), setConfirm(false))}>
      <Popover.Trigger asChild>
        <Button variant="ghost" size="icon" className="-my-1 size-8" aria-label={t("dashboards.widget.menu", { title })}>
          <MoreVertical aria-hidden="true" />
        </Button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="end" sideOffset={4} collisionPadding={8} className="z-50 flex w-48 flex-col gap-0.5 rounded-lg border bg-card p-1 text-card-foreground shadow-lg">
          <button type="button" className={item} onClick={close(onEdit)}>
            <Pencil className="size-4" aria-hidden="true" />
            {t("dashboards.widget.edit")}
          </button>
          <button type="button" className={item} onClick={close(onDuplicate)}>
            <Copy className="size-4" aria-hidden="true" />
            {t("dashboards.widget.duplicate")}
          </button>
          {confirm ? (
            <button type="button" className={cn(item, "text-destructive-text")} onClick={close(onDelete)}>
              <Trash2 className="size-4" aria-hidden="true" />
              {t("dashboards.widget.confirmDelete")}
            </button>
          ) : (
            <button type="button" className={item} onClick={() => setConfirm(true)}>
              <Trash2 className="size-4" aria-hidden="true" />
              {t("dashboards.widget.delete")}
            </button>
          )}
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

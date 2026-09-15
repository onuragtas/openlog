import { LayoutDashboard } from "lucide-react";
import { lazy, Suspense, useState } from "react";
import { useTranslation } from "react-i18next";
import type { DashboardUnit, DashboardVisualization, DashboardWidgetOptions } from "@/api/dashboards";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

// The dialog (and the dashboards API code) loads on first use.
const AddToDashboardDialog = lazy(() => import("./AddToDashboardDialog").then((m) => ({ default: m.AddToDashboardDialog })));

export interface AddToDashboardButtonProps {
  query: string;
  title: string;
  visualization?: DashboardVisualization;
  unit?: DashboardUnit;
  options?: DashboardWidgetOptions;
  /** Why the chart has no OQL equivalent: the button is disabled with this explanation. */
  disabledReason?: string;
  className?: string;
}

/** Icon button that opens "Add to dashboard" for an OQL query (e.g. from a host metric or explorer chart). */
export function AddToDashboardButton({ query, title, visualization = "line", unit, options, disabledReason, className }: AddToDashboardButtonProps) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  if (disabledReason) {
    return (
      <span title={disabledReason} className="inline-flex">
        <Button type="button" variant="ghost" size="icon" className={cn("-my-2 size-7", className)} disabled aria-label={`${t("oql.addToDashboard.button")}: ${disabledReason}`}>
          <LayoutDashboard aria-hidden="true" />
        </Button>
      </span>
    );
  }
  return (
    <>
      <Button
        type="button"
        variant="ghost"
        size="icon"
        className={cn("-my-2 size-7", className)}
        aria-label={`${t("oql.addToDashboard.button")}: ${title}`}
        title={t("oql.addToDashboard.button")}
        onClick={() => setOpen(true)}
      >
        <LayoutDashboard aria-hidden="true" />
      </Button>
      {open && (
        <Suspense fallback={null}>
          <AddToDashboardDialog open={open} onOpenChange={setOpen} query={query} defaultTitle={title} defaultVisualization={visualization} defaultUnit={unit} defaultOptions={options} />
        </Suspense>
      )}
    </>
  );
}

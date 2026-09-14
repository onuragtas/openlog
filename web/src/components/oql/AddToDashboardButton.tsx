import { LayoutDashboard } from "lucide-react";
import { lazy, Suspense, useState } from "react";
import { useTranslation } from "react-i18next";
import type { DashboardVisualization } from "@/api/dashboards";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

// The dialog (and the dashboards API code) loads on first use.
const AddToDashboardDialog = lazy(() => import("./AddToDashboardDialog").then((m) => ({ default: m.AddToDashboardDialog })));

/** Icon button that opens "Add to dashboard" for an OQL query (e.g. from a host metric chart). */
export function AddToDashboardButton({ query, title, visualization = "line", className }: { query: string; title: string; visualization?: DashboardVisualization; className?: string }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
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
          <AddToDashboardDialog open={open} onOpenChange={setOpen} query={query} defaultTitle={title} defaultVisualization={visualization} />
        </Suspense>
      )}
    </>
  );
}

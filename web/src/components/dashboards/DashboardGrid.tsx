import { lazy, Suspense, type ReactNode } from "react";
import type { DashboardWidget } from "@/api/dashboards";
import { GRID_COLS, ROW_HEIGHT, stackOrder, type GridItemLayout } from "@/lib/dashboards";
import { useIsBelowLg } from "@/lib/media";

// react-grid-layout (and its CSS) load only on desktop screens.
const GridLayoutDesktop = lazy(() => import("./GridLayoutDesktop"));

export interface DashboardGridProps {
  widgets: DashboardWidget[];
  /** Drag and resize (desktop only). */
  edit: boolean;
  renderWidget: (widget: DashboardWidget, mode: "grid" | "stacked") => ReactNode;
  onLayoutChange: (layout: GridItemLayout[]) => void;
}

/** Static 12-column CSS grid with the same geometry, shown while the grid chunk loads. */
function StaticGrid({ widgets, renderWidget }: Pick<DashboardGridProps, "widgets" | "renderWidget">) {
  return (
    <div className="grid gap-3" style={{ gridTemplateColumns: `repeat(${GRID_COLS}, minmax(0, 1fr))`, gridAutoRows: `${ROW_HEIGHT}px` }} data-testid="dashboard-grid-static">
      {widgets.map((w) => (
        <div key={w.id} className="min-w-0" style={{ gridColumn: `${w.layout.x + 1} / span ${w.layout.w}`, gridRow: `${w.layout.y + 1} / span ${w.layout.h}` }}>
          {renderWidget(w, "grid")}
        </div>
      ))}
    </div>
  );
}

/**
 * Dashboard widgets on a 12-column grid (row height ROW_HEIGHT). Desktop (≥ lg): react-grid-layout, draggable and
 * resizable in edit mode. Below lg: one column in reading order (y, then x), heights from the layout, no dragging.
 */
export function DashboardGrid({ widgets, edit, renderWidget, onLayoutChange }: DashboardGridProps) {
  const belowLg = useIsBelowLg();
  if (belowLg) {
    return (
      <div className="flex min-w-0 flex-col gap-3" data-testid="dashboard-grid-stacked">
        {stackOrder(widgets).map((w) => (
          <div key={w.id} className="min-w-0">
            {renderWidget(w, "stacked")}
          </div>
        ))}
      </div>
    );
  }
  return (
    <Suspense fallback={<StaticGrid widgets={widgets} renderWidget={renderWidget} />}>
      <GridLayoutDesktop widgets={widgets} edit={edit} renderWidget={renderWidget} onLayoutChange={onLayoutChange} />
    </Suspense>
  );
}

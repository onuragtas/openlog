// Lazy chunk: react-grid-layout and its stylesheet.
import { useMemo } from "react";
import GridLayout from "react-grid-layout";
import "react-grid-layout/css/styles.css";
import { GRID_COLS, ROW_HEIGHT } from "@/lib/dashboards";
import { useElementWidth } from "@/lib/media";
import type { DashboardGridProps } from "./DashboardGrid";

const MARGIN = [12, 12] as const;

export default function GridLayoutDesktop({ widgets, edit, renderWidget, onLayoutChange }: DashboardGridProps) {
  const [ref, width] = useElementWidth<HTMLDivElement>();
  const layout = useMemo(() => widgets.map((w) => ({ i: w.id, x: w.layout.x, y: w.layout.y, w: w.layout.w, h: w.layout.h, minW: 2, minH: 1 })), [widgets]);
  return (
    <div ref={ref} className="min-w-0" data-testid="dashboard-grid-layout" data-edit={edit}>
      <GridLayout
        width={width || 1200}
        layout={layout}
        gridConfig={{ cols: GRID_COLS, rowHeight: ROW_HEIGHT, margin: MARGIN, containerPadding: [0, 0] }}
        dragConfig={{ enabled: edit, handle: ".widget-drag-handle" }}
        resizeConfig={{ enabled: edit }}
        onLayoutChange={(next) => onLayoutChange(next.map(({ i, x, y, w, h }) => ({ i, x, y, w, h })))}
      >
        {widgets.map((w) => (
          <div key={w.id} className="min-w-0">
            {renderWidget(w, "grid")}
          </div>
        ))}
      </GridLayout>
    </div>
  );
}

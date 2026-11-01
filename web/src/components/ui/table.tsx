import { ChevronDown, ChevronsUpDown, ChevronUp } from "lucide-react";
import { createContext, useContext, type ComponentProps } from "react";
import { cn } from "@/lib/utils";

/**
 * Layout below `md`:
 * - "scroll": the table scrolls horizontally inside its container and the first column sticks;
 * - "stack": every row becomes a card; cells with a `label` show it before their value.
 */
export type TableMobileLayout = "scroll" | "stack";

const LayoutContext = createContext<TableMobileLayout>("scroll");

const STICKY_FIRST = "max-md:first:sticky max-md:first:left-0 max-md:first:z-10 max-md:first:bg-card max-md:first:shadow-[1px_0_0_var(--border)]";

export function Table({ className, mobile = "scroll", ...props }: ComponentProps<"table"> & { mobile?: TableMobileLayout }) {
  return (
    <LayoutContext.Provider value={mobile}>
      <div data-slot="table-container" data-mobile={mobile} className="relative w-full overflow-x-auto overscroll-x-contain">
        <table data-slot="table" className={cn("w-full caption-bottom text-sm", mobile === "stack" && "max-md:block", className)} {...props} />
      </div>
    </LayoutContext.Provider>
  );
}

export function TableHeader({ className, ...props }: ComponentProps<"thead">) {
  const layout = useContext(LayoutContext);
  // Cards have no column to head, so the headings go, except the ones that sort: on a phone they are the only way to reorder the list.
  return <thead data-slot="table-header" className={cn("[&_tr]:border-b", layout === "stack" && "max-md:block max-md:[&:not(:has([data-sortable]))]:sr-only", className)} {...props} />;
}

export function TableBody({ className, ...props }: ComponentProps<"tbody">) {
  const layout = useContext(LayoutContext);
  return <tbody data-slot="table-body" className={cn("[&_tr:last-child]:border-0", layout === "stack" && "max-md:block", className)} {...props} />;
}

export function TableRow({ className, ...props }: ComponentProps<"tr">) {
  const layout = useContext(LayoutContext);
  return (
    <tr
      data-slot="table-row"
      className={cn(
        "border-b transition-colors hover:bg-muted/50 data-[state=selected]:bg-muted",
        // Cards: cells take a full line; cells with `max-md:w-auto` (badges) share a line.
        layout === "stack" && "max-md:flex max-md:flex-wrap max-md:items-center max-md:gap-x-2 max-md:gap-y-1.5 max-md:px-4 max-md:py-3",
        className,
      )}
      {...props}
    />
  );
}

export function TableHead({ className, ...props }: ComponentProps<"th">) {
  const layout = useContext(LayoutContext);
  return (
    <th
      data-slot="table-head"
      className={cn(layout === "stack" && !("data-sortable" in props) && "max-md:sr-only", layout === "stack" && "max-md:h-7 max-md:px-1", "h-9 px-3 text-left align-middle text-xs font-medium whitespace-nowrap text-muted-foreground", layout === "scroll" && STICKY_FIRST, className)}
      {...props}
    />
  );
}

export function TableCell({ className, label, ...props }: ComponentProps<"td"> & { label?: string }) {
  const layout = useContext(LayoutContext);
  return (
    <td
      data-slot="table-cell"
      data-label={layout === "stack" ? label : undefined}
      className={cn(
        "px-3 py-2 align-middle",
        // Scrolling tables keep each value on one line (long text is truncated by the cell content).
        layout === "scroll" && `${STICKY_FIRST} max-md:whitespace-nowrap`,
        layout === "stack" && "max-md:block max-md:w-full max-md:max-w-none max-md:p-0 max-md:text-left max-md:whitespace-normal max-md:empty:hidden",
        layout === "stack" && label && "max-md:before:mr-2 max-md:before:text-xs max-md:before:text-muted-foreground max-md:before:content-[attr(data-label)]",
        className,
      )}
      {...props}
    />
  );
}

export type SortDir = "asc" | "desc";

/**
 * A heading that sorts its column. It only reports what was clicked: whoever owns the rows decides where the sorting happens,
 * which for a list that can outgrow a page has to be the server, because a browser can only sort the page it was sent.
 */
export function SortableHead<S extends string>({
  column, sort, dir, onSort, defaultDir = "desc", children, className, ...props
}: Omit<ComponentProps<"th">, "onClick"> & {
  column: S;
  sort: S;
  dir: SortDir;
  onSort: (column: S, dir: SortDir) => void;
  /** The way this column reads best at first: largest first for numbers, A to Z for names. */
  defaultDir?: SortDir;
}) {
  const active = sort === column;
  const Icon = !active ? ChevronsUpDown : dir === "asc" ? ChevronUp : ChevronDown;
  return (
    <TableHead data-sortable="" aria-sort={active ? (dir === "asc" ? "ascending" : "descending") : "none"} className={className} {...props}>
      <button
        type="button"
        className={cn("inline-flex items-center gap-1 hover:text-foreground", active && "text-foreground")}
        onClick={() => onSort(column, active ? (dir === "asc" ? "desc" : "asc") : defaultDir)}
      >
        {children}
        <Icon className={cn("size-3", !active && "opacity-50")} aria-hidden="true" />
      </button>
    </TableHead>
  );
}

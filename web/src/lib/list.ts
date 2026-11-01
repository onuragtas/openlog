// The sort and page of a list whose rows live on the server (api.md "Lists").
import { useCallback, useMemo, useState } from "react";
import type { SortDir } from "@/components/ui/table";

export interface ListState<S extends string> {
  sort: S;
  dir: SortDir;
  limit: number;
  offset: number;
}

export const PAGE_SIZES = [25, 50, 100, 250];

export function useListState<S extends string>(initialSort: S, initialDir: SortDir = "desc", initialLimit = 50) {
  const [state, setState] = useState<ListState<S>>({ sort: initialSort, dir: initialDir, limit: initialLimit, offset: 0 });
  // A new order is a new list, so page three of the old one means nothing.
  const onSort = useCallback((sort: S, dir: SortDir) => setState((s) => ({ ...s, sort, dir, offset: 0 })), []);
  const setOffset = useCallback((offset: number) => setState((s) => ({ ...s, offset: Math.max(0, offset) })), []);
  const setLimit = useCallback((limit: number) => setState((s) => ({ ...s, limit, offset: 0 })), []);
  const toFirstPage = useCallback(() => setState((s) => (s.offset === 0 ? s : { ...s, offset: 0 })), []);
  const head = useMemo(() => ({ sort: state.sort, dir: state.dir, onSort }), [state.sort, state.dir, onSort]);
  return { state, head, setOffset, setLimit, toFirstPage };
}

export type ListControls<S extends string> = ReturnType<typeof useListState<S>>;

/**
 * Sorting and paging of rows that have all been fetched, for a list short enough that the browser holds every row: a list of API
 * keys is not going to outgrow a page of the server's. Anything that can must use useListState and let the server do it.
 */
export function useLocalList<T, S extends string>(rows: readonly T[], read: Record<S, (row: T) => string | number | null | undefined>, initialSort: S, initialDir: SortDir = "asc", initialLimit = 50) {
  const list = useListState<S>(initialSort, initialDir, initialLimit);
  const { sort, dir, limit, offset } = list.state;
  const sorted = useMemo(() => {
    const value = read[sort];
    const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: "base" });
    // Stable, and with what has no value last in either direction: a blank is not the smallest of anything.
    return [...rows].sort((a, b) => {
      const x = value(a);
      const y = value(b);
      if (x === null || x === undefined || x === "") return y === null || y === undefined || y === "" ? 0 : 1;
      if (y === null || y === undefined || y === "") return -1;
      const c = typeof x === "number" && typeof y === "number" ? x - y : collator.compare(String(x), String(y));
      return dir === "asc" ? c : -c;
    });
  }, [rows, read, sort, dir]);
  const visible = useMemo(() => sorted.slice(offset, offset + limit), [sorted, offset, limit]);
  return { ...list, visible, total: sorted.length, hasMore: offset + limit < sorted.length };
}

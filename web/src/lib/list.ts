// The sort and page of a list whose rows live on the server (api.md "Lists").
import { useCallback, useEffect, useMemo, useState } from "react";
import type { SortDir } from "@/components/ui/table";

export interface ListState<S extends string> {
  sort: S;
  dir: SortDir;
  limit: number;
  offset: number;
}

export const PAGE_SIZES = [25, 50, 100, 250];

const URL_PARAM = "list";

/** Every list on a page shares one parameter of the address: `name~sort~dir~limit~offset`, joined by `|`. Lists at their defaults are left out. */
function readUrlLists(): Map<string, string[]> {
  const lists = new Map<string, string[]>();
  for (const entry of (new URLSearchParams(window.location.search).get(URL_PARAM) ?? "").split("|")) {
    const [name, ...rest] = entry.split("~");
    if (name && rest.length === 4) lists.set(name, rest);
  }
  return lists;
}

function writeUrlList(name: string, value: string[] | null) {
  const lists = readUrlLists();
  if (value) lists.set(name, value);
  else lists.delete(name);
  const url = new URL(window.location.href);
  const encoded = [...lists].map(([n, v]) => [n, ...v].join("~")).join("|");
  if (encoded) url.searchParams.set(URL_PARAM, encoded);
  else url.searchParams.delete(URL_PARAM);
  if (url.href !== window.location.href) window.history.replaceState(window.history.state, "", url);
}

/**
 * The state of a list, kept in the address so a link opens the same page in the same order. A value that came from a shared link is only
 * trusted as far as its shape: a sort the list no longer has is refused by the server, and the error says so.
 */
function initialListState<S extends string>(urlKey: string | undefined, sort: S, dir: SortDir, limit: number): ListState<S> {
  const saved = urlKey ? readUrlLists().get(urlKey) : undefined;
  if (!saved) return { sort, dir, limit, offset: 0 };
  const [savedSort, savedDir, savedLimit, savedOffset] = saved;
  const n = Number(savedLimit);
  const o = Number(savedOffset);
  return {
    sort: (savedSort || sort) as S,
    dir: savedDir === "asc" || savedDir === "desc" ? savedDir : dir,
    limit: PAGE_SIZES.includes(n) ? n : limit,
    offset: Number.isInteger(o) && o > 0 ? o : 0,
  };
}

export function useListState<S extends string>(initialSort: S, initialDir: SortDir = "desc", initialLimit = 50, urlKey?: string) {
  const [state, setState] = useState<ListState<S>>(() => initialListState(urlKey, initialSort, initialDir, initialLimit));
  useEffect(() => {
    if (!urlKey) return;
    const isDefault = state.sort === initialSort && state.dir === initialDir && state.limit === initialLimit && state.offset === 0;
    writeUrlList(urlKey, isDefault ? null : [state.sort, state.dir, String(state.limit), String(state.offset)]);
  }, [urlKey, state, initialSort, initialDir, initialLimit]);
  // A new order is a new list, so page three of the old one means nothing.
  const onSort = useCallback((sort: S, dir: SortDir) => setState((s) => ({ ...s, sort, dir, offset: 0 })), []);
  const setOffset = useCallback((offset: number) => setState((s) => ({ ...s, offset: Math.max(0, offset) })), []);
  const setLimit = useCallback((limit: number) => setState((s) => ({ ...s, limit, offset: 0 })), []);
  const toFirstPage = useCallback(() => setState((s) => (s.offset === 0 ? s : { ...s, offset: 0 })), []);
  const head = useMemo(() => ({ sort: state.sort, dir: state.dir, onSort }), [state.sort, state.dir, onSort]);
  return { state, head, setOffset, setLimit, toFirstPage };
}

export type ListControls<S extends string> = ReturnType<typeof useListState<S>>;

/** A timestamp as something to sort by: as text, two of them with a different number of fractional digits compare wrongly. */
export function timeOf(value: string | null | undefined): number | null {
  const ms = value ? Date.parse(value.replace(/(\.\d{3})\d+/, "$1")) : NaN;
  return Number.isNaN(ms) ? null : ms;
}

/**
 * Sorting and paging of rows that have all been fetched, for a list short enough that the browser holds every row: a list of API
 * keys is not going to outgrow a page of the server's. Anything that can must use useListState and let the server do it.
 */
export function useLocalList<T, S extends string>(rows: readonly T[] | undefined, read: Record<S, (row: T) => string | number | null | undefined>, initialSort: NoInfer<S>, initialDir: SortDir = "asc", initialLimit = 50, urlKey?: string) {
  const list = useListState<S>(initialSort, initialDir, initialLimit, urlKey);
  const { sort, dir, limit, offset } = list.state;
  const sorted = useMemo(() => {
    const value = read[sort];
    const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: "base" });
    // Stable, and with what has no value last in either direction: a blank is not the smallest of anything.
    return [...(rows ?? [])].sort((a, b) => {
      const x = value(a);
      const y = value(b);
      if (x === null || x === undefined || x === "") return y === null || y === undefined || y === "" ? 0 : 1;
      if (y === null || y === undefined || y === "") return -1;
      const c = typeof x === "number" && typeof y === "number" ? x - y : collator.compare(String(x), String(y));
      return dir === "asc" ? c : -c;
    });
  }, [rows, read, sort, dir]);
  const { setOffset } = list;
  // Removing the last row of the last page must not leave an empty page with a pager that can only go back.
  useEffect(() => {
    if (offset > 0 && offset >= sorted.length) setOffset(Math.floor((sorted.length - 1) / limit) * limit);
  }, [offset, limit, sorted.length, setOffset]);
  const visible = useMemo(() => sorted.slice(offset, offset + limit), [sorted, offset, limit]);
  return { ...list, visible, total: sorted.length, hasMore: offset + limit < sorted.length };
}

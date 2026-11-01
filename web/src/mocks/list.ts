// Sort and page a mock's rows the way a server list does (api.md "Lists"): what has no value goes last in either direction.
type Keys<T> = Record<string, (row: T) => string | number | null | undefined>;

const blank = (v: unknown) => v === null || v === undefined || v === "";

export function pageOf<T>(url: URL, rows: readonly T[], keys: Keys<T>, opts: { sort: string; asc?: string[]; limit?: number }): { rows: T[]; has_more: boolean } {
  const p = url.searchParams;
  const sort = p.get("sort") ?? opts.sort;
  const read = keys[sort] ?? keys[opts.sort]!;
  const asc = p.has("dir") ? p.get("dir") === "asc" : (opts.asc ?? []).includes(sort);
  const sorted = [...rows].sort((a, b) => {
    const x = read(a);
    const y = read(b);
    if (blank(x)) return blank(y) ? 0 : 1;
    if (blank(y)) return -1;
    const c = typeof x === "number" && typeof y === "number" ? x - y : String(x).localeCompare(String(y));
    return asc ? c : -c;
  });
  const offset = Number(p.get("offset") ?? 0);
  const limit = Number(p.get("limit") ?? opts.limit ?? sorted.length);
  return { rows: sorted.slice(offset, offset + limit), has_more: offset + limit < sorted.length };
}

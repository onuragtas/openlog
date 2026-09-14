// Recently executed OQL queries of this browser (query console). Storage may be unavailable (private mode, quota):
// every access is guarded and failures degrade to an empty history.

export const HISTORY_KEY = "openlog.oql.history";
export const HISTORY_MAX = 50;

export function loadHistory(): string[] {
  try {
    const raw = localStorage.getItem(HISTORY_KEY);
    const v: unknown = raw ? JSON.parse(raw) : [];
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string" && x.trim() !== "").slice(0, HISTORY_MAX) : [];
  } catch {
    return [];
  }
}

function save(list: string[]): void {
  try {
    localStorage.setItem(HISTORY_KEY, JSON.stringify(list));
  } catch {
    // ignore
  }
}

/** Moves (or adds) a query to the top; whitespace-only differences count as the same query. */
export function addToHistory(query: string, current = loadHistory()): string[] {
  const q = query.trim();
  if (!q) return current;
  const norm = (s: string) => s.replace(/\s+/g, " ").trim();
  const list = [q, ...current.filter((x) => norm(x) !== norm(q))].slice(0, HISTORY_MAX);
  save(list);
  return list;
}

export function clearHistory(): string[] {
  save([]);
  return [];
}

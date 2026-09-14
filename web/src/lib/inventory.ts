import type { InventoryItem } from "@/api/types";
import { formatPortKey } from "./ports";

/** Display order of known categories (semantic-conventions §3.3); unknown ones follow alphabetically. */
export const KNOWN_CATEGORIES = [
  "os",
  "hardware",
  "package",
  "process",
  "listening_port",
  "systemd_unit",
  "launchd_service",
  "windows_service",
  "kernel_module",
  "network_interface",
  "mount",
  "user",
  "discovered_service",
] as const;

export interface CategoryCount {
  category: string;
  count: number;
}

export function countByCategory(items: InventoryItem[]): CategoryCount[] {
  const counts = new Map<string, number>();
  for (const it of items) counts.set(it.category, (counts.get(it.category) ?? 0) + 1);
  const rank = (c: string) => {
    const i = (KNOWN_CATEGORIES as readonly string[]).indexOf(c);
    return i === -1 ? KNOWN_CATEGORIES.length : i;
  };
  return [...counts.entries()]
    .map(([category, count]) => ({ category, count }))
    .sort((a, b) => rank(a.category) - rank(b.category) || a.category.localeCompare(b.category));
}

/** Key as displayed: listening ports use the shared "tcp 0.0.0.0:80" format. */
export function displayKey(item: Pick<InventoryItem, "category" | "key">): string {
  return item.category === "listening_port" ? formatPortKey(item.key) : item.key;
}

const searchCache = new WeakMap<object, string>();

/** Lower-cased searchable text for an item: key (raw and displayed) plus the JSON data. */
export function searchText(item: InventoryItem): string {
  let s = searchCache.get(item);
  if (s === undefined) {
    const data = typeof item.data === "string" ? item.data : JSON.stringify(item.data ?? "");
    const shown = displayKey(item);
    s = `${item.key}\n${shown === item.key ? "" : `${shown}\n`}${data}`.toLowerCase();
    searchCache.set(item, s);
  }
  return s;
}

/**
 * Filters items by category ("" = all) and a query. The query is split on
 * whitespace; every term must occur in the key or the data (case-insensitive).
 */
export function filterInventory(items: InventoryItem[], category: string, query: string): InventoryItem[] {
  const terms = query.toLowerCase().split(/\s+/).filter(Boolean);
  return items.filter((it) => {
    if (category && it.category !== category) return false;
    if (terms.length === 0) return true;
    const text = searchText(it);
    return terms.every((t) => text.includes(t));
  });
}

/** Short one-line summary of an item's data for the table. */
export function summarize(data: unknown): string {
  if (data === null || data === undefined) return "";
  if (typeof data !== "object") return String(data);
  const o = data as Record<string, unknown>;
  const pick = ["version", "description", "process_name", "exe", "state", "enabled_state", "fs_type", "shell", "model", "pretty_name", "operstate"];
  const parts: string[] = [];
  for (const k of pick) {
    const v = o[k];
    if (v !== undefined && v !== null && v !== "") parts.push(`${k}=${String(v)}`);
    if (parts.length >= 3) break;
  }
  return parts.join("  ");
}

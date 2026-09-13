import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}

/** Resource attribute keys shown as dedicated host fields rather than chips. */
const WELL_KNOWN_PREFIXES = ["host.", "os.", "openlog.", "service.", "telemetry."];

export function extraAttributes(attrs: Record<string, string>): [string, string][] {
  return Object.entries(attrs)
    .filter(([k]) => !WELL_KNOWN_PREFIXES.some((p) => k.startsWith(p)))
    .sort(([a], [b]) => a.localeCompare(b));
}

// Same hues in both themes; the dark variants keep ≥ 6:1 against dark cards
// (the light ones drop below 3:1 there).
const PALETTE = ["#2563eb", "#16a34a", "#d97706", "#dc2626", "#7c3aed", "#0891b2", "#db2777", "#65a30d", "#ea580c", "#4f46e5"];
const PALETTE_DARK = ["#60a5fa", "#4ade80", "#fbbf24", "#f87171", "#a78bfa", "#22d3ee", "#f472b6", "#a3e635", "#fb923c", "#818cf8"];

type Theme = "light" | "dark";
const paletteFor = (theme: Theme) => (theme === "dark" ? PALETTE_DARK : PALETTE);

/** Stable color for a string (service name, series label). */
export function colorFor(key: string, theme: Theme = "light"): string {
  let h = 2166136261;
  for (let i = 0; i < key.length; i++) {
    h ^= key.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  const p = paletteFor(theme);
  return p[Math.abs(h) % p.length]!;
}

export function paletteColor(i: number, theme: Theme = "light"): string {
  const p = paletteFor(theme);
  return p[i % p.length]!;
}

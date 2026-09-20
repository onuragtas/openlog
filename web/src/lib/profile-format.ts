/**
 * formatValue prints a profile value in the unit the profile itself declared (docs/contracts/profiles.md §5).
 *
 * The API never invents a unit — it repeats the one the profile's own ValueType carried — so neither does the
 * UI: an unrecognised unit is printed as a plain number with the unit appended, rather than guessed at and
 * silently rendered as milliseconds.
 */
export function formatValue(value: number, unit: string, lang: string): string {
  const n = (v: number, digits = 1) => new Intl.NumberFormat(lang, { maximumFractionDigits: digits }).format(v);
  if (unit === "nanoseconds") {
    if (value < 1_000) return `${n(value, 0)} ns`;
    if (value < 1_000_000) return `${n(value / 1_000)} µs`;
    if (value < 1_000_000_000) return `${n(value / 1_000_000)} ms`;
    return `${n(value / 1_000_000_000, 2)} s`;
  }
  if (unit === "bytes") {
    if (value < 1024) return `${n(value, 0)} B`;
    if (value < 1024 ** 2) return `${n(value / 1024)} KiB`;
    if (value < 1024 ** 3) return `${n(value / 1024 ** 2)} MiB`;
    return `${n(value / 1024 ** 3, 2)} GiB`;
  }
  const suffix = unit ? ` ${unit}` : "";
  return `${n(value, 0)}${suffix}`;
}

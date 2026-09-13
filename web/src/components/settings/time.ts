/** Parses API timestamps (RFC3339 with nanoseconds) to epoch milliseconds. */
export function parseApiTime(v: string): number {
  return Date.parse(v.replace(/(\.\d{3})\d+/, "$1"));
}

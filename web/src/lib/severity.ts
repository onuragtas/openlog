/** Severity names accepted by the logs API `severity_min` parameter. */
export const SEVERITIES = ["TRACE", "DEBUG", "INFO", "WARN", "ERROR", "FATAL"] as const;

/**
 * Label of a log record's severity: its severity_text, otherwise the OpenTelemetry name of its
 * severity_number range (1–4 TRACE … 21–24 FATAL, with the step suffix: 10 → INFO2). Records
 * without any severity (number 0, e.g. access log lines without a level word) have no label.
 */
export function severityLabel(text: string | null | undefined, n: number): string | null {
  const t = text?.trim();
  if (t) return t;
  if (!Number.isInteger(n) || n < 1 || n > 24) return null;
  const name = SEVERITIES[Math.floor((n - 1) / 4)];
  if (!name) return null;
  const step = ((n - 1) % 4) + 1;
  return step === 1 ? name : `${name}${step}`;
}

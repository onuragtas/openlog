import { useTranslation } from "react-i18next";
import type { ValidationIssue } from "@/lib/alerts";

/** aria attributes linking a control to its error (preferred) or hint paragraph rendered by <Field>. */
export function describedBy(id: string, error?: string, hint?: string): { "aria-invalid"?: boolean; "aria-describedby"?: string } {
  if (error) return { "aria-invalid": true, "aria-describedby": `${id}-error` };
  return hint ? { "aria-describedby": `${id}-hint` } : {};
}

type Translate = (key: string, options?: Record<string, unknown>) => string;

/** Translates a validation issue (alerts.validation.*). */
export function useIssue(): (issue: ValidationIssue | undefined) => string | undefined {
  const { t } = useTranslation();
  const translate = t as unknown as Translate;
  return (issue) => (issue ? translate(`alerts.validation.${issue.key}`, issue.params ?? {}) : undefined);
}

import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ApiError } from "@/api/client";
import { formatDateTime, formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { parseApiTime } from "./time";

export function DateTimeText({ value, relative = false }: { value: string | null | undefined; relative?: boolean }) {
  const { t, i18n } = useTranslation();
  const now = useNow();
  const locale = i18n.resolvedLanguage ?? "en";
  if (!value) return <span className="text-muted-foreground">{t("settings.never")}</span>;
  const ms = parseApiTime(value);
  const absolute = formatDateTime(ms, locale);
  if (!relative) return <time dateTime={value}>{absolute}</time>;
  return (
    <time dateTime={value} title={absolute}>
      {formatRelative(ms, now, locale)}
    </time>
  );
}

export function FormError({ error }: { error: unknown }) {
  const { t } = useTranslation();
  if (!error) return null;
  const message =
    error instanceof ApiError ? error.message : error instanceof Error ? error.message : t("common.error");
  return (
    <p role="alert" className="text-sm text-destructive">
      {message}
    </p>
  );
}

export function SettingsSection({ title, description, children }: { title: string; description?: string; children?: ReactNode }) {
  return (
    <section className="rounded-xl border bg-card p-4">
      <h2 className="text-base font-semibold">{title}</h2>
      {description && <p className="mt-1 text-sm text-muted-foreground">{description}</p>}
      {children && <div className="mt-4 flex flex-col gap-3">{children}</div>}
    </section>
  );
}

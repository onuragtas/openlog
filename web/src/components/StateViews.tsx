import { AlertTriangle, Inbox, Loader2 } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ApiError } from "@/api/client";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export function LoadingState({ className, label }: { className?: string; label?: string }) {
  const { t } = useTranslation();
  return (
    <div role="status" aria-live="polite" className={cn("flex items-center justify-center gap-2 py-10 text-sm text-muted-foreground", className)}>
      <Loader2 className="size-4 animate-spin" aria-hidden="true" />
      {label ?? t("common.loading")}
    </div>
  );
}

export function ErrorState({ error, onRetry, className, title }: { error: unknown; onRetry?: () => void; className?: string; title?: string }) {
  const { t } = useTranslation();
  const detail =
    error instanceof ApiError
      ? t("common.errorDetail", { message: error.message, code: error.code })
      : error instanceof Error
        ? error.message
        : String(error);
  return (
    <div role="alert" className={cn("flex flex-col items-center justify-center gap-2 py-10 text-center text-sm", className)}>
      <AlertTriangle className="size-5 text-destructive" aria-hidden="true" />
      <p className="font-medium">{title ?? t("common.error")}</p>
      <p className="max-w-prose text-muted-foreground">{detail}</p>
      {onRetry && (
        <Button variant="outline" size="sm" onClick={onRetry}>
          {t("common.retry")}
        </Button>
      )}
    </div>
  );
}

export function EmptyState({ children, className, icon }: { children: ReactNode; className?: string; icon?: ReactNode }) {
  return (
    <div className={cn("flex flex-col items-center justify-center gap-2 py-10 text-center text-sm text-muted-foreground", className)}>
      {icon ?? <Inbox className="size-5" aria-hidden="true" />}
      <div className="max-w-prose">{children}</div>
    </div>
  );
}

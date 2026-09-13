import { CircleAlert, CircleCheck, CircleMinus, CircleX } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import type { IntegrationStatus } from "@/lib/integrations";

const VARIANT: Record<IntegrationStatus, "success" | "warning" | "destructive" | "muted"> = {
  enabled: "success",
  needs_configuration: "warning",
  error: "destructive",
  not_available: "muted",
};

const ICON = { enabled: CircleCheck, needs_configuration: CircleAlert, error: CircleX, not_available: CircleMinus } as const;

/** Integration status with an icon per state (not color alone). */
export function IntegrationStatusBadge({ status, label, title }: { status: IntegrationStatus; label?: string; title?: string }) {
  const { t } = useTranslation();
  const Icon = ICON[status];
  return (
    <Badge variant={VARIANT[status]} title={title} data-status={status}>
      <Icon aria-hidden="true" />
      {label ?? t(`integrations.status.${status}`)}
    </Badge>
  );
}

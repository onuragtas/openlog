import { Bell, BellOff, Mail, MessageSquare, Users, Webhook } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { AlertChannelType, AlertIncidentState, AlertRule, AlertSeverity } from "@/api/alerts";
import { Badge } from "@/components/ui/badge";

export function SeverityBadge({ severity }: { severity: AlertSeverity }) {
  const { t } = useTranslation();
  const variant = severity === "critical" ? "destructive" : severity === "warning" ? "warning" : "muted";
  return <Badge variant={variant}>{t(`alerts.severity.${severity}`)}</Badge>;
}

export function IncidentStateBadge({ state }: { state: AlertIncidentState }) {
  const { t } = useTranslation();
  const variant = state === "open" ? "destructive" : state === "acknowledged" ? "warning" : "success";
  return <Badge variant={variant}>{t(`alerts.incidentState.${state}`)}</Badge>;
}

export function RuleStateBadge({ state }: { state: AlertRule["status"]["state"] }) {
  const { t } = useTranslation();
  const variant = state === "firing" || state === "error" ? "destructive" : state === "pending" ? "warning" : state === "ok" ? "success" : "muted";
  return (
    <Badge variant={variant} data-testid="rule-state">
      {state === "disabled" ? <BellOff aria-hidden="true" /> : <Bell aria-hidden="true" />}
      {t(`alerts.ruleState.${state}`)}
    </Badge>
  );
}

const CHANNEL_ICONS: Record<AlertChannelType, typeof Mail> = { slack: MessageSquare, email: Mail, webhook: Webhook, teams: Users };

export function ChannelTypeIcon({ type, className }: { type: AlertChannelType; className?: string }) {
  const Icon = CHANNEL_ICONS[type];
  return <Icon aria-hidden="true" className={className ?? "size-4 text-muted-foreground"} />;
}

export function ChannelTypeLabel({ type }: { type: AlertChannelType }) {
  const { t } = useTranslation();
  return (
    <span className="inline-flex items-center gap-1.5">
      <ChannelTypeIcon type={type} />
      {t(`alerts.channels.types.${type}`)}
    </span>
  );
}

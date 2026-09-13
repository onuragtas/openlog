import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Plus } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { alertChannelsQuery, alertRulesQuery, deleteAlertRule, setAlertRuleEnabled, type AlertRule } from "@/api/alerts";
import { can } from "@/api/roles";
import { ConfirmAction } from "@/components/fleet/ConfirmAction";
import { DateTimeText, FormError } from "@/components/settings/common";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { buttonVariants } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { canEditOwned } from "@/lib/alerts";
import { ChannelTypeIcon, RuleStateBadge, SeverityBadge } from "./badges";

function RuleRow({ rule, editable, channelTypes }: { rule: AlertRule; editable: boolean; channelTypes: Map<string, { name: string; type: "slack" | "email" | "webhook" | "teams" }> }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["alerts"] });
  const toggle = useMutation({ mutationFn: () => setAlertRuleEnabled(rule.id, !rule.enabled), onSuccess: refresh });
  const remove = useMutation({ mutationFn: () => deleteAlertRule(rule.id), onSuccess: refresh });
  return (
    <TableRow data-testid="rule-row">
      <TableCell>
        <Link to="/alerts/rules/$ruleId" params={{ ruleId: rule.id }} className="font-medium hover:underline">
          {rule.name}
        </Link>
        {rule.status.last_error && <p className="max-w-sm truncate text-xs text-destructive-text">{rule.status.last_error}</p>}
      </TableCell>
      <TableCell label={t("alerts.rules.columns.type")} className="whitespace-nowrap">
        {t(`alerts.types.${rule.type}`)}
      </TableCell>
      <TableCell className="max-md:w-auto">
        <SeverityBadge severity={rule.severity} />
      </TableCell>
      <TableCell className="max-md:w-auto">
        <RuleStateBadge state={rule.status.state} />
      </TableCell>
      <TableCell className="max-md:w-auto">
        <span className="inline-flex items-center gap-1">
          {rule.channel_ids.map((id) => {
            const c = channelTypes.get(id);
            return c ? (
              <span key={id} title={c.name}>
                <ChannelTypeIcon type={c.type} />
                <span className="sr-only">{c.name}</span>
              </span>
            ) : null;
          })}
        </span>
      </TableCell>
      <TableCell label={t("alerts.rules.columns.evaluated")} className="whitespace-nowrap text-sm">
        {rule.status.last_evaluated_at ? <DateTimeText value={rule.status.last_evaluated_at} relative /> : <span className="text-muted-foreground">{t("alerts.rules.notEvaluated")}</span>}
      </TableCell>
      <TableCell>
        {editable && (
          <div className="flex flex-wrap items-center justify-end gap-2 max-md:justify-start">
            <label className="inline-flex items-center gap-2 text-sm">
              <input type="checkbox" role="switch" aria-label={t("alerts.rules.toggle", { name: rule.name })} checked={rule.enabled} disabled={toggle.isPending} onChange={() => toggle.mutate()} />
              <span className="sr-only lg:not-sr-only">{rule.enabled ? t("alerts.rules.disable") : t("alerts.rules.enable")}</span>
            </label>
            <ConfirmAction label={t("alerts.rules.delete")} confirmLabel={t("alerts.rules.confirmDelete")} destructive pending={remove.isPending} onConfirm={() => remove.mutate()} />
            <FormError error={toggle.error ?? remove.error} />
          </div>
        )}
      </TableCell>
    </TableRow>
  );
}

export function RulesList() {
  const { t } = useTranslation();
  const me = useMe().data;
  const rules = useQuery(alertRulesQuery());
  const channels = useQuery(alertChannelsQuery());
  const channelTypes = new Map((channels.data?.channels ?? []).map((c) => [c.id, { name: c.name, type: c.type }] as const));
  return (
    <div className="flex flex-col gap-3">
      {can(me?.role, "alerts.write") && (
        <div>
          <Link to="/alerts/rules/new" className={buttonVariants({})}>
            <Plus aria-hidden="true" />
            {t("alerts.rules.new")}
          </Link>
        </div>
      )}
      {rules.isPending ? (
        <LoadingState />
      ) : rules.isError ? (
        <ErrorState error={rules.error} onRetry={() => void rules.refetch()} />
      ) : rules.data.length === 0 ? (
        <EmptyState>{t("alerts.rules.empty")}</EmptyState>
      ) : (
        <div className="rounded-xl border bg-card">
          <Table mobile="stack">
            <TableHeader>
              <TableRow>
                <TableHead>{t("alerts.rules.columns.name")}</TableHead>
                <TableHead>{t("alerts.rules.columns.type")}</TableHead>
                <TableHead>{t("alerts.rules.columns.severity")}</TableHead>
                <TableHead>{t("alerts.rules.columns.state")}</TableHead>
                <TableHead>{t("alerts.rules.columns.channels")}</TableHead>
                <TableHead>{t("alerts.rules.columns.evaluated")}</TableHead>
                <TableHead>
                  <span className="sr-only">{t("alerts.rules.delete")}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rules.data.map((r) => (
                <RuleRow key={r.id} rule={r} editable={canEditOwned(me?.role, r.created_by_user_id, me?.user?.id)} channelTypes={channelTypes} />
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  );
}

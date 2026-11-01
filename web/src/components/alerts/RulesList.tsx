import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Plus } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import { alertChannelsQuery, alertRulesQuery, deleteAlertRule, setAlertRuleEnabled, type AlertChannelType, type AlertRule } from "@/api/alerts";
import { ConfirmAction } from "@/components/fleet/ConfirmAction";
import { ListPager } from "@/components/ListPager";
import { DateTimeText, FormError } from "@/components/settings/common";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { buttonVariants } from "@/components/ui/button";
import { SortableHead, Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { canEditOwned } from "@/lib/alerts";
import { timeOf, useLocalList } from "@/lib/list";
import { usePermissions } from "@/lib/org-writable";
import { ChannelTypeIcon, RuleStateBadge, SeverityBadge } from "./badges";

function RuleRow({ rule, editable, channelTypes }: { rule: AlertRule; editable: boolean; channelTypes: Map<string, { name: string; type: AlertChannelType }> }) {
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

const SEVERITY_RANK: Record<string, number> = { info: 0, warning: 1, critical: 2 };
const STATE_RANK: Record<string, number> = { disabled: 0, unknown: 1, ok: 2, pending: 3, firing: 4, error: 5 };

export function RulesList() {
  const { t } = useTranslation();
  const me = useMe().data;
  const perms = usePermissions();
  const rules = useQuery(alertRulesQuery());
  const channels = useQuery(alertChannelsQuery());
  const columns = useMemo(
    () => ({
      name: (r: AlertRule) => r.name,
      // What the column shows, since the names of the types sort differently from their keys.
      type: (r: AlertRule) => t(`alerts.types.${r.type}`),
      severity: (r: AlertRule) => SEVERITY_RANK[r.severity],
      state: (r: AlertRule) => STATE_RANK[r.status.state],
      channels: (r: AlertRule) => r.channel_ids.length,
      evaluated: (r: AlertRule) => timeOf(r.status.last_evaluated_at),
    }),
    [t],
  );
  const list = useLocalList(rules.data, columns, "name");
  const channelTypes = new Map((channels.data?.channels ?? []).map((c) => [c.id, { name: c.name, type: c.type }] as const));
  return (
    <div className="flex flex-col gap-3">
      {perms.can("alerts.write") && (
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
                <SortableHead column="name" defaultDir="asc" {...list.head}>{t("alerts.rules.columns.name")}</SortableHead>
                <SortableHead column="type" defaultDir="asc" {...list.head}>{t("alerts.rules.columns.type")}</SortableHead>
                <SortableHead column="severity" {...list.head}>{t("alerts.rules.columns.severity")}</SortableHead>
                <SortableHead column="state" {...list.head}>{t("alerts.rules.columns.state")}</SortableHead>
                <SortableHead column="channels" {...list.head}>{t("alerts.rules.columns.channels")}</SortableHead>
                <SortableHead column="evaluated" {...list.head}>{t("alerts.rules.columns.evaluated")}</SortableHead>
                <TableHead>
                  <span className="sr-only">{t("alerts.rules.delete")}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.visible.map((r) => (
                <RuleRow key={r.id} rule={r} editable={perms.writable && canEditOwned(me?.role, r.created_by_user_id, me?.user?.id)} channelTypes={channelTypes} />
              ))}
            </TableBody>
          </Table>
          <ListPager list={list} count={list.visible.length} hasMore={list.hasMore} />
        </div>
      )}
    </div>
  );
}

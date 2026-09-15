import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  adminCancelOrgDeletion,
  adminOrgDeletionsQuery,
  adminScheduleOrgDeletion,
  deletionCertificatesQuery,
  type DeletionCertificate,
  type OrgDeletion,
} from "@/api/privacy";
import { ConfirmButton } from "@/components/settings/ConfirmButton";
import { DateTimeText, FormError, SettingsSection } from "@/components/settings/common";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

interface OrgRef {
  id: string;
  tenant_id: string;
  name: string;
}

const ACTIVE = new Set<OrgDeletion["status"]>(["scheduled", "deleting"]);

/** Operator console → organization → deletion: schedule (reason, optional immediate), pending state, cancel, certificates (D-107). */
export function OperatorDeletionSection({ org }: { org: OrgRef }) {
  const { t } = useTranslation();
  const deletions = useQuery(adminOrgDeletionsQuery());
  const mine = (deletions.data ?? []).filter((d) => d.organization_id === org.id);
  const active = mine.find((d) => ACTIVE.has(d.status));
  return (
    <SettingsSection title={t("operator.deletion.title")} description={t("operator.deletion.description")}>
      {deletions.isPending ? (
        <LoadingState />
      ) : deletions.isError ? (
        <ErrorState error={deletions.error} onRetry={() => void deletions.refetch()} />
      ) : active ? (
        <PendingDeletion deletion={active} />
      ) : (
        <ScheduleDeletion org={org} />
      )}
      {mine.some((d) => !ACTIVE.has(d.status)) && (
        <div>
          <h3 className="mb-1 text-sm font-medium">{t("operator.deletion.history")}</h3>
          <ul className="flex flex-col divide-y text-sm">
            {mine
              .filter((d) => !ACTIVE.has(d.status))
              .map((d) => (
                <li key={d.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 py-1.5">
                  <Badge variant="muted">{t(`operator.deletion.status.${d.status}`)}</Badge>
                  <DateTimeText value={d.requested_at} />
                  {d.reason && <span className="break-words text-muted-foreground">{d.reason}</span>}
                </li>
              ))}
          </ul>
        </div>
      )}
      <Certificates tenantId={org.tenant_id} />
    </SettingsSection>
  );
}

function PendingDeletion({ deletion: d }: { deletion: OrgDeletion }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const cancel = useMutation({
    mutationFn: () => adminCancelOrgDeletion(d.id),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["operator"] }),
  });
  return (
    <div role="status" data-testid="operator-deletion-pending" className="flex flex-col gap-2 rounded-md border border-destructive/50 bg-destructive/10 px-3 py-2 text-sm">
      <p className="font-medium">
        {d.status === "deleting" ? t("operator.deletion.deleting") : t("operator.deletion.scheduled")} <DateTimeText value={d.purge_after} />
      </p>
      <dl className="grid grid-cols-1 gap-x-4 gap-y-1 sm:grid-cols-[10rem_1fr]">
        <dt className="text-muted-foreground">{t("operator.deletion.initiator")}</dt>
        <dd>
          {t(`operator.deletion.initiators.${d.initiator}`)}
          {d.requested_by_email && <span className="text-muted-foreground"> ({d.requested_by_email})</span>}
        </dd>
        <dt className="text-muted-foreground">{t("operator.deletion.requested")}</dt>
        <dd>
          <DateTimeText value={d.requested_at} />
        </dd>
        {d.reason && (
          <>
            <dt className="text-muted-foreground">{t("operator.deletion.reasonLabel")}</dt>
            <dd className="break-words">{d.reason}</dd>
          </>
        )}
        {d.last_error && (
          <>
            <dt className="text-muted-foreground">{t("operator.deletion.lastError")}</dt>
            <dd className="break-words text-destructive">{d.last_error}</dd>
          </>
        )}
      </dl>
      {d.cancellable ? (
        <div>
          <ConfirmButton label={t("operator.deletion.cancel")} confirmLabel={t("operator.deletion.confirmCancel")} pending={cancel.isPending} onConfirm={() => cancel.mutate()} />
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">{t("operator.deletion.notCancellable")}</p>
      )}
      <FormError error={cancel.error} />
    </div>
  );
}

function ScheduleDeletion({ org }: { org: OrgRef }) {
  const { t } = useTranslation();
  const id = useId();
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const [immediate, setImmediate] = useState(false);
  const [confirm, setConfirm] = useState("");
  const schedule = useMutation({
    mutationFn: () => adminScheduleOrgDeletion(org.id, reason.trim(), immediate),
    onSuccess: () => {
      setOpen(false);
      setReason("");
      setImmediate(false);
      setConfirm("");
      void qc.invalidateQueries({ queryKey: ["operator"] });
    },
  });
  const ready = reason.trim().length >= 3 && (!immediate || confirm.trim() === org.tenant_id);
  if (!open) {
    return (
      <div>
        <Button type="button" size="sm" variant="destructive" onClick={() => setOpen(true)}>
          {t("operator.deletion.schedule")}
        </Button>
      </div>
    );
  }
  return (
    <form
      aria-label={t("operator.deletion.schedule")}
      className="flex flex-col gap-2 rounded-lg border border-destructive/40 p-3"
      onSubmit={(e) => {
        e.preventDefault();
        if (ready) schedule.mutate();
      }}
    >
      <p className="text-sm text-muted-foreground">{t("operator.deletion.warning", { name: org.name })}</p>
      <label htmlFor={`${id}-reason`} className="text-xs text-muted-foreground">
        {t("operator.actions.reason")}
      </label>
      <textarea
        id={`${id}-reason`}
        required
        minLength={3}
        maxLength={1000}
        rows={2}
        autoFocus
        value={reason}
        onChange={(e) => setReason(e.target.value)}
        className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm pointer-coarse:text-base"
      />
      <label className="inline-flex items-center gap-2 text-sm">
        <input type="checkbox" checked={immediate} onChange={(e) => setImmediate(e.target.checked)} />
        {t("operator.deletion.immediate")}
      </label>
      {immediate && (
        <>
          <p role="note" className="text-xs text-destructive">
            {t("operator.deletion.immediateWarning")}
          </p>
          <label htmlFor={`${id}-confirm`} className="text-xs text-muted-foreground">
            {t("operator.deletion.confirmTenant", { tenant: org.tenant_id })}
          </label>
          <Input id={`${id}-confirm`} autoComplete="off" value={confirm} onChange={(e) => setConfirm(e.target.value)} className="max-w-xs" />
        </>
      )}
      <div className="flex flex-wrap gap-2">
        <Button type="submit" size="sm" variant="destructive" disabled={!ready || schedule.isPending}>
          {immediate ? t("operator.deletion.confirmImmediate") : t("operator.deletion.confirmSchedule")}
        </Button>
        <Button
          type="button"
          size="sm"
          variant="ghost"
          onClick={() => {
            setOpen(false);
            schedule.reset();
          }}
        >
          {t("common.cancel")}
        </Button>
      </div>
      <FormError error={schedule.error} />
    </form>
  );
}

function rowTotal(rows: Record<string, number>): number {
  return Object.values(rows).reduce((a, b) => a + b, 0);
}

function Certificates({ tenantId }: { tenantId: string }) {
  const { t } = useTranslation();
  const q = useQuery(deletionCertificatesQuery(tenantId));
  return (
    <div>
      <h3 className="mb-1 text-sm font-medium">{t("operator.deletion.certificates")}</h3>
      {q.isPending ? (
        <LoadingState />
      ) : q.isError ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      ) : q.data.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("operator.deletion.noCertificates")}</p>
      ) : (
        <ul className="flex flex-col divide-y text-sm" data-testid="deletion-certificates">
          {q.data.map((c: DeletionCertificate) => (
            <li key={c.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 py-1.5">
              <code className="font-mono text-xs break-all">{c.id}</code>
              <Badge variant={c.verified ? "success" : "warning"}>{c.verified ? t("operator.deletion.verified") : t("operator.deletion.unverified")}</Badge>
              <span className="text-muted-foreground">{t(`operator.deletion.initiators.${c.initiator}`)}</span>
              <span className="tabular-nums text-muted-foreground">
                {t("operator.deletion.rows", { postgres: rowTotal(c.postgres_rows), clickhouse: rowTotal(c.clickhouse_rows) })}
              </span>
              <span className="ml-auto text-xs text-muted-foreground">
                <DateTimeText value={c.completed_at} />
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

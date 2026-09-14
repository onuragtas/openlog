import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Pencil, Plus, RefreshCw } from "lucide-react";
import { useTranslation } from "react-i18next";
import {
  connectionInput,
  deleteSsoConnectionById,
  refreshSsoConnection,
  ssoConnectionLabel,
  storeSsoState,
  updateSsoConnection,
  type SsoConnection,
  type SsoHealthStatus,
  type SsoState,
} from "@/api/sso";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ConfirmButton } from "./ConfirmButton";
import { DateTimeText, FormError, SettingsSection } from "./common";

const HEALTH_VARIANT: Record<SsoHealthStatus, "success" | "warning" | "destructive" | "outline"> = {
  ok: "success",
  warning: "warning",
  error: "destructive",
  unknown: "outline",
};

function ConnectionRow({ c, editing, onEdit, onDeleted }: { c: SsoConnection; editing: boolean; onEdit: () => void; onDeleted: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toggle = useMutation({
    mutationFn: () => updateSsoConnection(c.id, connectionInput(c, { enabled: !c.enabled })),
    onSuccess: (res) => storeSsoState(qc, res),
  });
  const refresh = useMutation({ mutationFn: () => refreshSsoConnection(c.id), onSuccess: (res) => storeSsoState(qc, res) });
  const remove = useMutation({
    mutationFn: () => deleteSsoConnectionById(c.id),
    onSuccess: onDeleted,
    onSettled: () => void qc.invalidateQueries({ queryKey: ["settings", "sso"] }),
  });
  const label = ssoConnectionLabel(c);

  return (
    <li className="flex flex-col gap-2 rounded-lg border p-3" data-testid={`sso-connection-${c.id}`} aria-current={editing ? "true" : undefined}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="min-w-0 font-medium break-all">{label}</span>
        <Badge variant="muted">{c.protocol === "oidc" ? t("sso.connection.oidc") : t("sso.connection.saml")}</Badge>
        {c.default && <Badge variant="secondary">{t("sso.connections.default")}</Badge>}
        {c.enabled ? <Badge variant="success">{t("sso.connections.enabled")}</Badge> : <Badge variant="outline">{t("sso.connections.disabled")}</Badge>}
        {c.enforce && <Badge variant="warning">{t("sso.connections.enforced")}</Badge>}
        <Badge variant={HEALTH_VARIANT[c.health.status]}>{t(`sso.connections.health.${c.health.status}`)}</Badge>
      </div>
      {c.health.message && <p className="text-sm break-all text-muted-foreground">{c.health.message}</p>}
      <p className="text-xs text-muted-foreground">
        {t("sso.connections.lastChecked")}: <DateTimeText value={c.health.checked_at} relative />
      </p>
      <div className="flex flex-wrap items-center gap-2">
        <Button type="button" size="sm" variant="outline" onClick={onEdit} aria-label={`${t("sso.connections.edit")} ${label}`}>
          <Pencil aria-hidden="true" />
          {t("sso.connections.edit")}
        </Button>
        <Button type="button" size="sm" variant="outline" disabled={toggle.isPending} onClick={() => toggle.mutate()}>
          {c.enabled ? t("sso.connections.disable") : t("sso.connections.enable")}
        </Button>
        <Button type="button" size="sm" variant="outline" disabled={refresh.isPending} onClick={() => refresh.mutate()}>
          <RefreshCw aria-hidden="true" className={refresh.isPending ? "animate-spin" : undefined} />
          {t("sso.connections.refresh")}
        </Button>
        <ConfirmButton label={t("sso.connection.delete")} confirmLabel={t("sso.connection.confirmDelete")} pending={remove.isPending} onConfirm={() => remove.mutate()} />
      </div>
      <FormError error={toggle.error} />
      <FormError error={refresh.error} />
      <FormError error={remove.error} />
    </li>
  );
}

/** The organization's SSO connections: status, health and quick actions; editing opens the wizard. */
export function SsoConnections({
  state,
  editingId,
  onEdit,
  onAdd,
  onDeleted,
}: {
  state: SsoState;
  editingId: string | null;
  onEdit: (id: string) => void;
  onAdd: () => void;
  onDeleted: (id: string) => void;
}) {
  const { t } = useTranslation();
  return (
    <SettingsSection title={t("sso.connections.title")} description={t("sso.connections.description")}>
      <ul aria-label={t("sso.connections.listLabel")} className="flex flex-col gap-2">
        {state.connections.map((c) => (
          <ConnectionRow key={c.id} c={c} editing={c.id === editingId} onEdit={() => onEdit(c.id)} onDeleted={() => onDeleted(c.id)} />
        ))}
      </ul>
      <div>
        <Button type="button" variant="outline" disabled={!state.available} onClick={onAdd}>
          <Plus aria-hidden="true" />
          {t("sso.connections.add")}
        </Button>
      </div>
    </SettingsSection>
  );
}

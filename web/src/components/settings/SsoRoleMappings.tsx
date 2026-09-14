import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2 } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { replaceSsoRoleMappingsFor, ssoConnectionLabel, ssoRoleMappingsQuery, type SsoRoleMapping, type SsoState } from "@/api/sso";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { FormError, SettingsSection } from "./common";

const ROLES = ["admin", "member", "viewer"] as const;

function MappingsEditor({ connectionId }: { connectionId: string | null }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const query = useQuery(ssoRoleMappingsQuery(connectionId));
  const [edited, setEdited] = useState<SsoRoleMapping[] | null>(null);
  const rows = edited ?? query.data ?? [];
  const update = (i: number, patch: Partial<SsoRoleMapping>) => setEdited(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const save = useMutation({
    mutationFn: () => replaceSsoRoleMappingsFor(connectionId, rows.filter((r) => r.group.trim() !== "").map((r) => ({ ...r, group: r.group.trim() }))),
    onSuccess: (saved) => {
      qc.setQueryData(ssoRoleMappingsQuery(connectionId).queryKey, saved);
      setEdited(null);
    },
  });

  if (query.isPending) return <LoadingState />;
  if (query.isError) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  return (
    <form
      className="flex flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      {rows.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("sso.mappings.empty")}</p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t("sso.mappings.group")}</TableHead>
              <TableHead className="w-40">{t("sso.mappings.role")}</TableHead>
              <TableHead className="w-12">
                <span className="sr-only">{t("settings.columns.actions")}</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((r, i) => (
              <TableRow key={i}>
                <TableCell>
                  <Input aria-label={`${t("sso.mappings.group")} ${i + 1}`} value={r.group} maxLength={512} onChange={(e) => update(i, { group: e.target.value })} />
                </TableCell>
                <TableCell>
                  <NativeSelect aria-label={`${t("sso.mappings.role")} ${i + 1}`} value={r.role} onChange={(e) => update(i, { role: e.target.value as SsoRoleMapping["role"] })}>
                    {ROLES.map((role) => (
                      <option key={role} value={role}>
                        {t(`settings.roles.${role}`)}
                      </option>
                    ))}
                  </NativeSelect>
                </TableCell>
                <TableCell>
                  <Button type="button" variant="ghost" size="sm" aria-label={`${t("sso.mappings.remove")} ${i + 1}`} onClick={() => setEdited(rows.filter((_, j) => j !== i))}>
                    <Trash2 aria-hidden="true" />
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      <FormError error={save.error} />
      {save.isSuccess && edited === null && (
        <p role="status" className="text-sm text-success">
          {t("sso.mappings.saved")}
        </p>
      )}
      <div className="flex flex-wrap gap-2">
        <Button type="button" variant="outline" onClick={() => setEdited([...rows, { group: "", role: "viewer" }])}>
          <Plus aria-hidden="true" />
          {t("sso.mappings.add")}
        </Button>
        <Button type="submit" disabled={save.isPending || edited === null}>
          {t("sso.mappings.save")}
        </Button>
      </div>
    </form>
  );
}

/** IdP group → role mapping table: organization-wide or of one connection. */
export function SsoRoleMappings({ state }: { state: SsoState }) {
  const { t } = useTranslation();
  const id = useId();
  const [scope, setScope] = useState("");
  const current = scope !== "" && state.connections.some((c) => c.id === scope) ? scope : "";

  return (
    <SettingsSection title={t("sso.mappings.title")} description={t("sso.mappings.description")}>
      <div className="flex max-w-sm flex-col gap-1.5">
        <Label htmlFor={`${id}-scope`}>{t("sso.mappings.scope")}</Label>
        <NativeSelect id={`${id}-scope`} value={current} aria-describedby={`${id}-scope-hint`} onChange={(e) => setScope(e.target.value)}>
          <option value="">{t("sso.mappings.orgWide")}</option>
          {state.connections.map((c) => (
            <option key={c.id} value={c.id}>
              {ssoConnectionLabel(c)}
            </option>
          ))}
        </NativeSelect>
        <p id={`${id}-scope-hint`} className="text-xs text-muted-foreground">
          {t("sso.mappings.fallbackHint")}
        </p>
      </div>
      <MappingsEditor key={current} connectionId={current || null} />
    </SettingsSection>
  );
}

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { replaceSsoRoleMappings, ssoRoleMappingsQuery, type SsoRoleMapping } from "@/api/sso";
import { ErrorState, LoadingState } from "@/components/StateViews";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { FormError, SettingsSection } from "./common";

const ROLES = ["admin", "member", "viewer"] as const;

/** IdP group → role mapping table. */
export function SsoRoleMappings() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const query = useQuery(ssoRoleMappingsQuery());
  const [edited, setEdited] = useState<SsoRoleMapping[] | null>(null);
  const rows = edited ?? query.data ?? [];
  const update = (i: number, patch: Partial<SsoRoleMapping>) => setEdited(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const save = useMutation({
    mutationFn: () => replaceSsoRoleMappings(rows.filter((r) => r.group.trim() !== "").map((r) => ({ ...r, group: r.group.trim() }))),
    onSuccess: (saved) => {
      qc.setQueryData(ssoRoleMappingsQuery().queryKey, saved);
      setEdited(null);
    },
  });

  return (
    <SettingsSection title={t("sso.mappings.title")} description={t("sso.mappings.description")}>
      {query.isPending ? (
        <LoadingState />
      ) : query.isError ? (
        <ErrorState error={query.error} onRetry={() => void query.refetch()} />
      ) : (
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
      )}
    </SettingsSection>
  );
}

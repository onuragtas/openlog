import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Loader2, Plus } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { createLicenseKey, licenseKeysQuery, revokeLicenseKey, useMe } from "@/api/account";
import { can } from "@/api/roles";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ConfirmButton } from "./ConfirmButton";
import { DateTimeText, FormError, SettingsSection } from "./common";
import { SecretReveal } from "./SecretReveal";

export function LicenseKeysSettings() {
  const { t } = useTranslation();
  const id = useId();
  const qc = useQueryClient();
  const me = useMe().data;
  const canManage = can(me?.role, "license_keys.manage");
  const keys = useQuery(licenseKeysQuery());
  const [name, setName] = useState("");
  // The plaintext key exists only in this state until "Done".
  const [created, setCreated] = useState<{ name: string; key: string } | null>(null);

  const invalidate = () => void qc.invalidateQueries({ queryKey: licenseKeysQuery().queryKey });
  const create = useMutation({
    mutationFn: (keyName: string) => createLicenseKey(keyName),
    onSuccess: (res) => {
      setCreated({ name: res.license_key.name, key: res.key });
      setName("");
      invalidate();
    },
  });
  const revoke = useMutation({ mutationFn: (keyId: string) => revokeLicenseKey(keyId), onSettled: invalidate });

  return (
    <div className="flex flex-col gap-4">
      <SettingsSection title={t("settings.licenseKeys.title")} description={t("settings.licenseKeys.description")}>
        {canManage && (
          <form
            className="flex flex-wrap items-end gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (name.trim() !== "") create.mutate(name.trim());
            }}
          >
            <div className="flex min-w-60 flex-1 flex-col gap-1.5">
              <Label htmlFor={`${id}-name`}>{t("settings.licenseKeys.name")}</Label>
              <Input id={`${id}-name`} value={name} maxLength={200} placeholder={t("settings.licenseKeys.namePlaceholder")} onChange={(e) => setName(e.target.value)} />
            </div>
            <Button type="submit" disabled={create.isPending || name.trim() === ""}>
              {create.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Plus aria-hidden="true" />}
              {t("settings.licenseKeys.create")}
            </Button>
          </form>
        )}
        <FormError error={create.error ?? revoke.error} />
        {created && (
          <SecretReveal
            label={t("settings.licenseKeys.created", { name: created.name })}
            secret={created.key}
            note={t("settings.licenseKeys.usage")}
            onDone={() => setCreated(null)}
          />
        )}
        <p className="text-xs text-muted-foreground">{t("settings.licenseKeys.revokeNote")}</p>
      </SettingsSection>

      <div className="rounded-xl border bg-card">
        {keys.isPending ? (
          <LoadingState />
        ) : keys.isError ? (
          <ErrorState error={keys.error} onRetry={() => void keys.refetch()} />
        ) : keys.data.length === 0 ? (
          <EmptyState icon={<KeyRound className="size-5" aria-hidden="true" />}>{t("settings.licenseKeys.empty")}</EmptyState>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("settings.columns.name")}</TableHead>
                <TableHead>{t("settings.columns.key")}</TableHead>
                <TableHead className="hidden lg:table-cell">{t("settings.columns.createdBy")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("settings.columns.created")}</TableHead>
                <TableHead>{t("settings.columns.lastUsed")}</TableHead>
                <TableHead>{t("settings.columns.status")}</TableHead>
                {canManage && (
                  <TableHead>
                    <span className="sr-only">{t("settings.columns.actions")}</span>
                  </TableHead>
                )}
              </TableRow>
            </TableHeader>
            <TableBody>
              {keys.data.map((k) => (
                <TableRow key={k.id}>
                  <TableCell className="font-medium">{k.name}</TableCell>
                  <TableCell>
                    <code className="font-mono text-xs">{k.prefix}</code>
                    <span aria-hidden="true">…</span>
                  </TableCell>
                  <TableCell className="hidden lg:table-cell">{k.created_by_email || "–"}</TableCell>
                  <TableCell className="hidden md:table-cell">
                    <DateTimeText value={k.created_at} />
                  </TableCell>
                  <TableCell>
                    <DateTimeText value={k.last_used_at} relative />
                  </TableCell>
                  <TableCell>
                    {k.revoked_at ? <Badge variant="muted">{t("settings.revoked")}</Badge> : <Badge variant="success">{t("settings.active")}</Badge>}
                  </TableCell>
                  {canManage && (
                    <TableCell className="text-right">
                      {!k.revoked_at && (
                        <ConfirmButton
                          label={t("settings.revoke")}
                          confirmLabel={t("settings.confirmRevoke")}
                          pending={revoke.isPending && revoke.variables === k.id}
                          onConfirm={() => revoke.mutate(k.id)}
                        />
                      )}
                    </TableCell>
                  )}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </div>
    </div>
  );
}

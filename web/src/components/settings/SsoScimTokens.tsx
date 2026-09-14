import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { createScimToken, revokeScimToken, scimTokensQuery } from "@/api/sso";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ConfirmButton } from "./ConfirmButton";
import { DateTimeText, FormError, SettingsSection } from "./common";
import { SecretReveal } from "./SecretReveal";
import { SsoCopyField } from "./SsoCopyField";

/** SCIM base URL and bearer tokens (shown once). */
export function SsoScimTokens({ enabled }: { enabled: boolean }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const id = useId();
  const tokens = useQuery({ ...scimTokensQuery(), enabled });
  const [name, setName] = useState("");
  const [secret, setSecret] = useState<string | null>(null);
  const create = useMutation({
    mutationFn: () => createScimToken(name.trim(), null),
    onSuccess: (res) => {
      setSecret(res.secret);
      setName("");
    },
    onSettled: () => void qc.invalidateQueries({ queryKey: scimTokensQuery().queryKey }),
  });
  const revoke = useMutation({
    mutationFn: (tokenId: string) => revokeScimToken(tokenId),
    onSettled: () => void qc.invalidateQueries({ queryKey: scimTokensQuery().queryKey }),
  });

  return (
    <SettingsSection title={t("sso.scim.title")} description={t("sso.scim.description")}>
      {!enabled ? (
        <p className="text-sm text-muted-foreground">{t("sso.scim.disabled")}</p>
      ) : tokens.isPending ? (
        <LoadingState />
      ) : tokens.isError ? (
        <ErrorState error={tokens.error} onRetry={() => void tokens.refetch()} />
      ) : (
        <>
          <SsoCopyField label={t("sso.scim.baseUrl")} value={tokens.data.base_url} />
          <form
            className="flex flex-wrap items-end gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (name.trim()) create.mutate();
            }}
          >
            <div className="flex min-w-0 flex-1 flex-col gap-1.5 sm:max-w-xs">
              <Label htmlFor={`${id}-name`}>{t("sso.scim.tokenName")}</Label>
              <Input id={`${id}-name`} value={name} maxLength={200} placeholder={t("sso.scim.tokenPlaceholder")} onChange={(e) => setName(e.target.value)} />
            </div>
            <Button type="submit" disabled={create.isPending || name.trim() === ""}>
              {t("sso.scim.create")}
            </Button>
          </form>
          <FormError error={create.error} />
          {secret && <SecretReveal label={t("sso.scim.tokenCreated")} secret={secret} onDone={() => setSecret(null)} />}
          <FormError error={revoke.error} />
          {tokens.data.tokens.length === 0 ? (
            <EmptyState>{t("sso.scim.empty")}</EmptyState>
          ) : (
            <Table mobile="stack">
              <TableHeader>
                <TableRow>
                  <TableHead>{t("settings.columns.name")}</TableHead>
                  <TableHead className="hidden md:table-cell">{t("settings.columns.createdBy")}</TableHead>
                  <TableHead>{t("settings.columns.lastUsed")}</TableHead>
                  <TableHead>
                    <span className="sr-only">{t("settings.columns.actions")}</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {tokens.data.tokens.map((tok) => (
                  <TableRow key={tok.id}>
                    <TableCell>
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="font-medium">{tok.name}</span>
                        <span className="font-mono text-xs text-muted-foreground">{tok.prefix}…</span>
                        {tok.revoked_at && <Badge variant="outline">{t("settings.revoked")}</Badge>}
                      </div>
                    </TableCell>
                    <TableCell label={t("settings.columns.createdBy")} className="hidden md:table-cell">
                      <span className="break-all">{tok.created_by_email}</span> · <DateTimeText value={tok.created_at} />
                    </TableCell>
                    <TableCell label={t("settings.columns.lastUsed")}>
                      <DateTimeText value={tok.last_used_at} relative />
                    </TableCell>
                    <TableCell className="text-right">
                      {!tok.revoked_at && (
                        <ConfirmButton
                          label={t("settings.revoke")}
                          confirmLabel={t("settings.confirmRevoke")}
                          pending={revoke.isPending && revoke.variables === tok.id}
                          onConfirm={() => revoke.mutate(tok.id)}
                        />
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </>
      )}
    </SettingsSection>
  );
}

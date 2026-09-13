import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Loader2, Plus } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { apiKeysQuery, createApiKey, revokeApiKey, useMe, type ApiKey } from "@/api/account";
import { can } from "@/api/roles";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useNow } from "@/lib/hooks";
import { ConfirmButton } from "./ConfirmButton";
import { DateTimeText, FormError, SettingsSection } from "./common";
import { SecretReveal } from "./SecretReveal";
import { parseApiTime } from "./time";

const EXPIRY = ["never", "d30", "d90", "d365"] as const;
type Expiry = (typeof EXPIRY)[number];
const EXPIRY_DAYS: Record<Expiry, number | null> = { never: null, d30: 30, d90: 90, d365: 365 };

export function ApiKeysSettings() {
  const { t } = useTranslation();
  const id = useId();
  const qc = useQueryClient();
  const now = useNow();
  const me = useMe().data;
  const canCreate = can(me?.role, "api_keys.create");
  const canRevokeAny = can(me?.role, "api_keys.revoke_any");
  const keys = useQuery(apiKeysQuery());
  const [name, setName] = useState("");
  const [expiry, setExpiry] = useState<Expiry>("d90");
  const [created, setCreated] = useState<{ name: string; key: string } | null>(null);

  const invalidate = () => void qc.invalidateQueries({ queryKey: apiKeysQuery().queryKey });
  const create = useMutation({
    mutationFn: (v: { name: string; expiry: Expiry }) => {
      const days = EXPIRY_DAYS[v.expiry];
      return createApiKey(v.name, days === null ? null : new Date(Date.now() + days * 86_400_000).toISOString());
    },
    onSuccess: (res) => {
      setCreated({ name: res.api_key.name, key: res.key });
      setName("");
      invalidate();
    },
  });
  const revoke = useMutation({ mutationFn: (keyId: string) => revokeApiKey(keyId), onSettled: invalidate });

  const status = (k: ApiKey) =>
    k.revoked_at ? (
      <Badge variant="muted">{t("settings.revoked")}</Badge>
    ) : k.expires_at && parseApiTime(k.expires_at) <= now ? (
      <Badge variant="warning">{t("settings.expired")}</Badge>
    ) : (
      <Badge variant="success">{t("settings.active")}</Badge>
    );
  const canRevoke = (k: ApiKey) => !k.revoked_at && (canRevokeAny || k.created_by_user_id === me?.user?.id);

  return (
    <div className="flex flex-col gap-4">
      <SettingsSection title={t("settings.apiKeys.title")} description={t("settings.apiKeys.description")}>
        {canCreate && (
          <form
            className="flex flex-wrap items-end gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (name.trim() !== "") create.mutate({ name: name.trim(), expiry });
            }}
          >
            <div className="flex min-w-60 flex-1 flex-col gap-1.5">
              <Label htmlFor={`${id}-name`}>{t("settings.apiKeys.name")}</Label>
              <Input id={`${id}-name`} value={name} maxLength={200} placeholder={t("settings.apiKeys.namePlaceholder")} onChange={(e) => setName(e.target.value)} />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-expiry`}>{t("settings.apiKeys.expiry")}</Label>
              <NativeSelect id={`${id}-expiry`} value={expiry} onChange={(e) => setExpiry(e.target.value as Expiry)}>
                {EXPIRY.map((x) => (
                  <option key={x} value={x}>
                    {t(`settings.apiKeys.expiryOptions.${x}`)}
                  </option>
                ))}
              </NativeSelect>
            </div>
            <Button type="submit" disabled={create.isPending || name.trim() === ""}>
              {create.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Plus aria-hidden="true" />}
              {t("settings.apiKeys.create")}
            </Button>
          </form>
        )}
        <FormError error={create.error ?? revoke.error} />
        {created && (
          <SecretReveal label={t("settings.apiKeys.created", { name: created.name })} secret={created.key} note={t("settings.apiKeys.usage")} onDone={() => setCreated(null)} />
        )}
      </SettingsSection>

      <div className="rounded-xl border bg-card">
        {keys.isPending ? (
          <LoadingState />
        ) : keys.isError ? (
          <ErrorState error={keys.error} onRetry={() => void keys.refetch()} />
        ) : keys.data.length === 0 ? (
          <EmptyState icon={<KeyRound className="size-5" aria-hidden="true" />}>{t("settings.apiKeys.empty")}</EmptyState>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("settings.columns.name")}</TableHead>
                <TableHead>{t("settings.columns.key")}</TableHead>
                <TableHead className="hidden lg:table-cell">{t("settings.columns.createdBy")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("settings.columns.lastUsed")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("settings.columns.expires")}</TableHead>
                <TableHead>{t("settings.columns.status")}</TableHead>
                <TableHead>
                  <span className="sr-only">{t("settings.columns.actions")}</span>
                </TableHead>
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
                    <DateTimeText value={k.last_used_at} relative />
                  </TableCell>
                  <TableCell className="hidden md:table-cell">
                    <DateTimeText value={k.expires_at} />
                  </TableCell>
                  <TableCell>{status(k)}</TableCell>
                  <TableCell className="text-right">
                    {canRevoke(k) && (
                      <ConfirmButton
                        label={t("settings.revoke")}
                        confirmLabel={t("settings.confirmRevoke")}
                        pending={revoke.isPending && revoke.variables === k.id}
                        onConfirm={() => revoke.mutate(k.id)}
                      />
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </div>
    </div>
  );
}

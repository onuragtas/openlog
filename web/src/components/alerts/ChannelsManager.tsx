import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { useMe } from "@/api/account";
import {
  alertChannelsQuery,
  createAlertChannel,
  deleteAlertChannel,
  testAlertChannel,
  updateAlertChannel,
  type AlertChannel,
  type AlertChannelInput,
  type AlertChannelTestResult,
  type AlertChannelType,
} from "@/api/alerts";
import { can } from "@/api/roles";
import { ConfirmAction } from "@/components/fleet/ConfirmAction";
import { DateTimeText, FormError } from "@/components/settings/common";
import { SecretReveal } from "@/components/settings/SecretReveal";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ChannelTypeLabel } from "./badges";
import { Field } from "./fields";

const TYPES: AlertChannelType[] = ["slack", "email", "webhook", "teams"];

function ChannelForm({ channel, onDone, onGenerated }: { channel: AlertChannel | null; onDone: () => void; onGenerated: (secret: string) => void }) {
  const { t } = useTranslation();
  const uid = useId();
  const queryClient = useQueryClient();
  const [name, setName] = useState(channel?.name ?? "");
  const [type, setType] = useState<AlertChannelType>(channel?.type ?? "slack");
  const [enabled, setEnabled] = useState(channel?.enabled ?? true);
  const [url, setUrl] = useState("");
  const [hmac, setHmac] = useState("");
  const [to, setTo] = useState((channel?.config.to ?? []).join(", "));
  const smtp0 = channel?.config.smtp;
  const [override, setOverride] = useState(!!smtp0);
  const [smtp, setSmtp] = useState({ host: smtp0?.host ?? "", port: String(smtp0?.port ?? 587), username: smtp0?.username ?? "", from: smtp0?.from ?? "", tls: smtp0?.tls ?? "starttls" });
  const [password, setPassword] = useState("");
  const id = (n: string) => `${uid}-${n}`;

  const save = useMutation({
    mutationFn: () => {
      const input: AlertChannelInput = { name: name.trim(), type, enabled, config: {}, secrets: {} };
      if (type === "email") {
        input.config = { to: to.split(",").map((s) => s.trim()).filter(Boolean) };
        if (override) input.config.smtp = { host: smtp.host.trim(), port: Number(smtp.port) || 587, username: smtp.username.trim(), from: smtp.from.trim(), tls: smtp.tls as "starttls" | "tls" | "none" };
        if (password) input.secrets = { smtp_password: password };
      } else {
        if (url.trim()) input.secrets = { url: url.trim() };
        if (type === "webhook" && hmac) input.secrets = { ...input.secrets, hmac_secret: hmac };
      }
      return channel ? updateAlertChannel(channel.id, input) : createAlertChannel(input);
    },
    onSuccess: (c) => {
      void queryClient.invalidateQueries({ queryKey: ["alerts", "channels"] });
      const secret = c.generated_secrets?.hmac_secret;
      if (secret) onGenerated(secret);
      onDone();
    },
  });

  return (
    <form
      className="flex flex-col gap-4 rounded-xl border bg-card p-4"
      data-testid="channel-form"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <div className="grid gap-4 sm:grid-cols-3">
        <Field id={id("name")} label={t("alerts.channels.name")}>
          <Input id={id("name")} required value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field id={id("type")} label={t("alerts.channels.type")}>
          <NativeSelect id={id("type")} value={type} disabled={!!channel} onChange={(e) => setType(e.target.value as AlertChannelType)}>
            {TYPES.map((ty) => (
              <option key={ty} value={ty}>
                {t(`alerts.channels.types.${ty}`)}
              </option>
            ))}
          </NativeSelect>
        </Field>
        <label className="flex items-center gap-2 self-end pb-2 text-sm">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          {t("alerts.channels.enabled")}
        </label>
      </div>
      {type === "email" ? (
        <div className="flex flex-col gap-4">
          <Field id={id("to")} label={t("alerts.channels.to")} hint={t("alerts.channels.toHint")}>
            <Input id={id("to")} value={to} placeholder="oncall@example.com" onChange={(e) => setTo(e.target.value)} aria-describedby={`${id("to")}-hint`} />
          </Field>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={override} onChange={(e) => setOverride(e.target.checked)} />
            {t("alerts.channels.smtpOverride")}
          </label>
          {override && (
            <div className="grid gap-4 sm:grid-cols-3">
              <Field id={id("host")} label={t("alerts.channels.smtpHost")}>
                <Input id={id("host")} value={smtp.host} onChange={(e) => setSmtp({ ...smtp, host: e.target.value })} />
              </Field>
              <Field id={id("port")} label={t("alerts.channels.smtpPort")}>
                <Input id={id("port")} inputMode="numeric" value={smtp.port} onChange={(e) => setSmtp({ ...smtp, port: e.target.value })} />
              </Field>
              <Field id={id("tls")} label={t("alerts.channels.smtpTls")}>
                <NativeSelect id={id("tls")} value={smtp.tls} onChange={(e) => setSmtp({ ...smtp, tls: e.target.value as "starttls" | "tls" | "none" })}>
                  <option value="starttls">STARTTLS</option>
                  <option value="tls">TLS</option>
                  <option value="none">none</option>
                </NativeSelect>
              </Field>
              <Field id={id("user")} label={t("alerts.channels.smtpUsername")}>
                <Input id={id("user")} autoComplete="off" value={smtp.username} onChange={(e) => setSmtp({ ...smtp, username: e.target.value })} />
              </Field>
              <Field id={id("pw")} label={t("alerts.channels.smtpPassword")} hint={channel?.secret_hints.smtp_password ? t("alerts.channels.urlKeep", { hint: channel.secret_hints.smtp_password }) : undefined}>
                <Input id={id("pw")} type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />
              </Field>
              <Field id={id("from")} label={t("alerts.channels.smtpFrom")}>
                <Input id={id("from")} value={smtp.from} placeholder="alerts@example.com" onChange={(e) => setSmtp({ ...smtp, from: e.target.value })} />
              </Field>
            </div>
          )}
        </div>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2">
          <Field id={id("url")} label={t("alerts.channels.url")} hint={channel?.secret_hints.url ? t("alerts.channels.urlKeep", { hint: channel.secret_hints.url }) : undefined}>
            <Input id={id("url")} type="url" autoComplete="off" value={url} placeholder="https://" onChange={(e) => setUrl(e.target.value)} />
          </Field>
          {type === "webhook" && (
            <Field id={id("hmac")} label={t("alerts.channels.hmacSecret")} hint={channel?.secret_hints.hmac_secret ? t("alerts.channels.urlKeep", { hint: channel.secret_hints.hmac_secret }) : t("alerts.channels.hmacHint")}>
              <Input id={id("hmac")} type="password" autoComplete="new-password" value={hmac} onChange={(e) => setHmac(e.target.value)} />
            </Field>
          )}
        </div>
      )}
      <div className="flex flex-wrap items-center gap-2">
        <Button type="submit" disabled={save.isPending}>
          {channel ? t("alerts.channels.save") : t("alerts.channels.create")}
        </Button>
        <Button type="button" variant="ghost" onClick={onDone}>
          {t("alerts.cancel")}
        </Button>
        <FormError error={save.error} />
      </div>
    </form>
  );
}

function TestResult({ result }: { result: AlertChannelTestResult | Error }) {
  const { t } = useTranslation();
  if (result instanceof Error) return <FormError error={result} />;
  return result.success ? (
    <p role="status" className="text-xs text-success-text">
      {t("alerts.channels.testOk", { status: result.status_code, ms: result.duration_ms })}
    </p>
  ) : (
    <p role="alert" className="text-xs text-destructive-text">
      {t("alerts.channels.testFailed", { error: result.error, status: result.status_code })}
    </p>
  );
}

function ChannelRow({ channel, canManage, onEdit }: { channel: AlertChannel; canManage: boolean; onEdit: () => void }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [result, setResult] = useState<AlertChannelTestResult | Error | null>(null);
  const test = useMutation({
    mutationFn: () => testAlertChannel(channel.id),
    onSuccess: (r) => {
      setResult(r);
      void queryClient.invalidateQueries({ queryKey: ["alerts", "channels"] });
    },
    onError: (e) => setResult(e),
  });
  const remove = useMutation({ mutationFn: () => deleteAlertChannel(channel.id), onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["alerts"] }) });
  const destination = channel.type === "email" ? (channel.config.to ?? []).join(", ") : (channel.secret_hints.url ?? "");
  return (
    <TableRow data-testid="channel-row">
      <TableCell>
        <span className="font-medium">{channel.name}</span>
        {!channel.enabled && (
          <Badge variant="muted" className="ml-2">
            {t("alerts.channels.disabled")}
          </Badge>
        )}
      </TableCell>
      <TableCell className="whitespace-nowrap">
        <ChannelTypeLabel type={channel.type} />
      </TableCell>
      <TableCell label={t("alerts.channels.columns.destination")} className="max-w-xs truncate font-mono text-xs max-md:break-all" title={destination}>
        {destination}
      </TableCell>
      <TableCell label={t("alerts.channels.columns.last")} className="whitespace-nowrap text-sm">
        {channel.last_delivery ? (
          <span className="inline-flex items-center gap-2">
            <Badge variant={channel.last_delivery.status === "delivered" ? "success" : "destructive"}>{t(`alerts.deliveries.statuses.${channel.last_delivery.status}`)}</Badge>
            <DateTimeText value={channel.last_delivery.at} relative />
          </span>
        ) : (
          <span className="text-muted-foreground">{t("alerts.channels.never")}</span>
        )}
      </TableCell>
      <TableCell>
        {canManage && (
          <div className="flex flex-col items-end gap-1 max-md:items-start">
            <div className="flex flex-wrap justify-end gap-2 max-md:justify-start">
              <Button type="button" variant="outline" size="sm" disabled={test.isPending} onClick={() => test.mutate()}>
                {t("alerts.channels.test")}
              </Button>
              <Button type="button" variant="outline" size="sm" onClick={onEdit}>
                {t("alerts.channels.edit")}
              </Button>
              <ConfirmAction label={t("alerts.channels.delete")} confirmLabel={t("alerts.channels.confirmDelete")} destructive pending={remove.isPending} onConfirm={() => remove.mutate()} />
            </div>
            {result && <TestResult result={result} />}
          </div>
        )}
      </TableCell>
    </TableRow>
  );
}

export function ChannelsManager() {
  const { t } = useTranslation();
  const canManage = can(useMe().data?.role, "alerts.manage");
  const q = useQuery(alertChannelsQuery());
  const [editing, setEditing] = useState<AlertChannel | "new" | null>(null);
  const [generated, setGenerated] = useState<string | null>(null);
  return (
    <div className="flex flex-col gap-3">
      {q.data && !q.data.secrets_configured && (
        <p role="alert" className="rounded-lg border border-warning/60 bg-warning/10 px-3 py-2 text-sm">
          {t("alerts.channels.secretsMissing")}
        </p>
      )}
      {!canManage && (
        <p role="note" className="text-sm text-muted-foreground">
          {t("alerts.channels.readOnly")}
        </p>
      )}
      {generated && <SecretReveal label={t("alerts.channels.generatedSecret")} secret={generated} onDone={() => setGenerated(null)} />}
      {canManage && editing === null && (
        <div>
          <Button type="button" onClick={() => setEditing("new")}>
            <Plus aria-hidden="true" />
            {t("alerts.channels.new")}
          </Button>
        </div>
      )}
      {editing !== null && <ChannelForm key={editing === "new" ? "new" : editing.id} channel={editing === "new" ? null : editing} onDone={() => setEditing(null)} onGenerated={setGenerated} />}
      {q.isPending ? (
        <LoadingState />
      ) : q.isError ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      ) : q.data.channels.length === 0 ? (
        <EmptyState>{t("alerts.channels.empty")}</EmptyState>
      ) : (
        <div className="rounded-xl border bg-card">
          <Table mobile="stack">
            <TableHeader>
              <TableRow>
                <TableHead>{t("alerts.channels.columns.name")}</TableHead>
                <TableHead>{t("alerts.channels.columns.type")}</TableHead>
                <TableHead>{t("alerts.channels.columns.destination")}</TableHead>
                <TableHead>{t("alerts.channels.columns.last")}</TableHead>
                <TableHead>
                  <span className="sr-only">{t("alerts.channels.test")}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {q.data.channels.map((c) => (
                <ChannelRow key={c.id} channel={c} canManage={canManage} onEdit={() => setEditing(c)} />
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  );
}

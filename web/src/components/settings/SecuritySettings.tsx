import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { MonitorSmartphone } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { authConfigQuery, changePassword, revokeSession, sessionsQuery, useMe } from "@/api/account";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ConfirmButton } from "./ConfirmButton";
import { DateTimeText, FormError, SettingsSection } from "./common";

function shortAgent(ua: string): string {
  return ua.length > 80 ? `${ua.slice(0, 80)}…` : ua;
}

/** Change password and manage the signed-in user's sessions. */
export function SecuritySettings() {
  const { t } = useTranslation();
  const id = useId();
  const qc = useQueryClient();
  const me = useMe().data;
  const isSession = me?.auth === "session";
  const minLength = useQuery(authConfigQuery()).data?.password_min_length ?? 8;
  const sessions = useQuery({ ...sessionsQuery(), enabled: isSession });
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [mismatch, setMismatch] = useState(false);

  const change = useMutation({
    mutationFn: () => changePassword(current, next),
    onSuccess: () => {
      setCurrent("");
      setNext("");
      setConfirm("");
      void qc.invalidateQueries({ queryKey: sessionsQuery().queryKey });
    },
  });
  const revoke = useMutation({
    mutationFn: (sessionId: string) => revokeSession(sessionId),
    onSettled: () => void qc.invalidateQueries({ queryKey: sessionsQuery().queryKey }),
  });

  if (me && !isSession) return <EmptyState>{t("settings.apiKeyAuth")}</EmptyState>;

  return (
    <div className="flex flex-col gap-4">
      <SettingsSection title={t("settings.security.passwordTitle")} description={t("settings.security.passwordDescription")}>
        <form
          className="grid max-w-md gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            change.reset();
            if (next !== confirm) {
              setMismatch(true);
              return;
            }
            setMismatch(false);
            change.mutate();
          }}
        >
          <input type="text" name="username" autoComplete="username" value={me?.user?.email ?? ""} readOnly hidden />
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-current`}>{t("settings.security.currentPassword")}</Label>
            <Input id={`${id}-current`} type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-next`}>{t("settings.security.newPassword")}</Label>
            <Input
              id={`${id}-next`}
              type="password"
              autoComplete="new-password"
              minLength={minLength}
              value={next}
              aria-describedby={`${id}-hint`}
              onChange={(e) => setNext(e.target.value)}
            />
            <p id={`${id}-hint`} className="text-xs text-muted-foreground">
              {t("settings.security.passwordHint", { min: minLength })}
            </p>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-confirm`}>{t("settings.security.confirmPassword")}</Label>
            <Input
              id={`${id}-confirm`}
              type="password"
              autoComplete="new-password"
              value={confirm}
              aria-invalid={mismatch}
              onChange={(e) => setConfirm(e.target.value)}
            />
          </div>
          {mismatch && (
            <p role="alert" className="text-sm text-destructive">
              {t("settings.security.mismatch")}
            </p>
          )}
          <FormError error={change.error} />
          {change.isSuccess && (
            <p role="status" className="text-sm text-success">
              {t("settings.security.passwordChanged")}
            </p>
          )}
          <div>
            <Button type="submit" disabled={change.isPending || current === "" || next === ""}>
              {t("settings.security.changePassword")}
            </Button>
          </div>
        </form>
      </SettingsSection>

      <SettingsSection title={t("settings.security.sessionsTitle")} description={t("settings.security.sessionsDescription")}>
        <FormError error={revoke.error} />
        {sessions.isPending ? (
          <LoadingState />
        ) : sessions.isError ? (
          <ErrorState error={sessions.error} onRetry={() => void sessions.refetch()} />
        ) : sessions.data.length === 0 ? (
          <EmptyState>{t("settings.security.sessionsEmpty")}</EmptyState>
        ) : (
          <Table mobile="stack">
            <TableHeader>
              <TableRow>
                <TableHead>{t("settings.columns.device")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("settings.columns.ip")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("settings.columns.signedIn")}</TableHead>
                <TableHead>{t("settings.columns.lastActive")}</TableHead>
                <TableHead>
                  <span className="sr-only">{t("settings.columns.actions")}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {sessions.data.map((s) => (
                <TableRow key={s.id}>
                  <TableCell>
                    <div className="flex items-center gap-2">
                      <MonitorSmartphone className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                      <span className="break-all text-xs" title={s.user_agent}>
                        {s.user_agent ? shortAgent(s.user_agent) : t("settings.security.unknownDevice")}
                      </span>
                      {s.current && <Badge variant="success">{t("settings.security.current")}</Badge>}
                    </div>
                  </TableCell>
                  <TableCell label={t("settings.columns.ip")} className="hidden font-mono text-xs md:table-cell">
                    {s.ip || "–"}
                  </TableCell>
                  <TableCell label={t("settings.columns.signedIn")} className="hidden md:table-cell">
                    <DateTimeText value={s.created_at} />
                  </TableCell>
                  <TableCell label={t("settings.columns.lastActive")}>
                    <DateTimeText value={s.last_seen_at} relative />
                  </TableCell>
                  <TableCell className="text-right">
                    {!s.current && (
                      <ConfirmButton
                        label={t("settings.revoke")}
                        confirmLabel={t("settings.confirmRevoke")}
                        pending={revoke.isPending && revoke.variables === s.id}
                        onConfirm={() => revoke.mutate(s.id)}
                      />
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </SettingsSection>
    </div>
  );
}

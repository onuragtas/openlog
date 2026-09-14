import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Loader2, UserPlus } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  authConfigQuery,
  createInvitation,
  invitationLink,
  invitationsQuery,
  meQuery,
  membersQuery,
  removeMember,
  resendInvitation,
  revokeInvitation,
  updateMemberRole,
  useMe,
} from "@/api/account";
import { setSelectedOrg } from "@/api/auth";
import { assignableRoles, can, ROLES, type Role } from "@/api/roles";
import { EmptyState, ErrorState, LoadingState } from "@/components/StateViews";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ConfirmButton } from "./ConfirmButton";
import { DateTimeText, FormError, SettingsSection } from "./common";
import { SecretReveal } from "./SecretReveal";

export function MembersSettings() {
  const { t } = useTranslation();
  const id = useId();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const me = useMe().data;
  const role = me?.role ?? null;
  const myId = me?.user?.id;
  const canManage = can(role, "members.manage");
  const canInvite = can(role, "invitations.manage");
  const members = useQuery(membersQuery());
  const invitations = useQuery({ ...invitationsQuery(), enabled: canInvite });
  const emailEnabled = useQuery(authConfigQuery()).data?.email_enabled === true;

  const [email, setEmail] = useState("");
  const [inviteRole, setInviteRole] = useState<Role>("member");
  const [invited, setInvited] = useState<{ email: string; link: string; emailSent: boolean; resent: boolean } | null>(null);

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: membersQuery().queryKey });
    void qc.invalidateQueries({ queryKey: meQuery().queryKey });
  };
  const update = useMutation({
    mutationFn: (v: { userId: string; role: Role }) => updateMemberRole(v.userId, v.role),
    onSettled: refresh,
  });
  const remove = useMutation({
    mutationFn: (userId: string) => removeMember(userId),
    onSuccess: (_data, userId) => {
      if (userId === myId) {
        // Left the organization: continue in the default one.
        setSelectedOrg(null);
        qc.clear();
        void navigate({ to: "/hosts" });
      }
    },
    onSettled: refresh,
  });
  const invite = useMutation({
    mutationFn: (v: { email: string; role: Role }) => createInvitation(v.email, v.role),
    onSuccess: (res) => {
      setInvited({ email: res.invitation.email, link: invitationLink(res.token), emailSent: res.email_sent, resent: false });
      setEmail("");
      void qc.invalidateQueries({ queryKey: invitationsQuery().queryKey });
    },
  });
  const resend = useMutation({
    mutationFn: (invitationId: string) => resendInvitation(invitationId),
    onSuccess: (res) => setInvited({ email: res.invitation.email, link: invitationLink(res.token), emailSent: res.email_sent, resent: true }),
    onSettled: () => void qc.invalidateQueries({ queryKey: invitationsQuery().queryKey }),
  });
  const revealLabel = (inv: NonNullable<typeof invited>) =>
    inv.emailSent
      ? t("settings.members.inviteEmailed", { email: inv.email })
      : emailEnabled
        ? t("settings.members.inviteEmailFailed", { email: inv.email })
        : inv.resent
          ? t("settings.members.resendLink", { email: inv.email })
          : t("settings.members.inviteLink", { email: inv.email });
  const revoke = useMutation({
    mutationFn: (invitationId: string) => revokeInvitation(invitationId),
    onSettled: () => void qc.invalidateQueries({ queryKey: invitationsQuery().queryKey }),
  });

  return (
    <div className="flex flex-col gap-4">
      <SettingsSection title={t("settings.members.title")} description={t("settings.members.description")}>
        <FormError error={update.error ?? remove.error} />
        {members.isPending ? (
          <LoadingState />
        ) : members.isError ? (
          <ErrorState error={members.error} onRetry={() => void members.refetch()} />
        ) : members.data.length === 0 ? (
          <EmptyState>{t("settings.members.empty")}</EmptyState>
        ) : (
          <Table mobile="stack">
            <TableHeader>
              <TableRow>
                <TableHead>{t("settings.columns.member")}</TableHead>
                <TableHead>{t("settings.columns.role")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("settings.columns.joined")}</TableHead>
                <TableHead>
                  <span className="sr-only">{t("settings.columns.actions")}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {members.data.map((m) => {
                const options = assignableRoles(role, m.role);
                const self = m.user_id === myId;
                return (
                  <TableRow key={m.user_id}>
                    <TableCell>
                      <div className="font-medium">
                        {m.name || m.email}
                        {self && <span className="ml-1 text-xs font-normal text-muted-foreground">({t("settings.you")})</span>}
                      </div>
                      {m.name && <div className="text-xs text-muted-foreground">{m.email}</div>}
                    </TableCell>
                    <TableCell>
                      {options.length > 0 ? (
                        <NativeSelect
                          aria-label={t("settings.members.roleFor", { email: m.email })}
                          value={m.role}
                          disabled={update.isPending}
                          onChange={(e) => update.mutate({ userId: m.user_id, role: e.target.value as Role })}
                        >
                          {options.map((r) => (
                            <option key={r} value={r}>
                              {t(`settings.roles.${r}`)}
                            </option>
                          ))}
                        </NativeSelect>
                      ) : (
                        <Badge variant="secondary">{t(`settings.roles.${m.role}`)}</Badge>
                      )}
                    </TableCell>
                    <TableCell label={t("settings.columns.joined")} className="hidden md:table-cell">
                      <DateTimeText value={m.joined_at} />
                    </TableCell>
                    <TableCell className="text-right">
                      {self ? (
                        <ConfirmButton label={t("settings.leave")} confirmLabel={t("settings.confirmLeave")} pending={remove.isPending} onConfirm={() => remove.mutate(m.user_id)} />
                      ) : (
                        canManage &&
                        (m.role !== "owner" || role === "owner") && (
                          <ConfirmButton label={t("settings.remove")} confirmLabel={t("settings.confirmRemove")} pending={remove.isPending} onConfirm={() => remove.mutate(m.user_id)} />
                        )
                      )}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </SettingsSection>

      {canInvite && (
        <SettingsSection
          title={t("settings.members.inviteTitle")}
          description={emailEnabled ? t("settings.members.inviteDescriptionEmail") : t("settings.members.inviteDescription")}
        >
          <form
            className="flex flex-wrap items-end gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (email.trim() !== "") invite.mutate({ email: email.trim(), role: inviteRole });
            }}
          >
            <div className="flex min-w-60 flex-1 flex-col gap-1.5">
              <Label htmlFor={`${id}-email`}>{t("settings.members.email")}</Label>
              <Input id={`${id}-email`} type="email" value={email} placeholder={t("settings.members.emailPlaceholder")} onChange={(e) => setEmail(e.target.value)} />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`${id}-role`}>{t("settings.members.role")}</Label>
              <NativeSelect id={`${id}-role`} value={inviteRole} onChange={(e) => setInviteRole(e.target.value as Role)}>
                {ROLES.filter((r) => r !== "owner" || role === "owner").map((r) => (
                  <option key={r} value={r}>
                    {t(`settings.roles.${r}`)}
                  </option>
                ))}
              </NativeSelect>
            </div>
            <Button type="submit" disabled={invite.isPending || email.trim() === ""}>
              {invite.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <UserPlus aria-hidden="true" />}
              {t("settings.members.invite")}
            </Button>
          </form>
          <FormError error={invite.error ?? revoke.error ?? resend.error} />
          {invited && (
            <SecretReveal
              label={revealLabel(invited)}
              secret={invited.link}
              note={t("settings.members.inviteLinkNote")}
              onDone={() => setInvited(null)}
            />
          )}
          <h3 className="mt-2 text-sm font-semibold">{t("settings.members.pendingTitle")}</h3>
          {invitations.isPending ? (
            <LoadingState />
          ) : invitations.isError ? (
            <ErrorState error={invitations.error} onRetry={() => void invitations.refetch()} />
          ) : invitations.data.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("settings.members.pendingEmpty")}</p>
          ) : (
            <Table mobile="stack">
              <TableHeader>
                <TableRow>
                  <TableHead>{t("settings.columns.email")}</TableHead>
                  <TableHead>{t("settings.columns.role")}</TableHead>
                  <TableHead className="hidden md:table-cell">{t("settings.columns.invitedBy")}</TableHead>
                  <TableHead>{t("settings.columns.expires")}</TableHead>
                  {emailEnabled && <TableHead className="hidden md:table-cell">{t("settings.members.lastSent")}</TableHead>}
                  <TableHead>
                    <span className="sr-only">{t("settings.columns.actions")}</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {invitations.data.map((inv) => (
                  <TableRow key={inv.id}>
                    <TableCell className="max-md:w-auto max-md:break-all">{inv.email}</TableCell>
                    <TableCell className="max-md:w-auto">
                      <Badge variant="secondary">{t(`settings.roles.${inv.role}`)}</Badge>
                    </TableCell>
                    <TableCell label={t("settings.columns.invitedBy")} className="hidden break-all md:table-cell">
                      {inv.invited_by_email}
                    </TableCell>
                    <TableCell label={t("settings.columns.expires")}>
                      {inv.expired ? <Badge variant="warning">{t("settings.expired")}</Badge> : <DateTimeText value={inv.expires_at} relative />}
                    </TableCell>
                    {emailEnabled && (
                      <TableCell label={t("settings.members.lastSent")} className="hidden md:table-cell">
                        {inv.last_sent_at ? <DateTimeText value={inv.last_sent_at} relative /> : <span className="text-muted-foreground">{t("settings.members.notSent")}</span>}
                      </TableCell>
                    )}
                    <TableCell className="text-right">
                      <span className="inline-flex flex-wrap justify-end gap-1">
                        <Button type="button" variant="outline" size="sm" disabled={resend.isPending} onClick={() => resend.mutate(inv.id)}>
                          {t("settings.members.resend")}
                        </Button>
                        <ConfirmButton label={t("settings.revoke")} confirmLabel={t("settings.confirmRevoke")} pending={revoke.isPending} onConfirm={() => revoke.mutate(inv.id)} />
                      </span>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </SettingsSection>
      )}
    </div>
  );
}

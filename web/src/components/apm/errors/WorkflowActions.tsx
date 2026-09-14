// Workflow actions of error groups (PATCH /apm/errors/groups): resolve, resolve in version, ignore, reopen,
// assign. Used by the inbox bulk bar (several groups) and the group detail (one group).
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { membersQuery, useMe, type Member } from "@/api/account";
import { isErrorWorkflowKey, patchApmErrorGroups, type ApmErrorGroupPatch, type ApmErrorStatus } from "@/api/apm";
import { ApiError } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { canWriteWorkflow, versionProblem } from "@/lib/apm-errors";

const NONE = "__none__";

/** Signed-in user, write permission and organization members (undefined when they cannot be listed). */
export function useErrorWorkflow() {
  const me = useMe();
  const session = me.data?.auth === "session";
  const members = useQuery({ ...membersQuery(), enabled: session, retry: false });
  return {
    me: me.data,
    userId: me.data?.user?.id ?? null,
    canWrite: canWriteWorkflow(me.data ? { auth: me.data.auth, role: me.data.role } : undefined),
    members: members.isSuccess ? members.data : undefined,
  };
}

export function useInvalidateErrorWorkflow() {
  const qc = useQueryClient();
  return () => qc.invalidateQueries({ predicate: (q) => isErrorWorkflowKey(q.queryKey) });
}

export function errorMessage(e: unknown): string {
  return e instanceof ApiError ? e.message : String(e);
}

export interface WorkflowActionsProps {
  groupIds: string[];
  /** prefill of "Resolve in version…" (the service's current version) */
  defaultVersion?: string;
  members?: Member[];
  /** single group: its status and assignee (the assignee select shows the current value) */
  status?: ApmErrorStatus;
  assigneeUserId?: string | null;
  onDone?: () => void;
  label: string;
}

export function WorkflowActions({ groupIds, defaultVersion, members, status, assigneeUserId, onDone, label }: WorkflowActionsProps) {
  const { t } = useTranslation();
  const id = useId();
  const invalidate = useInvalidateErrorWorkflow();
  const single = status !== undefined;
  const [versionOpen, setVersionOpen] = useState(false);
  const [version, setVersion] = useState(defaultVersion ?? "");
  const [versionError, setVersionError] = useState<string | null>(null);
  const patch = useMutation({
    mutationFn: (body: Omit<ApmErrorGroupPatch, "group_ids">) => patchApmErrorGroups({ group_ids: groupIds, ...body }),
    onSuccess: () => {
      setVersionOpen(false);
      void invalidate();
      onDone?.();
    },
  });
  const disabled = patch.isPending || groupIds.length === 0;

  const submitVersion = (e: FormEvent) => {
    e.preventDefault();
    const problem = versionProblem(version);
    if (problem) {
      setVersionError(problem === "required" ? t("apm.errors.versionRequired") : t("apm.errors.versionTooLong"));
      return;
    }
    setVersionError(null);
    patch.mutate({ status: "resolved", resolved_in_version: version.trim() });
  };

  const assigneeValue = single ? (assigneeUserId ? assigneeUserId : NONE) : "";
  const knownAssignee = !assigneeUserId || (members ?? []).some((m) => m.user_id === assigneeUserId);

  return (
    <div className="flex flex-col gap-2">
      <div role="group" aria-label={label} className="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="outline" disabled={disabled || status === "resolved"} onClick={() => patch.mutate({ status: "resolved" })}>
          {t("apm.errors.resolve")}
        </Button>
        <Button
          size="sm"
          variant={versionOpen ? "secondary" : "outline"}
          aria-expanded={versionOpen}
          disabled={disabled}
          onClick={() => {
            setVersion((v) => v || defaultVersion || "");
            setVersionError(null);
            setVersionOpen((o) => !o);
          }}
        >
          {t("apm.errors.resolveInVersion")}
        </Button>
        <Button size="sm" variant="outline" disabled={disabled || status === "ignored"} onClick={() => patch.mutate({ status: "ignored" })}>
          {t("apm.errors.ignore")}
        </Button>
        <Button size="sm" variant="outline" disabled={disabled || status === "unresolved"} onClick={() => patch.mutate({ status: "unresolved" })}>
          {t("apm.errors.reopen")}
        </Button>
        {members && (
          <>
            <label htmlFor={`${id}-assign`} className="sr-only">
              {single ? t("apm.errors.assignee") : t("apm.errors.assignTo")}
            </label>
            <NativeSelect
              id={`${id}-assign`}
              value={assigneeValue}
              disabled={disabled}
              className="max-w-full"
              onChange={(e) => {
                const v = e.target.value;
                if (v === "") return;
                patch.mutate({ assignee_user_id: v === NONE ? "" : v });
              }}
            >
              {!single && <option value="">{t("apm.errors.assignPlaceholder")}</option>}
              <option value={NONE}>{single ? t("apm.errors.unassigned") : t("apm.errors.unassign")}</option>
              {!knownAssignee && assigneeUserId && <option value={assigneeUserId}>{assigneeUserId}</option>}
              {members.map((m) => (
                <option key={m.user_id} value={m.user_id}>
                  {m.name ? `${m.name} (${m.email})` : m.email}
                </option>
              ))}
            </NativeSelect>
          </>
        )}
      </div>
      {versionOpen && (
        <form onSubmit={submitVersion} noValidate className="flex flex-wrap items-end gap-2" aria-label={t("apm.errors.resolveInVersionConfirm")}>
          <div className="flex min-w-0 flex-col gap-1">
            <label htmlFor={`${id}-version`} className="text-xs font-medium">
              {t("apm.errors.version")}
            </label>
            <Input
              id={`${id}-version`}
              value={version}
              onChange={(e) => setVersion(e.target.value)}
              className="h-8 w-44 max-w-full"
              aria-invalid={!!versionError}
              aria-describedby={`${id}-version-hint`}
              autoFocus
            />
          </div>
          <Button type="submit" size="sm" disabled={patch.isPending}>
            {t("apm.errors.resolveInVersionConfirm")}
          </Button>
          <Button type="button" size="sm" variant="ghost" onClick={() => setVersionOpen(false)}>
            {t("apm.errors.cancel")}
          </Button>
          <p id={`${id}-version-hint`} className="w-full text-xs text-muted-foreground">
            {t("apm.errors.versionHint")}
          </p>
          {versionError && (
            <p role="alert" className="w-full text-xs text-destructive-text">
              {versionError}
            </p>
          )}
        </form>
      )}
      {patch.error && (
        <p role="alert" className="text-xs text-destructive-text">
          {errorMessage(patch.error)}
        </p>
      )}
    </div>
  );
}

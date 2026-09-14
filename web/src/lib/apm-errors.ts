// Pure helpers of the APM error inbox (docs/contracts/apm.md §3.4): filters, permissions, validation and
// readable activity entries. Rendering-independent and unit-tested.
import type { ApmErrorActivity, ApmErrorComment, ApmErrorStatus } from "@/api/apm";
import { atLeast, type Role } from "@/api/roles";

export const ERROR_STATUS_TABS = ["unresolved", "resolved", "ignored", "all"] as const;
export type ErrorStatusTab = (typeof ERROR_STATUS_TABS)[number];
export const ERROR_SORTS = ["count", "last_seen", "first_seen"] as const;

export const COMMENT_MAX_BYTES = 4000;
export const VERSION_MAX = 256;

/** A resolved group that reopened automatically (and has not been resolved again). */
export function isRegressed(g: { status: ApmErrorStatus; regressed_at: string | null }): boolean {
  return g.status === "unresolved" && !!g.regressed_at;
}

/** Signed-in members, admins and owners change workflow state and comment (viewers and API keys cannot). */
export function canWriteWorkflow(me: { auth: string; role: Role | null | undefined } | undefined): boolean {
  return !!me && me.auth === "session" && atLeast(me.role, "member");
}

/** Authors delete their own comments; admins and owners delete any. */
export function canDeleteComment(me: { auth: string; role: Role | null | undefined; userId?: string | null } | undefined, c: ApmErrorComment): boolean {
  if (!canWriteWorkflow(me)) return false;
  return atLeast(me!.role, "admin") || (!!me!.userId && me!.userId === c.author_user_id);
}

export type TextProblem = "required" | "tooLong";

export function versionProblem(v: string): TextProblem | null {
  const s = v.trim();
  if (!s) return "required";
  return s.length > VERSION_MAX ? "tooLong" : null;
}

export function commentProblem(body: string): TextProblem | null {
  if (!body.trim()) return "required";
  return new TextEncoder().encode(body).length > COMMENT_MAX_BYTES ? "tooLong" : null;
}

/** Ids in `visible` all selected? */
export function allSelected(selected: ReadonlySet<string>, visible: readonly string[]): boolean {
  return visible.length > 0 && visible.every((id) => selected.has(id));
}

/** Select-all checkbox: selects every visible id, or clears them when all were selected. */
export function toggleAll(selected: ReadonlySet<string>, visible: readonly string[]): Set<string> {
  const next = new Set(selected);
  if (allSelected(selected, visible)) visible.forEach((id) => next.delete(id));
  else visible.forEach((id) => next.add(id));
  return next;
}

/** Keeps only selected ids that are still listed (after a filter or status change). */
export function pruneSelection(selected: ReadonlySet<string>, visible: readonly string[]): Set<string> {
  const ids = new Set(visible);
  const next = new Set([...selected].filter((id) => ids.has(id)));
  return next.size === selected.size ? (selected as Set<string>) : next;
}

/** An i18n key under apm.errors.activity plus its interpolation values. */
export interface ActivityText {
  key: string;
  params: Record<string, string | number>;
}

const fromTo = (v: unknown): { from: string; to: string } | null => {
  if (!v || typeof v !== "object") return null;
  const o = v as Record<string, unknown>;
  return { from: typeof o.from === "string" ? o.from : "", to: typeof o.to === "string" ? o.to : "" };
};

/**
 * Readable sentences of one activity entry (audit events apm.error_group.update / regressed / comment /
 * comment_delete, details as written by internal/apm/errorstore_pg.go). `memberName` resolves user ids.
 */
export function activityTexts(a: ApmErrorActivity, memberName: (userId: string) => string): ActivityText[] {
  const actor = a.actor_email || "–";
  const d = a.details ?? {};
  switch (a.action) {
    case "apm.error_group.update": {
      const out: ActivityText[] = [];
      const status = fromTo(d.status);
      const version = typeof d.resolved_in_version === "string" ? d.resolved_in_version : "";
      if (status && ["unresolved", "resolved", "ignored"].includes(status.to)) {
        if (status.to === "resolved" && version) out.push({ key: "resolvedInVersion", params: { actor, version } });
        else out.push({ key: `status.${status.to}`, params: { actor } });
      } else if (version) {
        out.push({ key: "resolvedInVersion", params: { actor, version } });
      }
      const assignee = fromTo(d.assignee_user_id);
      if (assignee) {
        out.push(assignee.to ? { key: "assigned", params: { actor, name: memberName(assignee.to) } } : { key: "unassigned", params: { actor } });
      }
      return out.length ? out : [{ key: "updated", params: { actor } }];
    }
    case "apm.error_group.regressed": {
      const version = typeof d.version === "string" ? d.version : "";
      return [version ? { key: "regressedVersion", params: { version } } : { key: "regressed", params: {} }];
    }
    case "apm.error_group.comment":
      return [{ key: "comment", params: { actor } }];
    case "apm.error_group.comment_delete":
      return [{ key: "commentDelete", params: { actor } }];
    default:
      return [{ key: "other", params: { actor, action: a.action } }];
  }
}

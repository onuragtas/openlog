// Data subject requests: exports, account and organization deletion (docs/contracts/api.md "Data export and deletion").
import { queryOptions } from "@tanstack/react-query";
import { api, expectOk, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];
export type DataExport = S["DataExport"];
export type OrgDeletion = S["OrgDeletion"];
export type AccountPrivacy = S["AccountPrivacy"];
export type ExportSignal = "logs" | "traces" | "metrics";
export const EXPORT_SIGNALS: readonly ExportSignal[] = ["logs", "traces", "metrics"];

/** Polls while an export is queued or running. */
function pollWhileActive(list: DataExport[] | undefined): number | false {
  return list?.some((e) => e.status === "pending" || e.status === "running") ? 5_000 : false;
}

export const accountPrivacyQuery = () =>
  queryOptions({
    queryKey: ["privacy", "account"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/account/privacy", { signal })),
  });

export const personalExportsQuery = () =>
  queryOptions({
    queryKey: ["privacy", "exports", "personal"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/account/data-exports", { signal })).exports,
    refetchInterval: (q) => pollWhileActive(q.state.data),
  });

export const orgExportsQuery = () =>
  queryOptions({
    queryKey: ["privacy", "exports", "org"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/data-exports", { signal })).exports,
    refetchInterval: (q) => pollWhileActive(q.state.data),
  });

export async function requestPersonalExport(): Promise<DataExport> {
  return unwrap(await api.POST("/api/v1/account/data-exports", {})).export;
}

export async function requestOrgExport(body: { from?: string; to?: string; signals: ExportSignal[] }): Promise<DataExport> {
  return unwrap(await api.POST("/api/v1/data-exports", { body })).export;
}

/** Downloads an archive through the API (the organization header is sent, unlike a plain link) and saves it. */
export async function downloadExport(id: string): Promise<void> {
  const res = await api.GET("/api/v1/data-exports/{id}/download", { params: { path: { id } }, parseAs: "blob" });
  const blob = unwrap(res) as unknown as Blob;
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `openlog-export-${id}.zip`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 10_000);
}

export async function scheduleOrgDeletion(confirmName: string, password: string | undefined): Promise<OrgDeletion> {
  return unwrap(await api.POST("/api/v1/orgs/current/deletion", { body: { confirm_name: confirmName, password } })).deletion;
}

export async function cancelOrgDeletion(id: string): Promise<OrgDeletion> {
  return unwrap(await api.POST("/api/v1/org-deletions/{id}/cancel", { params: { path: { id } } })).deletion;
}

export async function deleteAccount(confirmEmail: string, password: string | undefined): Promise<void> {
  expectOk(await api.POST("/api/v1/account/delete", { body: { confirm_email: confirmEmail, password } }));
}

// ---- operators (superadmins): organization deletions and certificates ----

export type DeletionCertificate = S["DeletionCertificate"];

export const adminOrgDeletionsQuery = () =>
  queryOptions({
    queryKey: ["operator", "org-deletions"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/admin/org-deletions", { params: { query: { limit: 500 } }, signal })).deletions,
  });

/** Hex sha256 of a tenant id or user id: the subject of a deletion certificate. */
export async function subjectHash(id: string): Promise<string> {
  const sum = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(id));
  return Array.from(new Uint8Array(sum), (b) => b.toString(16).padStart(2, "0")).join("");
}

/** Deletion certificates of one subject (tenant id or user id), newest first. */
export const deletionCertificatesQuery = (subjectId: string) =>
  queryOptions({
    queryKey: ["operator", "deletion-certificates", subjectId],
    queryFn: async ({ signal }) => {
      const hash = await subjectHash(subjectId);
      return unwrap(await api.GET("/api/v1/admin/deletion-certificates", { params: { query: { subject_hash: hash, limit: 100 } }, signal })).certificates;
    },
  });

export async function adminScheduleOrgDeletion(org: string, reason: string, immediate: boolean): Promise<OrgDeletion> {
  return unwrap(await api.POST("/api/v1/admin/orgs/{org}/deletion", { params: { path: { org } }, body: { reason, immediate } })).deletion;
}

export async function adminCancelOrgDeletion(id: string): Promise<OrgDeletion> {
  return unwrap(await api.POST("/api/v1/admin/org-deletions/{id}/cancel", { params: { path: { id } } })).deletion;
}

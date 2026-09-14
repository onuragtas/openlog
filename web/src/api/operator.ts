// SaaS operator console, organization lifecycle and support access (docs/contracts/api.md "SaaS operations").
import { queryOptions } from "@tanstack/react-query";
import { api, expectOk, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];
export type OperatorOrg = S["OperatorOrg"];
export type OperatorOrgList = S["OperatorOrgList"];
export type OperatorOrgDetail = S["OperatorOrgDetail"];
export type OrgLifecycle = S["OrgLifecycle"];
export type SupportSession = S["SupportSession"];
export type AbuseFlag = S["AbuseFlag"];
export type OrgSaaSState = S["OrgSaaSState"];
export type OrgState = OperatorOrg["state"];

export interface OperatorOrgFilter {
  q?: string;
  plan?: string;
  state?: "active" | "suspended" | "trial" | "flagged";
  sort?: "created" | "name" | "ingest" | "members" | "last_ingest";
  offset?: number;
}

export const OPERATOR_PAGE_SIZE = 50;

export const operatorMeQuery = () =>
  queryOptions({
    queryKey: ["operator", "me"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/operator/me", { signal })),
    staleTime: 5 * 60_000,
    retry: false,
  });

export const operatorOrgsQuery = (f: OperatorOrgFilter) =>
  queryOptions({
    queryKey: ["operator", "orgs", f],
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/operator/orgs", {
          params: { query: { q: f.q || undefined, plan: f.plan || undefined, state: f.state, sort: f.sort, limit: OPERATOR_PAGE_SIZE, offset: f.offset || undefined } },
          signal,
        }),
      ),
  });

export const operatorOrgQuery = (org: string) =>
  queryOptions({
    queryKey: ["operator", "org", org],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/operator/orgs/{org}", { params: { path: { org } }, signal })),
  });

export const operatorFlagsQuery = (status: "open" | "dismissed" | "actioned" | "all" = "open") =>
  queryOptions({
    queryKey: ["operator", "flags", status],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/operator/flags", { params: { query: { status } }, signal })).flags,
  });

/** Suspension, trial and support access of the current organization (banners, Settings). */
export const orgSaaSStateQuery = () =>
  queryOptions({
    queryKey: ["saas", "state"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/orgs/current/saas", { signal })),
    staleTime: 60_000,
    refetchInterval: 5 * 60_000,
    retry: false,
  });

export type OperatorAction = "suspend" | "unsuspend" | "reset-quota-notifications" | "force-logout" | "resend-verification";

export async function runOperatorAction(org: string, action: OperatorAction, reason: string): Promise<unknown> {
  const init = { params: { path: { org } }, body: { reason } };
  switch (action) {
    case "suspend":
      return unwrap(await api.POST("/api/v1/operator/orgs/{org}/suspend", init));
    case "unsuspend":
      return unwrap(await api.POST("/api/v1/operator/orgs/{org}/unsuspend", init));
    case "reset-quota-notifications":
      return unwrap(await api.POST("/api/v1/operator/orgs/{org}/reset-quota-notifications", init));
    case "force-logout":
      return unwrap(await api.POST("/api/v1/operator/orgs/{org}/force-logout", init));
    case "resend-verification":
      return unwrap(await api.POST("/api/v1/operator/orgs/{org}/resend-verification", init));
  }
}

export async function startOrExtendTrial(org: string, body: S["TrialInput"]): Promise<OrgLifecycle> {
  return unwrap(await api.POST("/api/v1/operator/orgs/{org}/trial", { params: { path: { org } }, body }));
}

export async function startSupportSession(org: string, reason: string): Promise<SupportSession> {
  return unwrap(await api.POST("/api/v1/operator/orgs/{org}/support-sessions", { params: { path: { org } }, body: { reason } }));
}

export async function endSupportSession(id: string): Promise<void> {
  expectOk(await api.DELETE("/api/v1/operator/support-sessions/{id}", { params: { path: { id } } }));
}

export async function recordSupportView(id: string, path: string): Promise<void> {
  expectOk(await api.POST("/api/v1/operator/support-sessions/{id}/views", { params: { path: { id } }, body: { path } }));
}

export async function resolveFlag(id: number, status: "dismissed" | "actioned", note: string): Promise<AbuseFlag> {
  return unwrap(await api.POST("/api/v1/operator/flags/{id}/resolve", { params: { path: { id } }, body: { status, note } }));
}

export async function grantSupportAccess(duration: "24h" | "7d"): Promise<OrgSaaSState> {
  return unwrap(await api.PUT("/api/v1/orgs/current/support-access", { body: { duration } }));
}

export async function revokeSupportAccess(): Promise<OrgSaaSState> {
  return unwrap(await api.DELETE("/api/v1/orgs/current/support-access", {}));
}

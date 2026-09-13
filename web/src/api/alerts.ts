// Alerting API: docs/contracts/alerting.md, api.md "Alerting". Query factories follow queries.ts; mutations are
// plain async functions for useMutation. The server enforces permissions (roles.ts only hides actions).
import { keepPreviousData, queryOptions } from "@tanstack/react-query";
import { api, expectOk, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];
export type AlertRule = S["AlertRule"];
export type AlertRuleDetail = S["AlertRuleDetail"];
export type AlertRuleInput = S["AlertRuleInput"];
export type AlertRuleType = S["AlertRuleType"];
export type AlertRuleTypeInfo = S["AlertRuleTypeInfo"];
export type AlertCondition = S["AlertCondition"];
export type AlertFilter = S["AlertFilter"];
export type AlertFlapping = S["AlertFlapping"];
export type AlertSeverity = S["AlertSeverity"];
export type AlertOperator = S["AlertOperator"];
export type AlertSeriesState = S["AlertSeriesState"];
export type AlertIncident = S["AlertIncident"];
export type AlertIncidentDetail = S["AlertIncidentDetail"];
export type AlertIncidentEvent = S["AlertIncidentEvent"];
export type AlertIncidentState = S["AlertIncidentState"];
export type AlertChannel = S["AlertChannel"];
export type AlertChannelInput = S["AlertChannelInput"];
export type AlertChannelType = S["AlertChannelType"];
export type AlertChannelTestResult = S["AlertChannelTestResult"];
export type AlertMute = S["AlertMute"];
export type AlertMuteInput = S["AlertMuteInput"];
export type AlertMuteMatcher = S["AlertMuteMatcher"];
export type AlertMuteSchedule = S["AlertMuteSchedule"];
export type AlertMuteScheduleInput = S["AlertMuteScheduleInput"];
export type AlertRuleEvaluations = S["AlertRuleEvaluations"];
export type AlertDelivery = S["AlertDelivery"];
export type AlertRulePreview = S["AlertRulePreview"];
export type AlertPreviewSeries = S["AlertPreviewSeries"];
export type AlertNotificationStatus = S["AlertNotificationStatus"];

/** Incidents change while you look at them. */
export const INCIDENT_REFRESH_MS = 15_000;

export const alertRuleTypesQuery = () =>
  queryOptions({
    queryKey: ["alerts", "rule-types"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/alerts/rule-types", { signal })).types,
    staleTime: 5 * 60_000,
  });

export const alertRulesQuery = () =>
  queryOptions({
    queryKey: ["alerts", "rules"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/alerts/rules", { signal })).rules,
    refetchInterval: 30_000,
  });

export const alertRuleQuery = (id: string) =>
  queryOptions({
    queryKey: ["alerts", "rule", id],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/alerts/rules/{id}", { params: { path: { id } }, signal })),
  });

export interface IncidentFilter {
  state?: string;
  severity?: string;
}

export const alertIncidentsQuery = (f: IncidentFilter) =>
  queryOptions({
    queryKey: ["alerts", "incidents", f.state ?? "", f.severity ?? ""],
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/alerts/incidents", {
          params: { query: { state: f.state || undefined, severity: (f.severity || undefined) as AlertSeverity | undefined, limit: 200 } },
          signal,
        }),
      ),
    refetchInterval: INCIDENT_REFRESH_MS,
    placeholderData: keepPreviousData,
  });

export const alertIncidentQuery = (id: string) =>
  queryOptions({
    queryKey: ["alerts", "incident", id],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/alerts/incidents/{id}", { params: { path: { id } }, signal })),
    refetchInterval: INCIDENT_REFRESH_MS,
  });

export const alertChannelsQuery = () =>
  queryOptions({
    queryKey: ["alerts", "channels"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/alerts/channels", { signal })),
  });

export const alertMutesQuery = () =>
  queryOptions({
    queryKey: ["alerts", "mutes"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/alerts/mutes", { params: { query: { include_expired: true } }, signal })).mutes,
  });

export const alertDeliveriesQuery = (f: { channelId?: string; incidentId?: string } = {}) =>
  queryOptions({
    queryKey: ["alerts", "deliveries", f.channelId ?? "", f.incidentId ?? ""],
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/alerts/deliveries", {
          params: { query: { channel_id: f.channelId || undefined, incident_id: f.incidentId || undefined, limit: 200 } },
          signal,
        }),
      ).deliveries,
    refetchInterval: INCIDENT_REFRESH_MS,
  });

/** Metric names seen in the last 24 hours (metric picker). */
export const alertMetricNamesQuery = () =>
  queryOptions({
    queryKey: ["alerts", "metric-names"],
    queryFn: async ({ signal }) => {
      const now = Date.now();
      return unwrap(await api.GET("/api/v1/metrics/names", { params: { query: { from: String(now - 86_400_000), to: String(now) } }, signal })).names;
    },
    staleTime: 5 * 60_000,
  });

/** Preview of an unsaved definition; `input` null disables the query (invalid draft). */
export const alertPreviewQuery = (input: AlertRuleInput | null, hours: number) =>
  queryOptions({
    queryKey: ["alerts", "preview", input ? JSON.stringify(input) : "", hours],
    // openapi-fetch widens the [ms, value] point tuple to number[]; the schema type is exact.
    queryFn: async ({ signal }) => unwrap(await api.POST("/api/v1/alerts/rules/preview", { body: { rule: input!, hours }, signal })) as AlertRulePreview,
    enabled: input !== null,
    placeholderData: keepPreviousData,
    retry: false,
    staleTime: 30_000,
  });

/** Evaluation history of a saved rule over the last `hours` (GET /alerts/rules/{id}/evaluations). */
export const alertEvaluationsQuery = (id: string, hours: number) =>
  queryOptions({
    queryKey: ["alerts", "evaluations", id, hours],
    queryFn: async ({ signal }) => {
      const now = Date.now();
      const query = { from: String(now - hours * 3_600_000), to: String(now) };
      // openapi-fetch widens the [ms, value, state] point tuple; the schema type is exact.
      return unwrap(await api.GET("/api/v1/alerts/rules/{id}/evaluations", { params: { path: { id }, query }, signal })) as AlertRuleEvaluations;
    },
    refetchInterval: 60_000,
    placeholderData: keepPreviousData,
  });

export async function createAlertRule(input: AlertRuleInput): Promise<AlertRule> {
  return unwrap(await api.POST("/api/v1/alerts/rules", { body: input }));
}

export async function updateAlertRule(id: string, input: AlertRuleInput): Promise<AlertRule> {
  return unwrap(await api.PUT("/api/v1/alerts/rules/{id}", { params: { path: { id } }, body: input }));
}

export async function deleteAlertRule(id: string): Promise<void> {
  expectOk(await api.DELETE("/api/v1/alerts/rules/{id}", { params: { path: { id } } }));
}

export async function setAlertRuleEnabled(id: string, enabled: boolean): Promise<AlertRule> {
  return enabled
    ? unwrap(await api.POST("/api/v1/alerts/rules/{id}/enable", { params: { path: { id } } }))
    : unwrap(await api.POST("/api/v1/alerts/rules/{id}/disable", { params: { path: { id } } }));
}

export async function acknowledgeIncident(id: string): Promise<AlertIncident> {
  return unwrap(await api.POST("/api/v1/alerts/incidents/{id}/acknowledge", { params: { path: { id } } }));
}

export async function resolveIncident(id: string, note: string): Promise<AlertIncident> {
  return unwrap(await api.POST("/api/v1/alerts/incidents/{id}/resolve", { params: { path: { id } }, body: { note: note || undefined } }));
}

export async function addIncidentNote(id: string, text: string): Promise<AlertIncidentEvent> {
  return unwrap(await api.POST("/api/v1/alerts/incidents/{id}/notes", { params: { path: { id } }, body: { text } }));
}

export async function createAlertChannel(input: AlertChannelInput): Promise<AlertChannel> {
  return unwrap(await api.POST("/api/v1/alerts/channels", { body: input }));
}

export async function updateAlertChannel(id: string, input: AlertChannelInput): Promise<AlertChannel> {
  return unwrap(await api.PUT("/api/v1/alerts/channels/{id}", { params: { path: { id } }, body: input }));
}

export async function deleteAlertChannel(id: string): Promise<void> {
  expectOk(await api.DELETE("/api/v1/alerts/channels/{id}", { params: { path: { id } } }));
}

export async function testAlertChannel(id: string): Promise<AlertChannelTestResult> {
  return unwrap(await api.POST("/api/v1/alerts/channels/{id}/test", { params: { path: { id } } }));
}

export async function createAlertMute(input: AlertMuteInput): Promise<AlertMute> {
  return unwrap(await api.POST("/api/v1/alerts/mutes", { body: input }));
}

export async function updateAlertMute(id: string, input: AlertMuteInput): Promise<AlertMute> {
  return unwrap(await api.PUT("/api/v1/alerts/mutes/{id}", { params: { path: { id } }, body: input }));
}

export async function deleteAlertMute(id: string): Promise<void> {
  expectOk(await api.DELETE("/api/v1/alerts/mutes/{id}", { params: { path: { id } } }));
}

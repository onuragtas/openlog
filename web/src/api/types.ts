import type { components } from "./schema.gen";

type S = components["schemas"];

export type Host = S["Host"];
export type MetricMeta = S["MetricMeta"];
export type MetricSeries = S["MetricSeries"];
export type MetricSeriesResponse = S["MetricSeriesResponse"];
export type Aggregation = S["Aggregation"];
export type InventoryItem = S["InventoryItem"];
export type InventoryResponse = S["InventoryResponse"];
export type InventorySearchItem = S["InventorySearchItem"];
export type DiscoveredService = S["DiscoveredService"];
export type LogRecord = S["LogRecord"];
export type Span = S["Span"];
export type SpanEvent = S["SpanEvent"];
export type Trace = S["Trace"];
export type ApiErrorBody = S["Error"];

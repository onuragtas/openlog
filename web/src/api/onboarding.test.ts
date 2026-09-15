import { QueryClient } from "@tanstack/react-query";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import { server } from "@/mocks/server";
import { verifyLogsFilters, verifyLogsQuery } from "./onboarding";

describe("verifyLogsQuery (explorer query API)", () => {
  it("maps service and source to explorer filters", () => {
    expect(verifyLogsFilters({})).toEqual([]);
    expect(verifyLogsFilters({ service: "billing" })).toEqual([{ key: "service.name", op: "=", value: "billing" }]);
    expect(verifyLogsFilters({ source: "journald" })).toEqual([{ key: "attributes.openlog.log.source", op: "=", value: "journald" }]);
  });

  it("asks POST /api/v1/logs/query for the newest matching record since the flow started", async () => {
    let body: Record<string, unknown> | undefined;
    server.use(
      http.post("*/api/v1/logs/query", async ({ request }) => {
        body = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ rows: [{ id: "1", body: "hello" }], next_cursor: null });
      }),
    );
    const since = Date.now() - 60_000;
    const rows = await new QueryClient().fetchQuery(verifyLogsQuery({ source: "file" }, since));
    expect(rows).toHaveLength(1);
    expect(body).toMatchObject({
      from: since,
      order: "desc",
      limit: 1,
      include_record: false,
      filters: [{ key: "attributes.openlog.log.source", op: "=", value: "file" }],
    });
    expect(body!.to as number).toBeGreaterThan(since);
  });

  it("sends no filters for an empty filter and returns no rows when nothing arrived", async () => {
    let body: Record<string, unknown> | undefined;
    server.use(
      http.post("*/api/v1/logs/query", async ({ request }) => {
        body = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({ rows: [], next_cursor: null });
      }),
    );
    const rows = await new QueryClient().fetchQuery(verifyLogsQuery({}, Date.now()));
    expect(rows).toEqual([]);
    expect(body).not.toHaveProperty("filters");
  });
});

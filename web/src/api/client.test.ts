import { QueryClient } from "@tanstack/react-query";
import { http, HttpResponse } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MOCK_API_KEY, MOCK_EMAIL, MOCK_PASSWORD, MOCK_STAGING_ORG_ID, mockAuth } from "@/mocks/account";
import { server } from "@/mocks/server";
import { login, logout, meQuery } from "./account";
import { CSRF_HEADER, getCsrfToken, getSelectedOrg, ORG_HEADER, setCsrfToken, setSelectedOrg } from "./auth";
import { ApiError, api, setUnauthorizedHandler, unwrap } from "./client";

afterEach(() => setUnauthorizedHandler(null));

describe("session auth", () => {
  it("login stores the CSRF token and authenticates API requests", async () => {
    const me = await login(MOCK_EMAIL, MOCK_PASSWORD);
    expect(me.auth).toBe("session");
    expect(me.user?.email).toBe(MOCK_EMAIL);
    expect(me.role).toBe("owner");
    expect(me.organizations.length).toBe(2);
    expect(getCsrfToken()).toBe(mockAuth.get().csrf);
    const hosts = unwrap(await api.GET("/api/v1/hosts")).hosts;
    expect(hosts.length).toBeGreaterThan(0);
    expect(hosts[0]!.last_seen).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{9}Z$/);
  });

  it("rejects wrong credentials without reporting an expired session", async () => {
    const onUnauthorized = vi.fn();
    setUnauthorizedHandler(onUnauthorized);
    await expect(login(MOCK_EMAIL, "wrong password")).rejects.toMatchObject({ status: 401, code: "unauthenticated" });
    expect(onUnauthorized).not.toHaveBeenCalled();
    expect(getCsrfToken()).toBeNull();
  });

  it("sends X-CSRF-Token only on mutations and the selected organization on every request", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const seen: Record<string, string | null> = {};
    server.use(
      http.get("*/api/v1/hosts", ({ request }) => {
        seen.getCsrf = request.headers.get(CSRF_HEADER);
        seen.getOrg = request.headers.get(ORG_HEADER);
        return HttpResponse.json({ hosts: [] });
      }),
      http.delete("*/api/v1/license-keys/:id", ({ request }) => {
        seen.deleteCsrf = request.headers.get(CSRF_HEADER);
        seen.deleteOrg = request.headers.get(ORG_HEADER);
        return new HttpResponse(null, { status: 204 });
      }),
    );
    setSelectedOrg(MOCK_STAGING_ORG_ID);
    await api.GET("/api/v1/hosts");
    await api.DELETE("/api/v1/license-keys/{id}", { params: { path: { id: "lk-1" } } });
    expect(seen.getCsrf).toBeNull();
    expect(seen.getOrg).toBe(MOCK_STAGING_ORG_ID);
    expect(seen.deleteCsrf).toBe(getCsrfToken());
    expect(seen.deleteOrg).toBe(MOCK_STAGING_ORG_ID);
  });

  it("mutations without the CSRF token are rejected (like the API)", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    setCsrfToken(null);
    const res = await api.POST("/api/v1/license-keys", { body: { name: "x" } });
    expect(res.response.status).toBe(403);
  });

  it("a 401 after sign-in clears the session and calls the unauthorized handler", async () => {
    const onUnauthorized = vi.fn();
    setUnauthorizedHandler(onUnauthorized);
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    mockAuth.signOut(); // session revoked on the server
    const res = await api.GET("/api/v1/hosts");
    expect(res.response.status).toBe(401);
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
    expect(getCsrfToken()).toBeNull();
    expect(() => unwrap(res)).toThrowError(ApiError);
  });

  it("a signed-out visitor's 401 is not an expired session", async () => {
    const onUnauthorized = vi.fn();
    setUnauthorizedHandler(onUnauthorized);
    const res = await api.GET("/api/v1/auth/me");
    expect(res.response.status).toBe(401);
    expect(onUnauthorized).not.toHaveBeenCalled();
  });

  it("does not call the unauthorized handler for other errors", async () => {
    const onUnauthorized = vi.fn();
    setUnauthorizedHandler(onUnauthorized);
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const res = await api.GET("/api/v1/traces/{trace_id}", { params: { path: { trace_id: "0".repeat(32) } } });
    expect(res.response.status).toBe(404);
    expect(onUnauthorized).not.toHaveBeenCalled();
    expect(() => unwrap(res)).toThrow(expect.objectContaining({ code: "not_found", status: 404 }));
  });

  it("maps 400 error envelopes", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    const res = await api.GET("/api/v1/inventory/search", { params: { query: { category: "" } } });
    expect(() => unwrap(res)).toThrow(expect.objectContaining({ code: "invalid_argument", message: "category is required" }));
  });

  it("falls back to the default organization when the remembered one is gone", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    setSelectedOrg("00000000-0000-4000-8000-000000000000");
    const me = await new QueryClient().fetchQuery(meQuery());
    expect(me.organization?.tenant_id).toBe("default");
    expect(getSelectedOrg()).toBeNull();
  });

  it("API keys authenticate with Authorization: Bearer", async () => {
    const res = await api.GET("/api/v1/hosts", { headers: { Authorization: `Bearer ${MOCK_API_KEY}` } });
    expect(res.response.status).toBe(200);
    const me = unwrap(await api.GET("/api/v1/auth/me", { headers: { Authorization: `Bearer ${MOCK_API_KEY}` } }));
    expect(me.auth).toBe("api_key");
    expect(me.csrf_token).toBeNull();
  });

  it("logout ends the session", async () => {
    await login(MOCK_EMAIL, MOCK_PASSWORD);
    setSelectedOrg(MOCK_STAGING_ORG_ID);
    await logout();
    expect(getCsrfToken()).toBeNull();
    expect(getSelectedOrg()).toBeNull();
    expect((await api.GET("/api/v1/hosts")).response.status).toBe(401);
  });
});

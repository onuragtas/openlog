import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterAll, afterEach, beforeAll } from "vitest";
import { clearAuthState } from "@/api/auth";
import "@/i18n";
import { resetMockAccounts } from "@/mocks/account";
import { server } from "@/mocks/server";

beforeAll(() => server.listen({ onUnhandledRequest: "error" }));
afterEach(() => {
  server.resetHandlers();
  cleanup();
  localStorage.clear();
  clearAuthState();
  resetMockAccounts();
});
afterAll(() => server.close());

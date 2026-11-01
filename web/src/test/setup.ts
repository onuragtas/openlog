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
  // A list keeps its sort in the address (lib/list.ts), and the next test must not start on it.
  window.history.replaceState(null, "", window.location.pathname);
  localStorage.clear();
  clearAuthState();
  resetMockAccounts();
});
afterAll(() => server.close());

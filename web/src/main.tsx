import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { ApiError, setUnauthorizedHandler } from "@/api/client";
import "@/i18n";
import { createMutationCache } from "@/lib/org-writable";
import { ThemeProvider } from "@/lib/theme";
import { buildRouter } from "@/router";
import "./index.css";

async function enableMocking() {
  // `npm run dev:mock` / e2e: serve API responses from MSW instead of a backend.
  if (import.meta.env.VITE_USE_MSW !== "true") return;
  const { worker } = await import("./mocks/browser");
  await worker.start({ onUnhandledRequest: "bypass", quiet: true });
}

// A mutation rejected with 403 org_suspended refetches the SaaS state, so the whole UI turns read-only (lib/org-writable.tsx).
const queryClient: QueryClient = new QueryClient({
  mutationCache: createMutationCache(() => queryClient),
  defaultOptions: {
    queries: {
      staleTime: 15_000,
      refetchOnWindowFocus: false,
      retry: (failureCount, error) => {
        if (error instanceof ApiError && error.status < 500) return false;
        return failureCount < 2;
      },
    },
  },
});

const router = buildRouter(queryClient);

setUnauthorizedHandler(() => {
  queryClient.clear();
  const { pathname, searchStr } = router.state.location;
  if (pathname === "/login") return;
  void router.navigate({ to: "/login", search: { redirect: pathname + searchStr, expired: true } });
});

void enableMocking().then(() => {
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <ThemeProvider>
        <QueryClientProvider client={queryClient}>
          <RouterProvider router={router} />
        </QueryClientProvider>
      </ThemeProvider>
    </StrictMode>,
  );
});

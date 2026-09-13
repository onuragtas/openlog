/// <reference types="vitest/config" />
import { rmSync } from "node:fs";
import { fileURLToPath, URL } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin } from "vite";

/** public/mockServiceWorker.js is only for dev:mock and e2e; never ship it. */
const stripMswWorker = (): Plugin => ({
  name: "openlog:strip-msw-worker",
  apply: "build",
  closeBundle() {
    rmSync(fileURLToPath(new URL("./dist/mockServiceWorker.js", import.meta.url)), { force: true });
  },
});

// `vite --mode mock` (npm run dev:mock) serves the app with MSW mocks instead
// of proxying /api to a running openlog-api.
export default defineConfig(({ mode }) => ({
  plugins: [react(), tailwindcss(), stripMswWorker()],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  define: {
    "import.meta.env.VITE_USE_MSW": JSON.stringify(mode === "mock" ? "true" : (process.env.VITE_USE_MSW ?? "false")),
  },
  server: {
    port: 5173,
    proxy: mode === "mock" ? undefined : { "/api": { target: "http://localhost:8080", changeOrigin: true } },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
    chunkSizeWarningLimit: 400,
    rolldownOptions: {
      output: {
        // Routes are lazy (router.tsx); long-lived vendor code gets its own cacheable chunks.
        codeSplitting: {
          groups: [
            { name: "vendor-uplot", test: /[\\/]node_modules[\\/]uplot[\\/]/, priority: 40 },
            { name: "vendor-react", test: /[\\/]node_modules[\\/](react|react-dom|scheduler)[\\/]/, priority: 30 },
            { name: "vendor-tanstack", test: /[\\/]node_modules[\\/]@tanstack[\\/](react-router|router-core|history|react-store|store|react-query|query-core)[\\/]/, priority: 30 },
            { name: "vendor-i18n", test: /[\\/]node_modules[\\/](i18next|react-i18next|i18next-browser-languagedetector)[\\/]/, priority: 30 },
            { name: "vendor-radix", test: /[\\/]node_modules[\\/](radix-ui|@radix-ui)[\\/]/, priority: 20 },
          ],
        },
      },
    },
  },
  test: {
    environment: "jsdom",
    globals: false,
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
    css: false,
  },
}));

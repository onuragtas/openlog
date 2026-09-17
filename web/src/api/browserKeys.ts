// Browser keys of the RUM SDK (docs/contracts/rum.md §3, api.md "Browser keys"). The value is returned only
// by create: a browser key is public by construction — it ships inside a web page — but the API is still not
// a place to read credentials back from, so a lost key is replaced rather than looked up.
import { queryOptions } from "@tanstack/react-query";
import { api, expectOk, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];

export type BrowserKey = S["BrowserKey"];
export type BrowserKeyInput = S["BrowserKeyInput"];

export const browserKeysQuery = () =>
  queryOptions({
    queryKey: ["settings", "browser-keys"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/browser-keys", { signal })).browser_keys,
  });

/** Returns the key with its value; the value is shown once and never again. */
export async function createBrowserKey(input: BrowserKeyInput) {
  return unwrap(await api.POST("/api/v1/browser-keys", { body: input }));
}

export async function updateBrowserKey(id: string, input: BrowserKeyInput): Promise<BrowserKey> {
  return unwrap(await api.PUT("/api/v1/browser-keys/{id}", { params: { path: { id } }, body: input }));
}

/** Soft delete: the value stays permanently unusable, because it is still cached in loaded pages (rum.md §3.5). */
export async function revokeBrowserKey(id: string): Promise<void> {
  expectOk(await api.DELETE("/api/v1/browser-keys/{id}", { params: { path: { id } } }));
}

// Source maps of browser applications (docs/contracts/api.md "Source maps", rum.md §8). The upload is the
// only request in the app that sends a binary body: the document goes as it is, through openapi-fetch so the
// CSRF and organization headers of client.ts still apply.
import { queryOptions } from "@tanstack/react-query";
import { api, expectOk, unwrap } from "./client";
import type { components } from "./schema.gen";

type S = components["schemas"];

export type SourceMap = S["SourceMap"];

/** 32 MiB, the server's limit; checked here so a mistake is caught before the upload starts. */
export const MAX_SOURCE_MAP_BYTES = 32 * 1024 * 1024;

export const sourceMapsQuery = () =>
  queryOptions({
    queryKey: ["settings", "source-maps"],
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/source-maps", { signal })).source_maps,
  });

/** The file name a stack frame carries: no directory, query string or fragment (the server refuses those). */
export function scriptNameOf(fileName: string): string {
  const cut = fileName.split(/[?#]/)[0] ?? fileName;
  return cut.slice(cut.lastIndexOf("/") + 1);
}

/** Drops the ".map" a bundler appends, so uploading "main.3f2a1b9c.js.map" keys on the script it belongs to. */
export function scriptFromMapName(fileName: string): string {
  const name = scriptNameOf(fileName);
  return name.endsWith(".map") ? name.slice(0, -4) : name;
}

export async function uploadSourceMap(app: string, script: string, document: Blob): Promise<SourceMap> {
  const res = await api.POST("/api/v1/source-maps", {
    params: { query: { app, script } },
    body: document as never,
    bodySerializer: (b: unknown) => b as BodyInit,
    headers: { "content-type": "application/octet-stream" },
  });
  return unwrap(res).source_map;
}

export async function deleteSourceMap(id: string): Promise<void> {
  expectOk(await api.DELETE("/api/v1/source-maps/{id}", { params: { path: { id } } }));
}

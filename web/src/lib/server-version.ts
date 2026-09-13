import { useSyncExternalStore } from "react";

/** Response header carrying the backend version (docs/contracts/releases-updates.md §5). */
export const VERSION_HEADER = "X-Openlog-Version";

// The first version seen is the backend this page (and its lazily loaded, content-hashed chunks)
// was served by. A later response from another version means the backend was updated: old chunks
// may be gone, so the user is asked to reload. Sticky: during a rolling update responses can
// alternate between versions, the prompt stays once a new version was seen.
let initial: string | null = null;
let updatedTo: string | null = null;
const listeners = new Set<() => void>();

/** Records the version of one API response. */
export function observeServerVersion(version: string | null | undefined): void {
  const v = version?.trim();
  if (!v) return;
  if (initial === null) {
    initial = v;
    return;
  }
  if (v !== initial && v !== updatedTo) {
    updatedTo = v;
    listeners.forEach((l) => l());
  }
}

/** The version the backend was updated to since the page loaded, or null. */
export function getUpdatedServerVersion(): string | null {
  return updatedTo;
}

/** Test helper: forget observed versions. */
export function resetServerVersion(): void {
  initial = null;
  updatedTo = null;
  listeners.forEach((l) => l());
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/** Re-renders when the backend version changes; returns the new version or null. */
export function useUpdatedServerVersion(): string | null {
  return useSyncExternalStore(subscribe, getUpdatedServerVersion, getUpdatedServerVersion);
}

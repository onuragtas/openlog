/**
 * Client-side rules for imported ingest license key values, mirroring the API
 * (docs/contracts/api.md, POST /api/v1/license-keys with "key"): 16–256
 * printable ASCII characters without spaces, quotes or backslashes. The server
 * validates again; this only gives inline feedback.
 */
export const CUSTOM_KEY_MIN = 16;
export const CUSTOM_KEY_MAX = 256;

const allowed = /^[\x21\x23-\x26\x28-\x5b\x5d-\x7e]*$/;

/** Returns why a (trimmed) value is invalid, or null when it is valid. */
export function customKeyProblem(value: string): "length" | "chars" | null {
  if (!allowed.test(value)) return "chars";
  if (value.length < CUSTOM_KEY_MIN || value.length > CUSTOM_KEY_MAX) return "length";
  return null;
}

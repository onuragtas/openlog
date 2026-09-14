// License key choice of the Add data flow (components/onboarding/LicenseKeyStep.tsx). The value lives in React state
// only: never in the URL, storage or any request except POST /api/v1/license-keys when the user creates a key.
import { customKeyProblem } from "@/api/licenseKeyValue";

export type KeyMode = "create" | "paste" | "placeholder";

export interface KeyChoice {
  mode: KeyMode;
  /** Value pasted by the user ("I have a key"). */
  pasted: string;
  /** Id of the existing key the pasted value belongs to ("" = unknown), for a prefix check. */
  pasteFor: string;
  /** Key created in this flow; its value is shown once. */
  created: { name: string; key: string } | null;
}

export function initialKeyChoice(canCreate: boolean): KeyChoice {
  return { mode: canCreate ? "create" : "placeholder", pasted: "", pasteFor: "", created: null };
}

/** Problem of a pasted value: only characters are checked (short development keys are valid). */
export function pastedKeyProblem(value: string): "chars" | null {
  const v = value.trim();
  return v !== "" && customKeyProblem(v) === "chars" ? "chars" : null;
}

export function keyChoiceReady(c: KeyChoice): boolean {
  switch (c.mode) {
    case "placeholder":
      return true;
    case "create":
      return c.created !== null;
    case "paste":
      return c.pasted.trim() !== "" && pastedKeyProblem(c.pasted) === null;
  }
}

/** The value inserted into the commands ("" = the <LICENSE_KEY> placeholder). */
export function keyForCommands(c: KeyChoice): string {
  if (c.mode === "create") return c.created?.key ?? "";
  if (c.mode === "paste") return pastedKeyProblem(c.pasted) === null ? c.pasted.trim() : "";
  return "";
}

/** Translation of a key computed at runtime from typed ids (targets, blocks, notes, option values). */
export function tDynamic(t: unknown, key: string, options?: Record<string, unknown>): string {
  return (t as (k: string, o?: Record<string, unknown>) => string)(key, options);
}

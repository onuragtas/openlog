import { useSyncExternalStore } from "react";
import { subscribeSupportSession, supportSessionId } from "@/api/supportSession";

/** The active support session id, re-rendering when a support view starts or ends. */
export function useSupportSessionId(): string | null {
  return useSyncExternalStore(subscribeSupportSession, supportSessionId, () => null);
}

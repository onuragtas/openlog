// Java agent helpers of the fleet views (java-agent.md §2, D-123).
import type { FleetHostJavaAgent } from "@/api/fleet";

type Tone = "default" | "secondary" | "outline" | "warning" | "destructive";

/** Tone of the agent-reported state (installed, restart_pending, unmanaged, …). */
export function javaStateTone(state: string): Tone {
  switch (state) {
    case "installed":
      return "default";
    case "restart_pending":
    case "unmanaged":
      return "warning";
    case "error":
      return "destructive";
    case "staged":
      return "secondary";
    default:
      return "outline";
  }
}

/** Tone of the fleet decision. */
export function javaStatusTone(status: FleetHostJavaAgent["status"]): Tone {
  switch (status) {
    case "offer":
      return "secondary";
    case "up_to_date":
      return "default";
    case "already_failed":
    case "not_capable":
      return "warning";
    default:
      return "outline";
  }
}

/** Whether the host has anything Java-related to show (JVMs, a managed or foreign jar, an override, an operation). */
export function hasJavaAgent(p: FleetHostJavaAgent): boolean {
  return p.reported && (p.jvms.length > 0 || !!p.version || p.state === "unmanaged" || p.state === "error" || !!p.override || p.update !== null);
}

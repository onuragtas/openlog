import i18n from "@/i18n";

type Translate = (key: string, options?: Record<string, string>) => string;
const t = (key: string, options?: Record<string, string>) => (i18n.t as unknown as Translate)(key, options);

/**
 * An updater message (GET /api/v1/version: updater.message, update_requests.latest.message)
 * translated by its message_code (update.messages.<code>, internal/updatemsg) with message_params.
 * Falls back to the English message for documents without a code (older updaters, free text) and
 * for codes this UI does not know. An appended English error (params.error) is kept.
 */
export function updateMessage(message: string | undefined, code?: string | null, params?: Record<string, string> | null): string {
  const key = `update.messages.${code}`;
  if (!code || code === "withError" || !i18n.exists(key)) return message ?? "";
  const { error, ...rest } = params ?? {};
  const text = t(key, rest);
  return error ? t("update.messages.withError", { message: text, error }) : text;
}

/** An update step name (backup, pull, …), or the raw name when unknown. */
export function updateStep(name: string): string {
  const key = `update.steps.${name}`;
  return i18n.exists(key) ? t(key) : name;
}

/** An updater state (up_to_date, updating, …), or the raw state when unknown. */
export function updateState(state: string): string {
  const key = `update.states.${state}`;
  return i18n.exists(key) ? t(key) : state;
}

import type { TFunction } from "i18next";
import { ApiError } from "@/api/client";

/** User-facing message of a failed query: the API message for 400 (already "line L, column C: …"), friendly texts otherwise. */
export function queryErrorMessage(error: unknown, t: TFunction): string {
  if (error instanceof ApiError) {
    switch (error.status) {
      case 400:
        return error.message;
      case 422:
        return t("oql.errors.limit");
      case 429:
        return t("oql.errors.rateLimited");
      case 504:
        return t("oql.errors.timeout");
      default:
        return t("common.errorDetail", { message: error.message, code: error.code });
    }
  }
  return error instanceof Error ? error.message : String(error);
}

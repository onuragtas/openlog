import { useInfiniteQuery } from "@tanstack/react-query";
import { useMemo } from "react";
import { logsInfiniteQuery, type LogsRequest } from "@/api/queries";
import { concatLogPages } from "./logs";

/** Paged log listing: rows of all loaded pages plus "load older" controls. */
export function useLogPages(request: LogsRequest) {
  const query = useInfiniteQuery(logsInfiniteQuery(request));
  const logs = useMemo(() => (query.data ? concatLogPages(query.data.pages) : undefined), [query.data]);
  return { query, logs };
}

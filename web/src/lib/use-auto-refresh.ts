import { useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { isRefreshing, refreshActiveQueries, startAutoRefresh } from "@/lib/auto-refresh";

/** Ticks `refreshActiveQueries` every `intervalMs` (null = no timer); skips while a mounted query is still fetching. */
export function useAutoRefresh(intervalMs: number | null): void {
  const client = useQueryClient();
  useEffect(() => {
    if (!intervalMs) return;
    return startAutoRefresh({ intervalMs, refresh: () => void refreshActiveQueries(client), isBusy: () => isRefreshing(client) });
  }, [client, intervalMs]);
}

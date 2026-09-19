import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/** Query key namespace for POPO installations and Windows bridges.
 * Realtime sync invalidates `installations(wsId)` on `popo_installation:*`. */
export const popoKeys = {
  all: (wsId: string) => ["popo", wsId] as const,
  installations: (wsId: string) => [...popoKeys.all(wsId), "installations"] as const,
  bridges: (wsId: string) => [...popoKeys.all(wsId), "bridges"] as const,
};

export const popoInstallationsOptions = (wsId: string) =>
  queryOptions({
    queryKey: popoKeys.installations(wsId),
    queryFn: () => api.listPopoInstallations(wsId),
    enabled: !!wsId,
  });

export const popoBridgesOptions = (wsId: string) =>
  queryOptions({
    queryKey: popoKeys.bridges(wsId),
    queryFn: () => api.listPopoBridges(wsId),
    enabled: !!wsId,
    // Heartbeat is 15s; 45s without one is offline. Refresh so the list
    // does not sit on a stale online flag until the next navigation.
    refetchInterval: 15_000,
  });

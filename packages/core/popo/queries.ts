import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/** Query key namespace for POPO installations. Realtime sync invalidates
 * `installations(wsId)` on `popo_installation:*` events. */
export const popoKeys = {
  all: (wsId: string) => ["popo", wsId] as const,
  installations: (wsId: string) => [...popoKeys.all(wsId), "installations"] as const,
};

export const popoInstallationsOptions = (wsId: string) =>
  queryOptions({
    queryKey: popoKeys.installations(wsId),
    queryFn: () => api.listPopoInstallations(wsId),
    enabled: !!wsId,
  });

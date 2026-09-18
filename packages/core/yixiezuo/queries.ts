import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const yixiezuoKeys = {
  all: (wsId: string) => ["yixiezuo", wsId] as const,
  connection: (wsId: string) => [...yixiezuoKeys.all(wsId), "connection"] as const,
};

export const yixiezuoConnectionOptions = (wsId: string) =>
  queryOptions({
    queryKey: yixiezuoKeys.connection(wsId),
    queryFn: () => api.getYixiezuoConnection(wsId),
    enabled: !!wsId,
  });

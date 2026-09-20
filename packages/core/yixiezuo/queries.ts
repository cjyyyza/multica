import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const yixiezuoKeys = {
  all: (wsId: string) => ["yixiezuo", wsId] as const,
  imported: (wsId: string, issueId: string) => [...yixiezuoKeys.all(wsId), "import", issueId] as const,
  operation: (wsId: string, operationId: string) => [...yixiezuoKeys.all(wsId), "operation", operationId] as const,
};

export const yixiezuoImportStatesOptions = (wsId: string) => queryOptions({
  queryKey: [...yixiezuoKeys.all(wsId), "states"],
  queryFn: () => api.listYixiezuoImportStates(wsId),
  enabled: !!wsId,
  staleTime: 10_000,
  refetchInterval: 10_000,
});

export const yixiezuoImportOptions = (wsId: string, issueId: string) =>
  queryOptions({
    queryKey: yixiezuoKeys.imported(wsId, issueId),
    queryFn: () => api.getYixiezuoImport(wsId, issueId),
    enabled: !!wsId && !!issueId,
    refetchInterval: (query) => ["pending", "running"].includes(query.state.data?.import?.state ?? "") ? 1500 : false,
  });

export const yixiezuoOperationOptions = (wsId: string, operationId: string) =>
  queryOptions({
    queryKey: yixiezuoKeys.operation(wsId, operationId),
    queryFn: () => api.getYixiezuoOperation(wsId, operationId),
    enabled: !!wsId && !!operationId,
    refetchInterval: (query) => ["pending", "running"].includes(query.state.data?.state ?? "pending") ? 1500 : false,
  });

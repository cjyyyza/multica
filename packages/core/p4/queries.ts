import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const p4Keys = {
  /** Every changelist query, for the `p4_changelist:updated` invalidation. */
  changelistsAll: () => ["p4", "changelists"] as const,
  changelists: (wsId: string, issueId: string) =>
    [...p4Keys.changelistsAll(), wsId, issueId] as const,
  content: (wsId: string, issueId: string, changelistId: string, file: string) =>
    [...p4Keys.changelists(wsId, issueId), "content", changelistId, file] as const,
};

export const issueP4ChangelistsOptions = (wsId: string, issueId: string) =>
  queryOptions({
    queryKey: p4Keys.changelists(wsId, issueId),
    queryFn: () => api.listIssueP4Changelists(issueId),
    enabled: !!wsId && !!issueId,
  });

export const p4ChangelistContentOptions = (wsId: string, issueId: string, changelistId: string, file = "") =>
  queryOptions({
    queryKey: p4Keys.content(wsId, issueId, changelistId, file),
    queryFn: async ({ signal }) => {
      let request = await api.requestIssueP4Content(issueId, changelistId, file, signal);
      const deadline = Date.now() + 125_000;
      while (request.status === "pending" || request.status === "running") {
        if (Date.now() >= deadline) throw new Error("Perforce content request timed out");
        await new Promise((resolve) => setTimeout(resolve, 500));
        signal.throwIfAborted();
        request = await api.getIssueP4Content(issueId, changelistId, request.id, signal);
      }
      if (request.status !== "completed" || !request.result) {
        throw new Error(request.error || "Could not read changelist content");
      }
      return request.result;
    },
    enabled: !!wsId && !!issueId && !!changelistId,
    staleTime: 0,
    retry: false,
    refetchOnWindowFocus: false,
  });

import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const p4Keys = {
  /** Every changelist query, for the `p4_changelist:updated` invalidation. */
  changelistsAll: () => ["p4", "changelists"] as const,
  changelists: (wsId: string, issueId: string) =>
    [...p4Keys.changelistsAll(), wsId, issueId] as const,
};

export const issueP4ChangelistsOptions = (wsId: string, issueId: string) =>
  queryOptions({
    queryKey: p4Keys.changelists(wsId, issueId),
    queryFn: () => api.listIssueP4Changelists(issueId),
    enabled: !!wsId && !!issueId,
  });

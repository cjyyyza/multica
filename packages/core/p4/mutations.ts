import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { LinkIssueP4ChangelistRequest } from "../types";
import { p4Keys } from "./queries";

// Both writes await the server and then refetch. A linked changelist's status,
// file count and Swarm review are filled in by the daemon's next poll, so there
// is no predictable local result to patch in; an unlink removes a row a person
// may have just read numbers off, which rules out an optimistic delete.
export function useLinkIssueP4Changelist(wsId: string, issueId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: LinkIssueP4ChangelistRequest) => api.linkIssueP4Changelist(issueId, req),
    onSuccess: () => qc.invalidateQueries({ queryKey: p4Keys.changelists(wsId, issueId) }),
  });
}

export function useUnlinkIssueP4Changelist(wsId: string, issueId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (changelistId: string) => api.unlinkIssueP4Changelist(issueId, changelistId),
    onSuccess: () => qc.invalidateQueries({ queryKey: p4Keys.changelists(wsId, issueId) }),
  });
}

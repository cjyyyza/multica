import type { QueryClient } from "@tanstack/react-query";
import { chatKeys } from "../chat/queries";
import { issueKeys } from "../issues/queries";
import { requirementKeys } from "./queries";
import type {
  RequirementApproveResponse,
  RequirementDetail,
  RequirementOpenResponse,
} from "./schemas";

export function writeAuthoritativeDetail(
  qc: QueryClient,
  wsId: string,
  detail: RequirementDetail,
) {
  qc.setQueryData(requirementKeys.detail(wsId, detail.id), detail);
  if (detail.chatSessionId) {
    qc.setQueryData(requirementKeys.byChat(wsId, detail.chatSessionId), detail);
  }
}

export function invalidateRequirementDetail(
  qc: QueryClient,
  wsId: string,
  requirementId: string,
) {
  const detailKey = requirementKeys.detail(wsId, requirementId);
  const detail = qc.getQueryData<RequirementDetail>(detailKey);
  qc.invalidateQueries({ queryKey: detailKey });
  if (detail?.chatSessionId) {
    qc.invalidateQueries({
      queryKey: requirementKeys.byChat(wsId, detail.chatSessionId),
    });
  }
}

export function invalidateOpenedRequirementChat(
  qc: QueryClient,
  wsId: string,
  result: RequirementOpenResponse,
) {
  const sessionId = result.session.id;
  qc.invalidateQueries({ queryKey: chatKeys.sessions(wsId) });
  qc.invalidateQueries({ queryKey: chatKeys.session(wsId, sessionId) });
  qc.invalidateQueries({ queryKey: chatKeys.messages(sessionId) });
  qc.invalidateQueries({ queryKey: chatKeys.messagesPage(sessionId) });
  qc.invalidateQueries({ queryKey: chatKeys.pendingTask(sessionId) });
}

export function invalidateApprovedRequirementIssue(
  qc: QueryClient,
  wsId: string,
  result: RequirementApproveResponse,
) {
  const issueId = result.issue.id;
  qc.invalidateQueries({ queryKey: issueKeys.detail(wsId, issueId) });
  qc.invalidateQueries({ queryKey: issueKeys.list(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.tasks(issueId) });
  qc.invalidateQueries({ queryKey: issueKeys.timeline(issueId) });
}

import type { QueryClient } from "@tanstack/react-query";
import type { RequirementEventPayload } from "../types/events";
import { requirementKeys } from "./queries";

export function applyRequirementEventToCache(
  qc: QueryClient,
  wsId: string,
  payload: RequirementEventPayload,
) {
  if (payload.workspaceId !== wsId) return;
  if (
    !payload.requirementId ||
    !Number.isSafeInteger(payload.revision) ||
    payload.revision <= 0
  ) {
    qc.invalidateQueries({ queryKey: requirementKeys.all(wsId) });
    return;
  }
  qc.invalidateQueries({
    queryKey: requirementKeys.detail(wsId, payload.requirementId),
  });
  if (payload.chatSessionId) {
    qc.invalidateQueries({
      queryKey: requirementKeys.byChat(wsId, payload.chatSessionId),
    });
  }
}

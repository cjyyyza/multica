import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const requirementKeys = {
  all: (wsId: string) => ["requirements", wsId] as const,
  detail: (wsId: string, requirementId: string) =>
    [...requirementKeys.all(wsId), "detail", requirementId] as const,
  byChat: (wsId: string, chatSessionId: string) =>
    [...requirementKeys.all(wsId), "byChat", chatSessionId] as const,
};

export const requirementDetailOptions = (wsId: string, requirementId: string) =>
  queryOptions({
    queryKey: requirementKeys.detail(wsId, requirementId),
    queryFn: () => api.getRequirement(wsId, requirementId),
    enabled: !!wsId && !!requirementId,
  });

/**
 * Legacy chat-scoped lookup: `GET /requirements?chat_session_id=`.
 *
 * Only for a Chat whose `requirement_id` projection is missing — a server that
 * predates the projection, or a caller that has no ChatSession payload. A
 * current server emits `requirement_id` on every session (`null` = definitely
 * unbound), so ordinary Chats must NOT use this probe: see
 * `useRequirementSurface` for the compatibility-window rationale. Delete it
 * together with the undefined branch once no supported server omits the
 * projection (plan P2).
 */
export const requirementByChatOptions = (wsId: string, chatSessionId: string) =>
  queryOptions({
    queryKey: requirementKeys.byChat(wsId, chatSessionId),
    queryFn: () => api.getRequirementByChat(wsId, chatSessionId),
    enabled: !!wsId && !!chatSessionId,
  });

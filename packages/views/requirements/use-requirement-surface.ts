"use client";

import { useQuery } from "@tanstack/react-query";
import {
  requirementByChatOptions,
  requirementDetailOptions,
  type RequirementDetail,
} from "@multica/core/requirements";
import type { ChatSession } from "@multica/core/types";
import { useAuthStore } from "@multica/core/auth";

export interface RequirementSurfaceState {
  detail: RequirementDetail | null;
  error: Error | null;
  isLoading: boolean;
  canCompose: boolean;
  projectLocked: boolean;
}

export function requirementCanCompose(detail: RequirementDetail, userId?: string | null): boolean {
  if (!userId || !detail.chatSessionId || detail.issueId || detail.state === "cancelled" || detail.cancelledAt) return false;
  return detail.requesterId === userId || detail.participants.some((participant) =>
    participant.userId === userId && participant.roles.some((role) => role === "requester" || role === "approver"),
  );
}

/** A Chat is either bound to a Requirement id, definitely unbound, or its
 *  binding is unknown and must be probed through the legacy chat-scoped query. */
type ChatRequirementLink = string | null | undefined;

/**
 * Legacy compatibility adapter, deliberately confined to this hook.
 *
 * A current server projects the additive `requirement_id` on every loaded
 * ChatSession, so a `null` binding means "definitely unbound" and an ordinary
 * Chat issues no Requirement request at all. Only two shapes still need the
 * chat-scoped fallback:
 * - a loaded ChatSession from an old backend omits the projection (`undefined`);
 * - an Issue surface knows only the source chat session id (`origin_id`).
 * Both are indistinguishable from the server's side, so the hook probes
 * `GET /requirements?chat_session_id=`.
 */
function resolveChatRequirementLink(
  chatSession: ChatSession | null | undefined,
  chatSessionId: string | null | undefined,
): ChatRequirementLink {
  if (chatSession !== undefined) {
    // A row proves the binding one way or the other. `null` = no session
    // selected or the session list has not settled: never probe.
    return chatSession ? chatSession.requirement_id : null;
  }
  if (!chatSessionId) return null;
  // The caller knows a chat session id but has no ChatSession payload (the
  // Issue surface's `origin_id` fallback on an old backend): probe.
  return undefined;
}

/**
 * Requirement state for one Chat or Issue surface.
 *
 * `requirement_id` is a tri-state version signal:
 * - string    → fetch the Requirement detail by id and apply its compose/lock
 *               rules (read-only after issue/cancel, project locked).
 * - null      → current server, definitely unbound. Issue no Requirement
 *               request at all: this is what removes the old
 *               `GET /requirements?chat_session_id=` probe from ordinary Chats
 *               and stops its loading/error from disabling the composer.
 * - undefined → projection missing. Either an old backend omitted it on a
 *               loaded ChatSession, or the caller only knows a ChatSession id
 *               (the Issue surface projecting `origin_id`). Keep the legacy
 *               chat-scoped probe for that pre-release/rolling-upgrade window:
 *               a server that shipped Requirements but does not emit the field
 *               can own a bound, read-only Chat, so it must be probed instead
 *               of being treated as unbound. The server remains the final write
 *               boundary, but the UI must not open the composer itself.
 *               Delete this branch and `requirementByChatOptions` once no
 *               supported server version is older than the projection (P2).
 */
export function useRequirementSurface({
  wsId,
  requirementId,
  chatSessionId,
  chatSession,
  enabled = true,
}: {
  wsId: string;
  /** Explicit Requirement id (deep link or `Issue.requirement_id`); wins over
   *  the ChatSession projection. `null` means definitely unbound. */
  requirementId?: string | null;
  /** Chat session id: the active Chat, or the Issue's `origin_id` fallback. */
  chatSessionId?: string | null;
  /**
   * The loaded ChatSession row for `chatSessionId`, passed by Chat surfaces as
   * `currentSession`. `null` means no row is loaded yet (no session selected
   * or the list is still settling): stay closed without probing. Omit it in
   * surfaces that only know a chat session id; the hook then keeps the legacy
   * probe.
   */
  chatSession?: ChatSession | null;
  enabled?: boolean;
}): RequirementSurfaceState {
  const userId = useAuthStore((state) => state.user?.id);
  const link: ChatRequirementLink =
    requirementId !== undefined
      ? requirementId || null
      : resolveChatRequirementLink(chatSession, chatSessionId);

  const byId = useQuery({
    ...requirementDetailOptions(wsId, typeof link === "string" ? link : ""),
    enabled: enabled && !!wsId && typeof link === "string",
  });
  // Legacy compatibility path only: reached for a missing projection (old
  // server, or a caller without any ChatSession payload). Never enabled for a
  // current server's ordinary Chat (link === null).
  const probeEnabled = enabled && !!wsId && !!chatSessionId && link === undefined;
  const byChat = useQuery({
    ...requirementByChatOptions(wsId, chatSessionId ?? ""),
    enabled: probeEnabled,
  });

  const query = typeof link === "string" ? byId : probeEnabled ? byChat : null;
  const detail = query?.data ?? null;
  const error = query?.error instanceof Error ? query.error : null;
  const isLoading = enabled && (query?.isLoading ?? false);

  let canCompose: boolean;
  if (!enabled) {
    canCompose = false;
  } else if (detail) {
    canCompose = requirementCanCompose(detail, userId);
  } else if (!query) {
    // No session or a definite null projection is editable. A selected session
    // whose row has not loaded yet is still waiting for its projection, so stay
    // closed without creating a Requirement probe.
    canCompose = !chatSessionId || chatSession !== null;
  } else {
    // Bound or legacy-probed Chat: stay read-only until the detail proves
    // otherwise. A failed read is not proof, so it keeps the composer closed.
    canCompose = !isLoading && !error;
  }

  return {
    detail,
    error,
    isLoading,
    canCompose,
    projectLocked: !!detail,
  };
}

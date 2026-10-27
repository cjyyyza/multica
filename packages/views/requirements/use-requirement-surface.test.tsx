// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import {
  RequirementDetailSchema,
  type RequirementDetail,
} from "@multica/core/requirements";
import type { ChatSession } from "@multica/core/types";
import { useRequirementSurface } from "./use-requirement-surface";

const WS_ID = "ws-1";
const CHAT_ID = "chat-1";

const auth = vi.hoisted(() => ({ user: { id: "user-1" } as { id: string } | null }));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: Object.assign(
    (selector: (state: typeof auth) => unknown) => selector(auth),
    { getState: () => auth },
  ),
}));

beforeEach(() => {
  auth.user = { id: "user-1" };
});

function makeChatSession(overrides: Partial<ChatSession> = {}): ChatSession {
  return {
    id: CHAT_ID,
    workspace_id: WS_ID,
    agent_id: "agent-1",
    creator_id: "user-1",
    title: "Chat",
    status: "active",
    has_unread: false,
    requirement_id: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

function makeDetail(overrides: Record<string, unknown> = {}): RequirementDetail {
  return RequirementDetailSchema.parse({
    id: "req-1",
    workspaceId: WS_ID,
    requesterId: "user-1",
    revision: 3,
    state: "clarifying",
    intakeState: "clarifying",
    chatSessionId: CHAT_ID,
    ...overrides,
  });
}

interface MockApi {
  getRequirement: ReturnType<typeof vi.fn>;
  getRequirementByChat: ReturnType<typeof vi.fn>;
}

function setApi(overrides: Partial<MockApi> = {}): MockApi {
  const api: MockApi = {
    getRequirement: vi.fn(),
    getRequirementByChat: vi.fn(),
    ...overrides,
  };
  setApiInstance(api as unknown as ApiClient);
  return api;
}

function makeClient(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

function renderSurface(
  qc: QueryClient,
  args: Parameters<typeof useRequirementSurface>[0],
) {
  function wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  }
  return renderHook(
    (currentArgs: Parameters<typeof useRequirementSurface>[0]) => useRequirementSurface(currentArgs),
    { wrapper, initialProps: args },
  );
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("useRequirementSurface ChatSession requirement link", () => {
  it.each(["viewer", "unknown"])("keeps a %s participant read-only", async (role) => {
    const detail = makeDetail({
      requesterId: "requester-1",
      participants: [{ id: "participant-1", userId: "user-1", roles: [role] }],
    });
    setApi({ getRequirement: vi.fn().mockResolvedValue(detail) });
    const { result } = renderSurface(makeClient(), {
      wsId: WS_ID, chatSessionId: CHAT_ID,
      chatSession: makeChatSession({ requirement_id: detail.id }),
    });
    await waitFor(() => expect(result.current.detail).toEqual(detail));
    expect(result.current.canCompose).toBe(false);
  });

  it("allows an approver to clarify a requirement", async () => {
    const detail = makeDetail({
      requesterId: "requester-1",
      participants: [{ id: "participant-1", userId: "user-1", roles: ["viewer", "approver"] }],
    });
    setApi({ getRequirement: vi.fn().mockResolvedValue(detail) });
    const { result } = renderSurface(makeClient(), {
      wsId: WS_ID, chatSessionId: CHAT_ID,
      chatSession: makeChatSession({ requirement_id: detail.id }),
    });
    await waitFor(() => expect(result.current.canCompose).toBe(true));
  });

  it("does not reuse another user's cached detail to grant input", async () => {
    auth.user = null;
    const detail = makeDetail();
    setApi({ getRequirement: vi.fn().mockResolvedValue(detail) });
    const { result } = renderSurface(makeClient(), {
      wsId: WS_ID, chatSessionId: CHAT_ID,
      chatSession: makeChatSession({ requirement_id: detail.id }),
    });
    await waitFor(() => expect(result.current.detail).toEqual(detail));
    expect(result.current.canCompose).toBe(false);
  });

  it("keeps a new Chat editable when no session is selected", () => {
    const api = setApi();
    const { result } = renderSurface(makeClient(), {
      wsId: WS_ID,
      chatSessionId: null,
      chatSession: null,
    });

    expect(result.current.canCompose).toBe(true);
    expect(api.getRequirementByChat).not.toHaveBeenCalled();
    expect(api.getRequirement).not.toHaveBeenCalled();
  });

  it("sends no Requirement request for a definitely unbound Chat (new server null)", async () => {
    const api = setApi();
    const { result } = renderSurface(makeClient(), {
      wsId: WS_ID,
      chatSessionId: CHAT_ID,
      chatSession: makeChatSession({ requirement_id: null }),
    });

    await waitFor(() => expect(result.current.canCompose).toBe(true));
    expect(result.current.detail).toBeNull();
    expect(result.current.isLoading).toBe(false);
    expect(result.current.projectLocked).toBe(false);
    // The whole point of the additive projection: ordinary Chats stop probing.
    expect(api.getRequirementByChat).not.toHaveBeenCalled();
    expect(api.getRequirement).not.toHaveBeenCalled();
  });

  it("fetches the Requirement by id for a bound Chat and keeps its readonly lock", async () => {
    const detail = makeDetail({ issueId: "issue-1", state: "in_review" });
    const api = setApi({ getRequirement: vi.fn().mockResolvedValue(detail) });
    const { result } = renderSurface(makeClient(), {
      wsId: WS_ID,
      chatSessionId: CHAT_ID,
      chatSession: makeChatSession({ requirement_id: "req-1" }),
    });

    await waitFor(() => expect(result.current.detail).toEqual(detail));
    expect(api.getRequirement).toHaveBeenCalledWith(WS_ID, "req-1");
    expect(api.getRequirementByChat).not.toHaveBeenCalled();
    expect(result.current.projectLocked).toBe(true);
    expect(result.current.canCompose).toBe(false);
  });

  it("keeps a bound Chat read-only when its Requirement detail fails", async () => {
    const api = setApi({
      getRequirement: vi.fn().mockRejectedValue(new Error("detail failed")),
    });
    const { result } = renderSurface(makeClient(), {
      wsId: WS_ID,
      chatSessionId: CHAT_ID,
      chatSession: makeChatSession({ requirement_id: "req-1" }),
    });

    await waitFor(() => expect(result.current.error?.message).toBe("detail failed"));
    expect(api.getRequirement).toHaveBeenCalledWith(WS_ID, "req-1");
    expect(api.getRequirementByChat).not.toHaveBeenCalled();
    expect(result.current.canCompose).toBe(false);
  });

  it("follows a bound Chat's detail after its session projection resolves", async () => {
    const detail = makeDetail();
    let resolveDetail: (value: RequirementDetail) => void = () => {};
    const getRequirement = vi.fn().mockReturnValue(
      new Promise<RequirementDetail>((resolve) => {
        resolveDetail = resolve;
      }),
    );
    const api = setApi({ getRequirement });
    let args: Parameters<typeof useRequirementSurface>[0] = {
      wsId: WS_ID,
      chatSessionId: CHAT_ID,
      chatSession: null,
    };
    const { result, rerender } = renderSurface(makeClient(), args);

    expect(result.current.canCompose).toBe(false);
    expect(getRequirement).not.toHaveBeenCalled();

    args = { ...args, chatSession: makeChatSession({ requirement_id: "req-1" }) };
    rerender(args);

    await waitFor(() => expect(getRequirement).toHaveBeenCalledWith(WS_ID, "req-1"));
    expect(result.current.canCompose).toBe(false);
    expect(api.getRequirementByChat).not.toHaveBeenCalled();

    resolveDetail(detail);
    await waitFor(() => expect(result.current.detail).toEqual(detail));
    expect(result.current.canCompose).toBe(true);
    expect(result.current.projectLocked).toBe(true);
  });

  it("keeps the legacy byChat probe when a loaded old-backend row omits the projection", async () => {
    const bound = makeDetail({ issueId: "issue-1", state: "done" });
    const api = setApi({
      getRequirementByChat: vi.fn().mockResolvedValue(bound),
    });
    const { result } = renderSurface(makeClient(), {
      wsId: WS_ID,
      chatSessionId: CHAT_ID,
      chatSession: makeChatSession({ requirement_id: undefined }),
    });

    await waitFor(() => expect(result.current.detail).toEqual(bound));
    expect(api.getRequirementByChat).toHaveBeenCalledWith(WS_ID, CHAT_ID);
    expect(api.getRequirement).not.toHaveBeenCalled();
    // Old servers that shipped Requirements can still own a read-only Chat, so
    // a missing projection must not be treated as unbound.
    expect(result.current.canCompose).toBe(false);
  });

  it("opens the composer when the legacy probe reports no binding", async () => {
    const api = setApi({
      getRequirementByChat: vi.fn().mockResolvedValue(null),
    });
    const { result } = renderSurface(makeClient(), {
      wsId: WS_ID,
      chatSessionId: CHAT_ID,
      chatSession: makeChatSession({ requirement_id: undefined }),
    });

    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(api.getRequirementByChat).toHaveBeenCalledWith(WS_ID, CHAT_ID);
    expect(result.current.canCompose).toBe(true);
    expect(result.current.detail).toBeNull();
  });

  it("keeps the legacy probe failure visible and read-only", async () => {
    const api = setApi({
      getRequirementByChat: vi.fn().mockRejectedValue(new Error("readback failed")),
    });
    const { result } = renderSurface(makeClient(), {
      wsId: WS_ID,
      chatSessionId: CHAT_ID,
      chatSession: makeChatSession({ requirement_id: undefined }),
    });

    await waitFor(() => expect(result.current.error?.message).toBe("readback failed"));
    expect(api.getRequirementByChat).toHaveBeenCalledWith(WS_ID, CHAT_ID);
    expect(result.current.canCompose).toBe(false);
  });

  it("keeps a selected Chat closed without probing until its projection arrives", async () => {
    const api = setApi();
    let args: Parameters<typeof useRequirementSurface>[0] = {
      wsId: WS_ID,
      chatSessionId: CHAT_ID,
      // The caller's session list is still loading: no row, so no binding can
      // be proven either way.
      chatSession: null,
    };
    const { result, rerender } = renderSurface(makeClient(), args);

    expect(result.current.canCompose).toBe(false);
    rerender(args);
    expect(api.getRequirementByChat).not.toHaveBeenCalled();
    expect(api.getRequirement).not.toHaveBeenCalled();

    args = { ...args, chatSession: makeChatSession({ requirement_id: null }) };
    rerender(args);

    await waitFor(() => expect(result.current.canCompose).toBe(true));
    expect(api.getRequirementByChat).not.toHaveBeenCalled();
    expect(api.getRequirement).not.toHaveBeenCalled();
  });

  it("probes from the Issue surface when only a chat session id is known (old backend)", async () => {
    // IssueDetail projects `origin_id` and passes it with no ChatSession row.
    // That is the only remaining cache-free legacy path.
    const detail = makeDetail({ id: "req-1", issueId: "issue-1", state: "done" });
    const api = setApi({
      getRequirementByChat: vi.fn().mockResolvedValue(detail),
    });
    const { result } = renderSurface(makeClient(), {
      wsId: WS_ID,
      chatSessionId: CHAT_ID,
    });

    await waitFor(() => expect(result.current.detail).toEqual(detail));
    expect(api.getRequirementByChat).toHaveBeenCalledWith(WS_ID, CHAT_ID);
    expect(api.getRequirement).not.toHaveBeenCalled();
    expect(result.current.canCompose).toBe(false);
  });

  it("does not probe when the Issue's requirement_id is a definite null", async () => {
    const api = setApi();
    const { result } = renderSurface(makeClient(), {
      wsId: WS_ID,
      requirementId: null,
      chatSessionId: CHAT_ID,
    });

    expect(result.current.detail).toBeNull();
    expect(api.getRequirementByChat).not.toHaveBeenCalled();
    expect(api.getRequirement).not.toHaveBeenCalled();
  });

  it("prefers an explicit requirement id over the ChatSession binding", async () => {
    const detail = makeDetail({ id: "req-deep" });
    const api = setApi({ getRequirement: vi.fn().mockResolvedValue(detail) });
    const { result } = renderSurface(makeClient(), {
      wsId: WS_ID,
      requirementId: "req-deep",
      chatSessionId: CHAT_ID,
      chatSession: makeChatSession({ requirement_id: "req-chat" }),
    });

    await waitFor(() => expect(result.current.detail).toEqual(detail));
    expect(api.getRequirement).toHaveBeenCalledWith(WS_ID, "req-deep");
    expect(api.getRequirementByChat).not.toHaveBeenCalled();
  });

  it("stays inert when disabled", () => {
    const api = setApi();
    const { result } = renderSurface(makeClient(), {
      wsId: WS_ID,
      chatSessionId: CHAT_ID,
      chatSession: makeChatSession({ requirement_id: "req-1" }),
      enabled: false,
    });

    expect(result.current.canCompose).toBe(false);
    expect(result.current.detail).toBeNull();
    expect(api.getRequirement).not.toHaveBeenCalled();
    expect(api.getRequirementByChat).not.toHaveBeenCalled();
  });
});

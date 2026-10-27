// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { chatKeys } from "@multica/core/chat/queries";
import {
  RequirementDetailSchema,
  type RequirementDetail,
} from "@multica/core/requirements";
import type { Agent, ChatSession } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { ChatWindow } from "./chat-window";

const chatState = {
  isOpen: true,
  activeSessionId: "chat-1",
  selectedAgentId: null,
  selectedProjectId: null,
  isExpanded: false,
  setOpen: vi.fn(),
  setActiveSession: vi.fn(),
  setSelectedAgentId: vi.fn(),
  setSelectedProjectId: vi.fn(),
};

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { user: { id: string } }) => unknown) => selector({ user: { id: "user-1" } }),
}));
vi.mock("@multica/core/chat", () => ({
  useChatStore: Object.assign(
    (selector: (state: typeof chatState) => unknown) => selector(chatState),
    { getState: () => chatState },
  ),
}));
vi.mock("@multica/core/agents", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/agents")>()),
  useAgentPresenceDetail: () => ({ availability: "online" }),
  useCustomizeConversationStartersHref: () => null,
  useWorkspaceAgentAvailability: () => "available",
}));
vi.mock("@multica/core/chat/mutations", () => ({
  useCreateChatSession: () => ({ mutateAsync: vi.fn() }),
  useMarkChatSessionRead: () => ({ mutate: vi.fn() }),
  useSetChatSessionArchived: () => ({ mutate: vi.fn() }),
  useSetChatSessionProject: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdateChatSession: () => ({ mutate: vi.fn() }),
}));
vi.mock("@multica/core/chat/use-quick-actions-pending-timeout", () => ({
  useQuickActionsPendingTimeout: () => undefined,
}));
vi.mock("./use-quick-actions-failure-toast", () => ({
  useQuickActionsFailureToast: () => undefined,
}));
vi.mock("./use-chat-draft-restore", () => ({
  useChatDraftRestore: () => ({
    restoreDraftRequest: null,
    enqueueLocalRestore: vi.fn(),
    handleRestoreDraftApplied: vi.fn(),
  }),
}));
vi.mock("./use-chat-task-actions", () => ({
  useChatTaskActions: () => ({
    cancelChatTask: vi.fn(),
    handleEditQueuedTask: vi.fn(),
    handleRemoveQueuedTask: vi.fn(),
    handleClearQueuedTasks: vi.fn(),
    handleSendQueuedTaskNow: vi.fn(),
  }),
}));
vi.mock("./use-chat-input-focus", () => ({
  useChatInputFocus: () => ({ focusRequest: 0, requestInputFocus: vi.fn() }),
}));
vi.mock("./use-chat-project-context-support", () => ({
  useChatProjectContextSupport: () => true,
}));
vi.mock("./use-chat-resize", () => ({
  useChatResize: () => ({
    renderWidth: 480,
    renderHeight: 640,
    isAtMax: false,
    boundsReady: true,
    isDragging: false,
    toggleExpand: vi.fn(),
    startDrag: vi.fn(),
  }),
}));
vi.mock("./use-visual-viewport-keyboard", () => ({
  useVisualViewportKeyboard: () => null,
}));
vi.mock("./use-chat-context-items", () => ({ useChatContextItems: () => [] }));
vi.mock("../../common/use-app-foreground", () => ({ useAppForeground: () => false }));
vi.mock("../../common/actor-avatar", () => ({ ActorAvatar: () => <span>avatar</span> }));
vi.mock("../../requirements", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../requirements")>()),
  RequirementFlowPanel: ({ surface }: { surface: { detail: RequirementDetail | null; error: Error | null; isLoading: boolean; canCompose: boolean } }) => (
    <div
      data-testid="requirement-panel"
      data-loading={surface.isLoading ? "true" : "false"}
      data-can-compose={surface.canCompose ? "true" : "false"}
    >
      {surface.error?.message ?? surface.detail?.id ?? ""}
    </div>
  ),
}));
vi.mock("./chat-window-messages", () => ({
  ChatWindowMessages: ({ quickActionsDisabled, children }: { quickActionsDisabled?: boolean; children: React.ReactNode }) => (
    <div data-testid="window-messages" data-quick-actions-disabled={quickActionsDisabled ? "true" : "false"}>{children}</div>
  ),
}));
vi.mock("./chat-input", () => ({
  ChatInput: ({ disabled, projectLocked, agentRuntimeRequired, agentAccessRevoked }: {
    disabled?: boolean;
    projectLocked?: boolean;
    agentRuntimeRequired?: boolean;
    agentAccessRevoked?: boolean;
  }) => (
    <div
      data-testid="window-input"
      data-disabled={disabled ? "true" : "false"}
      data-project-locked={projectLocked ? "true" : "false"}
      data-runtime-required={agentRuntimeRequired ? "true" : "false"}
      data-access-revoked={agentAccessRevoked ? "true" : "false"}
    />
  ),
}));
vi.mock("./chat-queue", () => ({ ChatQueue: () => null }));
vi.mock("./chat-empty-state", () => ({ EmptyState: () => null }));
vi.mock("./chat-resize-handles", () => ({ ChatResizeHandles: () => null }));
vi.mock("./offline-banner", () => ({ OfflineBanner: () => null }));
vi.mock("./no-agent-banner", () => ({ NoAgentBanner: () => null }));
vi.mock("./archived-agent-banner", () => ({ ArchivedAgentBanner: () => null }));
vi.mock("./agent-access-revoked-banner", () => ({ AgentAccessRevokedBanner: () => null }));
vi.mock("./runtime-required-banner", () => ({ RuntimeRequiredBanner: () => null }));
vi.mock("motion/react", () => ({
  AnimatePresence: ({ children }: { children: React.ReactNode }) => children,
  motion: {
    div: ({ children, initial: _initial, animate: _animate, transition: _transition, ...props }: Record<string, unknown> & { children: React.ReactNode }) => <div {...props}>{children}</div>,
  },
}));

const agent: Agent = {
  id: "agent-1",
  workspace_id: "ws-1",
  runtime_id: "runtime-1",
  name: "Planner",
  description: "",
  instructions: "",
  avatar_url: null,
  runtime_mode: "local",
  runtime_config: {},
  custom_args: [],
  visibility: "workspace",
  permission_mode: "public_to",
  invocation_targets: [{ target_type: "workspace", target_id: null }],
  status: "idle",
  max_concurrent_tasks: 1,
  model: "",
  owner_id: "user-2",
  skills: [],
  created_at: "2026-05-28T00:00:00Z",
  updated_at: "2026-05-28T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

/** A ChatSession as a current (Requirement-aware) server projects it. */
function makeSession(overrides: Partial<ChatSession> = {}): ChatSession {
  return {
    id: "chat-1",
    workspace_id: "ws-1",
    agent_id: "agent-1",
    creator_id: "user-1",
    title: "Requirement",
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
    workspaceId: "ws-1",
    requesterId: "user-1",
    revision: 4,
    state: "done",
    intakeState: "awaiting_approval",
    chatSessionId: "chat-1",
    issueId: "issue-1",
    ...overrides,
  });
}

interface RenderOptions {
  session?: ChatSession;
  getRequirement?: ReturnType<typeof vi.fn>;
  getRequirementByChat?: ReturnType<typeof vi.fn>;
  listChatSessions?: ReturnType<typeof vi.fn>;
}

function renderWindow(options: RenderOptions = {}) {
  const activeSession = options.session ?? makeSession();
  const listChatSessions =
    options.listChatSessions ?? vi.fn().mockResolvedValue([activeSession]);
  setApiInstance({
    getRequirement: options.getRequirement ?? vi.fn(),
    getRequirementByChat: options.getRequirementByChat ?? vi.fn().mockResolvedValue(null),
    listAgents: vi.fn().mockResolvedValue([agent]),
    listMembers: vi.fn().mockResolvedValue([{ user_id: "user-1", role: "member" }]),
    listProjects: vi.fn().mockResolvedValue({ projects: [], total: 0 }),
    listChatSessions,
    getPendingChatTask: vi.fn().mockResolvedValue({}),
    listPendingChatTasks: vi.fn().mockResolvedValue({ tasks: [] }),
  } as unknown as ApiClient);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = renderWithI18n(
    <QueryClientProvider client={qc}><ChatWindow /></QueryClientProvider>,
  );
  return { qc, listChatSessions, view };
}

beforeEach(() => {
  vi.clearAllMocks();
  chatState.activeSessionId = "chat-1";
});

describe("ChatWindow Requirement surface", () => {
  it("issues no Requirement request for an ordinary Chat on a current server", async () => {
    const getRequirement = vi.fn();
    const getRequirementByChat = vi.fn().mockResolvedValue(null);
    renderWindow({
      session: makeSession({ requirement_id: null }),
      getRequirement,
      getRequirementByChat,
    });

    await waitFor(() =>
      expect(screen.getByTestId("requirement-panel")).toHaveAttribute("data-can-compose", "true"),
    );
    expect(screen.getByTestId("requirement-panel").textContent).toBe("");
    await waitFor(() =>
      expect(screen.getByTestId("window-input")).toHaveAttribute("data-disabled", "false"),
    );
    expect(screen.getByTestId("window-input")).toHaveAttribute("data-project-locked", "false");
    expect(screen.getByTestId("window-messages")).toHaveAttribute("data-quick-actions-disabled", "false");
    // The additive projection (null) replaced the old chat-scoped probe.
    expect(getRequirementByChat).not.toHaveBeenCalled();
    expect(getRequirement).not.toHaveBeenCalled();
  });

  it("fetches a bound Requirement by id and closes the composer", async () => {
    const detail = makeDetail();
    const getRequirement = vi.fn().mockResolvedValue(detail);
    const getRequirementByChat = vi.fn();
    renderWindow({
      session: makeSession({ requirement_id: "req-1" }),
      getRequirement,
      getRequirementByChat,
    });

    expect(await screen.findByTestId("requirement-panel")).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByTestId("requirement-panel")).toHaveTextContent("req-1"),
    );
    expect(getRequirement).toHaveBeenCalledWith("ws-1", "req-1");
    expect(getRequirementByChat).not.toHaveBeenCalled();
    expect(screen.getByTestId("window-input")).toHaveAttribute("data-disabled", "true");
    expect(screen.getByTestId("window-input")).toHaveAttribute("data-project-locked", "true");
    expect(screen.getByTestId("window-messages")).toHaveAttribute("data-quick-actions-disabled", "true");
  });

  it("keeps the legacy chat-scoped probe for a server without the projection", async () => {
    const detail = makeDetail();
    const getRequirement = vi.fn();
    const getRequirementByChat = vi.fn().mockResolvedValue(detail);
    renderWindow({
      // Old server: the key is absent, which is the version signal to probe.
      session: makeSession({ requirement_id: undefined }),
      getRequirement,
      getRequirementByChat,
    });

    await waitFor(() => expect(getRequirementByChat).toHaveBeenCalledWith("ws-1", "chat-1"));
    expect(getRequirement).not.toHaveBeenCalled();
    expect(await screen.findByTestId("requirement-panel")).toHaveTextContent("req-1");
    expect(screen.getByTestId("window-input")).toHaveAttribute("data-disabled", "true");
    expect(screen.getByTestId("window-input")).toHaveAttribute("data-project-locked", "true");
  });

  it("keeps an ordinary Chat editable when the legacy probe finds no binding", async () => {
    const getRequirementByChat = vi.fn().mockResolvedValue(null);
    renderWindow({
      session: makeSession({ requirement_id: undefined }),
      getRequirementByChat,
    });

    await waitFor(() => expect(getRequirementByChat).toHaveBeenCalledWith("ws-1", "chat-1"));
    await waitFor(() => expect(screen.getByTestId("requirement-panel").textContent).toBe(""));
    expect(screen.getByTestId("window-input")).toHaveAttribute("data-disabled", "false");
    expect(screen.getByTestId("window-messages")).toHaveAttribute("data-quick-actions-disabled", "false");
  });

  it("keeps the composer closed without probing while the session list is loading", async () => {
    let resolveSessions: (sessions: ChatSession[]) => void = () => {};
    const listChatSessions = vi.fn().mockReturnValue(
      new Promise<ChatSession[]>((resolve) => {
        resolveSessions = resolve;
      }),
    );
    const getRequirementByChat = vi.fn();
    renderWindow({ session: makeSession(), listChatSessions, getRequirementByChat });

    await waitFor(() => expect(listChatSessions).toHaveBeenCalled());
    // A still-loading list is not evidence either way — ordinary Chats must not
    // probe while it settles, and a bound Chat must not become editable.
    await act(async () => {});
    expect(getRequirementByChat).not.toHaveBeenCalled();
    expect(screen.getByTestId("window-input")).toHaveAttribute("data-disabled", "true");
    expect(screen.getByTestId("window-messages")).toHaveAttribute("data-quick-actions-disabled", "true");

    // Settled without the row: the id is dangling, not probeable.
    await act(async () => {
      resolveSessions([]);
    });
    await waitFor(() => expect(chatState.setActiveSession).toHaveBeenCalledWith(null));
    expect(getRequirementByChat).not.toHaveBeenCalled();
  });

  it("does not lock an ordinary Chat after a failed session list and reconnect", async () => {
    const getRequirementByChat = vi.fn();
    const listChatSessions = vi.fn().mockRejectedValueOnce(new Error("offline"));
    const { qc } = renderWindow({
      session: makeSession({ requirement_id: null }),
      listChatSessions,
      getRequirementByChat,
    });

    // A failed list leaves no projection to follow: stay closed without
    // probing, then follow the row once the retry resolves.
    await waitFor(() => expect(listChatSessions).toHaveBeenCalled());
    await act(async () => {});
    expect(getRequirementByChat).not.toHaveBeenCalled();
    expect(screen.getByTestId("window-input")).toHaveAttribute("data-disabled", "true");

    listChatSessions.mockResolvedValue([makeSession({ requirement_id: null })]);
    await qc.refetchQueries({ queryKey: chatKeys.sessions("ws-1") });
    await waitFor(() =>
      expect(screen.getByTestId("requirement-panel")).toHaveAttribute("data-can-compose", "true"),
    );
    expect(screen.getByTestId("window-input")).toHaveAttribute("data-disabled", "false");
    expect(screen.getByTestId("window-messages")).toHaveAttribute("data-quick-actions-disabled", "false");
    expect(getRequirementByChat).not.toHaveBeenCalled();
  });

  it("self-heals a session missing from the settled list without probing", async () => {
    // The server's session list includes Requirement participants now, so a
    // missing row is dangling: no cached Requirement or probe keeps it alive.
    const getRequirementByChat = vi.fn();
    renderWindow({
      listChatSessions: vi.fn().mockResolvedValue([makeSession({ id: "chat-other" })]),
      getRequirementByChat,
    });

    await waitFor(() => expect(chatState.setActiveSession).toHaveBeenCalledWith(null));
    expect(getRequirementByChat).not.toHaveBeenCalled();
  });

  it("still self-heals an ordinary dangling session target", async () => {
    // No cached Requirement binding: the original self-heal must keep clearing
    // stale session pointers (deleted / never existed / lost access).
    const getRequirementByChat = vi.fn().mockResolvedValue(null);
    renderWindow({
      listChatSessions: vi.fn().mockResolvedValue([makeSession({ id: "chat-other" })]),
      getRequirementByChat,
    });

    await waitFor(() => expect(chatState.setActiveSession).toHaveBeenCalledWith(null));
  });
});

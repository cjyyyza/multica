// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ApiError, setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { RequirementDetailSchema, type RequirementDetail } from "@multica/core/requirements";
import type { Agent } from "@multica/core/types";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import { renderWithI18n } from "../../test/i18n";
import { RequirementIntakeDialog, RequirementIntakeForm } from "./requirement-intake-dialog";

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { user: { id: string } }) => unknown) =>
    selector({ user: { id: "user-1" } }),
}));
vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/paths")>();
  return {
    ...actual,
    useWorkspacePaths: () => actual.paths.workspace("acme"),
  };
});
vi.mock("../../issues/components/yixiezuo-import", () => ({
  YixiezuoSourcePreview: ({ snapshot }: { snapshot: { title: string } }) => <div>source-preview: {snapshot.title}</div>,
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

const snapshot = {
  source: { host: "yixiezuo", id: "42", url: "https://source.example/42" },
  title: "Real source",
  description: "Actual preview",
  status: "open",
  priority: "medium",
  updated_at: "2026-01-01T00:00:00Z",
  lock_version: 3,
  digest: "sha256:source",
  attachments: [],
  comments: [],
  statuses: [],
  warnings: [],
  fields: [],
};

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

function renderForm(api: Record<string, unknown>) {
  setApiInstance(api as unknown as ApiClient);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const push = vi.fn();
  const navigation: NavigationAdapter = {
    push,
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/chat",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (path) => path,
  };
  const view = renderWithI18n(
    <QueryClientProvider client={qc}>
      <NavigationProvider value={navigation}>
        <RequirementIntakeForm />
      </NavigationProvider>
    </QueryClientProvider>,
  );
  return { ...view, push };
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("RequirementIntakeForm", () => {
  it("creates only from a reliable preview and navigates only after server confirmation", async () => {
    const createResponse = deferred<RequirementDetail>();
    const createRequirement = vi.fn().mockReturnValue(createResponse.promise);
    const { push } = renderForm({
      listAgents: vi.fn().mockResolvedValue([agent]),
      listMembers: vi.fn().mockResolvedValue([{ user_id: "user-1", role: "member" }]),
      listProjects: vi.fn().mockResolvedValue({ projects: [], total: 0 }),
      previewYixiezuoIssue: vi.fn().mockResolvedValue({ id: "operation-1", kind: "preview", state: "pending", snapshot: null, error: "" }),
      getYixiezuoOperation: vi.fn().mockResolvedValue({
        id: "operation-1",
        kind: "preview",
        state: "succeeded",
        snapshot,
        error: "",
      }),
      createRequirement,
    });
    expect(createRequirement).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Use an existing source link" }));
    fireEvent.change(await screen.findByLabelText("Source URL"), { target: { value: snapshot.source.url } });
    fireEvent.click(screen.getByRole("button", { name: "Preview source" }));
    fireEvent.click(await screen.findByRole("button", { name: "Create requirement" }));

    await waitFor(() => expect(createRequirement).toHaveBeenCalledWith("ws-1", expect.objectContaining({
      sourceHost: "yixiezuo",
      externalId: "42",
      sourceDigest: "sha256:source",
      sourceLockVersion: 3,
      agentId: "agent-1",
      intakeOperationId: "operation-1",
      idempotencyKey: expect.any(String),
    })));
    expect(push).not.toHaveBeenCalled();

    const detail = RequirementDetailSchema.parse({
      id: "req-1",
      workspaceId: "ws-1",
      requesterId: "user-1",
    });
    createResponse.resolve(detail);
    await waitFor(() => expect(push).toHaveBeenCalledWith("/acme/chat?requirement=req-1"));
  });

  it("does not offer creation without a source snapshot", async () => {
    const createRequirement = vi.fn();
    renderForm({
      listAgents: vi.fn().mockResolvedValue([agent]),
      listMembers: vi.fn().mockResolvedValue([{ user_id: "user-1", role: "member" }]),
      listProjects: vi.fn().mockResolvedValue({ projects: [], total: 0 }),
      previewYixiezuoIssue: vi.fn().mockResolvedValue({ id: "operation-1", kind: "preview", state: "pending", snapshot: null, error: "" }),
      getYixiezuoOperation: vi.fn().mockResolvedValue({ id: "operation-1", kind: "preview", state: "succeeded", snapshot: null, error: "" }),
      createRequirement,
    });

    fireEvent.click(screen.getByRole("button", { name: "Use an existing source link" }));
    fireEvent.change(await screen.findByLabelText("Source URL"), { target: { value: snapshot.source.url } });
    fireEvent.click(screen.getByRole("button", { name: "Preview source" }));

    await waitFor(() => expect(screen.queryByRole("button", { name: "Create requirement" })).not.toBeInTheDocument());
    expect(createRequirement).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "Preview source" })).toBeInTheDocument();
  });

  it("navigates to an existing requirement only when the conflict includes a readable id", async () => {
    const createRequirement = vi.fn().mockRejectedValue(new ApiError(
      "a requirement already exists for this source",
      409,
      "Conflict",
      { code: "requirement_source_already_exists", existing_requirement_id: "req-existing" },
    ));
    const { push } = renderForm({
      listAgents: vi.fn().mockResolvedValue([agent]),
      listMembers: vi.fn().mockResolvedValue([{ user_id: "user-1", role: "member" }]),
      listProjects: vi.fn().mockResolvedValue({ projects: [], total: 0 }),
      previewYixiezuoIssue: vi.fn().mockResolvedValue({ id: "operation-1", kind: "preview", state: "pending", snapshot: null, error: "" }),
      getYixiezuoOperation: vi.fn().mockResolvedValue({ id: "operation-1", kind: "preview", state: "succeeded", snapshot, error: "" }),
      createRequirement,
    });

    fireEvent.click(screen.getByRole("button", { name: "Use an existing source link" }));
    fireEvent.change(await screen.findByLabelText("Source URL"), { target: { value: snapshot.source.url } });
    fireEvent.click(screen.getByRole("button", { name: "Preview source" }));
    fireEvent.click(await screen.findByRole("button", { name: "Create requirement" }));

    await waitFor(() => expect(push).toHaveBeenCalledWith("/acme/chat?requirement=req-existing"));
  });

  it("does not navigate or leak an id when the source conflict has no readable id", async () => {
    const createRequirement = vi.fn().mockRejectedValue(new ApiError(
      "a requirement already exists for this source",
      409,
      "Conflict",
      { code: "requirement_source_already_exists" },
    ));
    const { push } = renderForm({
      listAgents: vi.fn().mockResolvedValue([agent]),
      listMembers: vi.fn().mockResolvedValue([{ user_id: "user-1", role: "member" }]),
      listProjects: vi.fn().mockResolvedValue({ projects: [], total: 0 }),
      previewYixiezuoIssue: vi.fn().mockResolvedValue({ id: "operation-1", kind: "preview", state: "pending", snapshot: null, error: "" }),
      getYixiezuoOperation: vi.fn().mockResolvedValue({ id: "operation-1", kind: "preview", state: "succeeded", snapshot, error: "" }),
      createRequirement,
    });

    fireEvent.click(screen.getByRole("button", { name: "Use an existing source link" }));
    fireEvent.change(await screen.findByLabelText("Source URL"), { target: { value: snapshot.source.url } });
    fireEvent.click(screen.getByRole("button", { name: "Preview source" }));
    fireEvent.click(await screen.findByRole("button", { name: "Create requirement" }));

    expect(await screen.findByText("a requirement already exists for this source")).toBeInTheDocument();
    expect(push).not.toHaveBeenCalled();
  });

  it("disables new create and preview after the server reports feature off", async () => {
    const createRequirement = vi.fn().mockRejectedValue(new ApiError(
      "requirement flow is disabled",
      403,
      "Forbidden",
      { code: "requirement_flow_disabled" },
    ));
    const { push } = renderForm({
      listAgents: vi.fn().mockResolvedValue([agent]),
      listMembers: vi.fn().mockResolvedValue([{ user_id: "user-1", role: "member" }]),
      listProjects: vi.fn().mockResolvedValue({ projects: [], total: 0 }),
      previewYixiezuoIssue: vi.fn().mockResolvedValue({ id: "operation-1", kind: "preview", state: "pending", snapshot: null, error: "" }),
      getYixiezuoOperation: vi.fn().mockResolvedValue({ id: "operation-1", kind: "preview", state: "succeeded", snapshot, error: "" }),
      createRequirement,
    });

    fireEvent.click(screen.getByRole("button", { name: "Use an existing source link" }));
    fireEvent.change(await screen.findByLabelText("Source URL"), { target: { value: snapshot.source.url } });
    fireEvent.click(screen.getByRole("button", { name: "Preview source" }));
    fireEvent.click(await screen.findByRole("button", { name: "Create requirement" }));

    expect(await screen.findByText("requirement flow is disabled")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create requirement" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Preview source" })).toBeDisabled();
    expect(push).not.toHaveBeenCalled();
  });

  it.each(["accepted", "queued"])("does not treat %s as external completion", async (state) => {
    const createRequirement = vi.fn();
    renderForm({
      listAgents: vi.fn().mockResolvedValue([agent]),
      listMembers: vi.fn().mockResolvedValue([{ user_id: "user-1", role: "member" }]),
      listProjects: vi.fn().mockResolvedValue({ projects: [], total: 0 }),
      previewYixiezuoIssue: vi.fn().mockResolvedValue({ id: "operation-1", kind: "preview", state: "pending", snapshot: null, error: "" }),
      getYixiezuoOperation: vi.fn().mockResolvedValue({ id: "operation-1", kind: "preview", state, snapshot, error: "" }),
      createRequirement,
    });

    fireEvent.click(screen.getByRole("button", { name: "Use an existing source link" }));
    fireEvent.change(await screen.findByLabelText("Source URL"), { target: { value: snapshot.source.url } });
    fireEvent.click(screen.getByRole("button", { name: "Preview source" }));

    await waitFor(() => expect(screen.queryByRole("button", { name: "Create requirement" })).not.toBeInTheDocument());
    expect(createRequirement).not.toHaveBeenCalled();
  });

  function ideaApi(overrides: Record<string, unknown> = {}) {
    return {
      listAgents: vi.fn().mockResolvedValue([agent]),
      listMembers: vi.fn().mockResolvedValue([{ user_id: "user-1", role: "member" }]),
      listProjects: vi.fn().mockResolvedValue({ projects: [], total: 0 }),
      createRequirement: vi.fn(),
      createRequirementDraft: vi.fn().mockResolvedValue({ id: "draft-1", kind: "draft", state: "pending", error: "" }),
      getYixiezuoOperation: vi.fn().mockResolvedValue({ id: "draft-1", kind: "draft", state: "pending", error: "" }),
      reconcileRequirementDraft: vi.fn(),
      ...overrides,
    };
  }

  async function submitIdea(value: string) {
    fireEvent.change(screen.getByLabelText("Idea"), { target: { value } });
    const submit = screen.getByRole("button", { name: "Create requirement" });
    await waitFor(() => expect(submit).toBeEnabled());
    fireEvent.click(submit);
  }

  it("starts from an idea and does not ask for a source URL", async () => {
    renderForm(ideaApi());
    expect(screen.getByLabelText("Idea")).toBeInTheDocument();
    expect(screen.queryByLabelText("Source URL")).not.toBeInTheDocument();
  });

  it("queues a draft like /requirement and opens the chat only after the requirement exists", async () => {
    const createRequirement = vi.fn();
    const createRequirementDraft = vi.fn().mockResolvedValue({ id: "draft-1", kind: "draft", state: "pending", error: "" });
    const { push } = renderForm(ideaApi({
      createRequirement,
      createRequirementDraft,
      getYixiezuoOperation: vi.fn().mockResolvedValue({
        id: "draft-1",
        kind: "draft",
        state: "succeeded",
        error: "",
        requirement_id: "req-1",
      }),
    }));

    const idea = `${"登".repeat(121)}\n补充范围`;
    await submitIdea(idea);

    await waitFor(() => expect(createRequirementDraft).toHaveBeenCalledWith("ws-1", {
      title: "登".repeat(120),
      body: idea,
      agentId: "agent-1",
      idempotencyKey: expect.any(String),
    }));
    expect(createRequirementDraft.mock.calls[0]?.[1]).not.toHaveProperty("templateUrl");
    expect(createRequirement).not.toHaveBeenCalled();
    await waitFor(() => expect(push).toHaveBeenCalledWith("/acme/chat?requirement=req-1"));
    expect(createRequirementDraft).toHaveBeenCalledTimes(1);
  });

  it("does not navigate or create again while the draft result is unknown", async () => {
    const createRequirementDraft = vi.fn().mockResolvedValue({ id: "draft-1", kind: "draft", state: "pending", error: "" });
    const reconcileRequirementDraft = vi.fn().mockResolvedValue({ id: "preview-1", kind: "preview", state: "pending", error: "" });
    const { push } = renderForm(ideaApi({
      createRequirementDraft,
      reconcileRequirementDraft,
      getYixiezuoOperation: vi.fn().mockResolvedValue({ id: "draft-1", kind: "draft", state: "unknown", error: "" }),
    }));

    await submitIdea("Remember me");
    expect(await screen.findByText("The draft result is not confirmed. Do not create another one.")).toBeInTheDocument();
    expect(screen.getByText("[Multica draft draft-1]")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create requirement" })).toBeDisabled();
    expect(push).not.toHaveBeenCalled();
    expect(createRequirementDraft).toHaveBeenCalledTimes(1);

    fireEvent.change(screen.getByLabelText("New issue link"), { target: { value: "https://source.example/99" } });
    fireEvent.click(screen.getByRole("button", { name: "Check issue" }));
    await waitFor(() => expect(reconcileRequirementDraft).toHaveBeenCalledWith("ws-1", "draft-1", {
      url: "https://source.example/99",
      idempotencyKey: expect.any(String),
    }));
    expect(createRequirementDraft).toHaveBeenCalledTimes(1);
    expect(push).not.toHaveBeenCalled();
  });

  it("keeps creation locked when the queued draft cannot be read", async () => {
    const createRequirementDraft = vi.fn().mockResolvedValue({ id: "draft-1", kind: "draft", state: "pending", error: "" });
    const getYixiezuoOperation = vi.fn().mockRejectedValue(new ApiError(
      "operation not found",
      404,
      "Not Found",
      { error: "operation not found" },
    ));
    const { push } = renderForm(ideaApi({ createRequirementDraft, getYixiezuoOperation }));

    await submitIdea("Remember me");
    expect(await screen.findByText("[Multica draft draft-1]")).toBeInTheDocument();
    expect(screen.getByText("operation not found")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create requirement" })).toBeDisabled();
    expect(push).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Check again" }));
    await waitFor(() => expect(getYixiezuoOperation).toHaveBeenCalledTimes(2));
    expect(createRequirementDraft).toHaveBeenCalledTimes(1);
  });

  it("retries a recorded failure with a new key and ignores a second click while submitting", async () => {
    const first = deferred<{ id: string; kind: string; state: string; error: string }>();
    const createRequirementDraft = vi.fn()
      .mockReturnValueOnce(first.promise)
      .mockResolvedValue({ id: "draft-2", kind: "draft", state: "pending", error: "" });
    renderForm(ideaApi({
      createRequirementDraft,
      getYixiezuoOperation: vi.fn().mockImplementation((_ws: string, id: string) => Promise.resolve({
        id,
        kind: "draft",
        state: id === "draft-1" ? "failed" : "pending",
        error: id === "draft-1" ? "missing field" : "",
      })),
    }));

    fireEvent.change(screen.getByLabelText("Idea"), { target: { value: "Remember me" } });
    const submit = screen.getByRole("button", { name: "Create requirement" });
    await waitFor(() => expect(submit).toBeEnabled());
    fireEvent.click(submit);
    await waitFor(() => expect(createRequirementDraft).toHaveBeenCalledTimes(1));
    fireEvent.click(submit);
    expect(createRequirementDraft).toHaveBeenCalledTimes(1);
    first.resolve({ id: "draft-1", kind: "draft", state: "pending", error: "" });

    expect(await screen.findByText("missing field")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "Create requirement" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "Create requirement" }));
    await waitFor(() => expect(createRequirementDraft).toHaveBeenCalledTimes(2));
    const firstKey = createRequirementDraft.mock.calls[0]?.[1].idempotencyKey;
    const secondKey = createRequirementDraft.mock.calls[1]?.[1].idempotencyKey;
    expect(firstKey).toEqual(expect.any(String));
    expect(secondKey).not.toBe(firstKey);
  });
});

describe("RequirementIntakeDialog", () => {
  it("opens on the idea and keeps the sidebar action a full-width row", async () => {
    setApiInstance({
      listAgents: vi.fn().mockResolvedValue([agent]),
      listMembers: vi.fn().mockResolvedValue([{ user_id: "user-1", role: "member" }]),
      listProjects: vi.fn().mockResolvedValue({ projects: [], total: 0 }),
    } as unknown as ApiClient);
    const navigation: NavigationAdapter = {
      push: vi.fn(),
      replace: vi.fn(),
      back: vi.fn(),
      pathname: "/acme/chat",
      searchParams: new URLSearchParams(),
      hash: "",
      getShareableUrl: (path) => path,
    };
    renderWithI18n(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <NavigationProvider value={navigation}>
          <RequirementIntakeDialog />
        </NavigationProvider>
      </QueryClientProvider>,
    );

    const trigger = screen.getByRole("button", { name: "New requirement" });
    expect(trigger.className).toContain("w-full");
    expect(trigger.className).toContain("justify-start");
    expect(trigger.className).not.toContain("border-border");
    fireEvent.click(trigger);

    const dialog = await screen.findByRole("dialog", { name: "New requirement" });
    const idea = screen.getByLabelText("Idea");
    expect(idea).toHaveAccessibleDescription("Creates a 易协作 draft, then opens a requirement Chat to clarify it.");
    expect(dialog).toContainElement(idea);
    expect(screen.queryByLabelText("Source URL")).not.toBeInTheDocument();
  });

  it("retries a thrown submit with the same key after the dialog closes", async () => {
    const createRequirementDraft = vi.fn().mockRejectedValue(new Error("Failed to fetch"));
    setApiInstance({
      listAgents: vi.fn().mockResolvedValue([agent]),
      listMembers: vi.fn().mockResolvedValue([{ user_id: "user-1", role: "member" }]),
      listProjects: vi.fn().mockResolvedValue({ projects: [], total: 0 }),
      createRequirementDraft,
      getYixiezuoOperation: vi.fn(),
    } as unknown as ApiClient);
    const navigation: NavigationAdapter = {
      push: vi.fn(),
      replace: vi.fn(),
      back: vi.fn(),
      pathname: "/acme/chat",
      searchParams: new URLSearchParams(),
      hash: "",
      getShareableUrl: (path) => path,
    };
    renderWithI18n(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <NavigationProvider value={navigation}>
          <RequirementIntakeDialog />
        </NavigationProvider>
      </QueryClientProvider>,
    );

    fireEvent.click(screen.getByRole("button", { name: "New requirement" }));
    fireEvent.change(await screen.findByLabelText("Idea"), { target: { value: "Remember me" } });
    const create = screen.getByRole("button", { name: "Create requirement" });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create);
    expect(await screen.findByRole("button", { name: "Retry" })).toBeInTheDocument();
    expect(screen.getByLabelText("Idea")).toHaveValue("Remember me");
    expect(screen.getByLabelText("Idea")).toBeDisabled();
    expect(screen.getByText(/Discarding it and writing a new idea may create a duplicate/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    fireEvent.click(screen.getByRole("button", { name: "New requirement" }));
    expect(screen.getByLabelText("Idea")).toHaveValue("Remember me");
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(createRequirementDraft).toHaveBeenCalledTimes(2));
    expect(createRequirementDraft.mock.calls[1]?.[1].idempotencyKey).toBe(
      createRequirementDraft.mock.calls[0]?.[1].idempotencyKey,
    );
    expect(navigation.push).not.toHaveBeenCalled();
  });
});

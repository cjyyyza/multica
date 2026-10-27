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
import { RequirementIntakeForm } from "./requirement-intake-dialog";

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

    fireEvent.change(screen.getByLabelText("Source URL"), { target: { value: snapshot.source.url } });
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

    fireEvent.change(screen.getByLabelText("Source URL"), { target: { value: snapshot.source.url } });
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

    fireEvent.change(screen.getByLabelText("Source URL"), { target: { value: snapshot.source.url } });
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

    fireEvent.change(screen.getByLabelText("Source URL"), { target: { value: snapshot.source.url } });
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

    fireEvent.change(screen.getByLabelText("Source URL"), { target: { value: snapshot.source.url } });
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

    fireEvent.change(screen.getByLabelText("Source URL"), { target: { value: snapshot.source.url } });
    fireEvent.click(screen.getByRole("button", { name: "Preview source" }));

    await waitFor(() => expect(screen.queryByRole("button", { name: "Create requirement" })).not.toBeInTheDocument());
    expect(createRequirement).not.toHaveBeenCalled();
  });
});

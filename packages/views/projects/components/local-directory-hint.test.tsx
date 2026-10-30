import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import type { Agent, ProjectResource } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enProjects from "../../locales/en/projects.json";

const TEST_RESOURCES = { en: { common: enCommon, projects: enProjects } };

// The viewer a fixture belongs to unless its test says otherwise. `mockAuth`
// below owns the live id; the constant is what `beforeEach` resets it to.
const VIEWER_ID = "user-viewer";

const mockDaemonStatus = vi.hoisted(() => ({
  daemonId: null as string | null,
  deviceName: null as string | null,
  running: false,
}));

vi.mock("../../platform", () => ({
  useLocalDaemonStatus: () => mockDaemonStatus,
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

const mockAuth = vi.hoisted(() => ({ userId: "user-viewer" as string | null }));

vi.mock("@multica/core/auth", () => {
  const getState = () => ({
    user: mockAuth.userId ? { id: mockAuth.userId } : null,
  });
  const useAuthStore = Object.assign(
    (selector?: (state: ReturnType<typeof getState>) => unknown) =>
      selector ? selector(getState()) : getState(),
    { getState },
  );
  return { useAuthStore };
});

const mockListResources = vi.hoisted(() => vi.fn());
const mockListRuntimes = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", () => ({
  api: {
    listProjectResources: (...args: unknown[]) => mockListResources(...args),
    listRuntimes: (...args: unknown[]) => mockListRuntimes(...args),
  },
}));

import { LocalDirectoryHint } from "./local-directory-hint";

function hintElement(
  projectId: string | null | undefined,
  assignedAgent: Pick<Agent, "runtime_id" | "runtime_bound"> | null | undefined,
  queryClient: QueryClient,
) {
  return (
    <QueryClientProvider client={queryClient}>
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <LocalDirectoryHint projectId={projectId} assignedAgent={assignedAgent} />
      </I18nProvider>
    </QueryClientProvider>
  );
}

function renderHint(
  projectId: string | null | undefined,
  assignedAgent?: Pick<Agent, "runtime_id" | "runtime_bound"> | null,
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const result = render(hintElement(projectId, assignedAgent, queryClient));
  return {
    ...result,
    queryClient,
    // Re-render the same tree against the same client so a changed
    // authenticated user sees the already-cached resource list.
    rerenderHint: () =>
      result.rerender(hintElement(projectId, assignedAgent, queryClient)),
  };
}

function makeLocalDirectoryResource(overrides: {
  daemon_id: string;
  local_path: string;
  label?: string;
  execution_mode?: string;
  created_by?: string | null;
}): ProjectResource {
  return {
    id: `res-${overrides.local_path}`,
    project_id: "proj-1",
    workspace_id: "ws-1",
    resource_type: "local_directory",
    resource_ref: {
      daemon_id: overrides.daemon_id,
      local_path: overrides.local_path,
      ...(overrides.label ? { label: overrides.label } : {}),
      ...(overrides.execution_mode
        ? { execution_mode: overrides.execution_mode }
        : {}),
    },
    label: null,
    position: 0,
    created_at: new Date(0).toISOString(),
    created_by: overrides.created_by === undefined ? VIEWER_ID : overrides.created_by,
  };
}

describe("LocalDirectoryHint", () => {
  beforeEach(() => {
    mockAuth.userId = VIEWER_ID;
    mockDaemonStatus.daemonId = null;
    mockDaemonStatus.deviceName = null;
    mockDaemonStatus.running = false;
    mockListResources.mockReset();
    mockListRuntimes.mockReset().mockResolvedValue([]);
  });

  it("shows the assigned agent's directory instead of the viewer's directory (#8861)", async () => {
    mockDaemonStatus.daemonId = "daemon-A";
    mockListRuntimes.mockResolvedValue([{ id: "runtime-vue", daemon_id: "daemon-B" }]);
    mockListResources.mockResolvedValue({
      resources: [
        makeLocalDirectoryResource({ daemon_id: "daemon-A", local_path: "/repos/laravel", label: "Laravel" }),
        makeLocalDirectoryResource({ daemon_id: "daemon-B", local_path: "/repos/vue", label: "VueJS", execution_mode: "worktree" }),
      ],
      total: 2,
    });

    renderHint("proj-1", { runtime_id: "runtime-vue" });

    expect(await screen.findByText("VueJS")).toBeInTheDocument();
    expect(screen.getByText(/\/repos\/vue/)).toBeInTheDocument();
    expect(screen.getByText(/isolated worktree of/i)).toBeInTheDocument();
    expect(screen.queryByText("Laravel")).not.toBeInTheDocument();
  });

  it.each([
    { name: "agent is missing", agent: null, runtimes: [] },
    { name: "runtime is hidden or missing", agent: { runtime_id: "runtime-vue" }, runtimes: [] },
  ])("does not substitute the viewer's directory when $name", async ({ agent, runtimes }) => {
    mockDaemonStatus.daemonId = "daemon-A";
    mockListRuntimes.mockResolvedValue(runtimes);
    mockListResources.mockResolvedValue({
      resources: [makeLocalDirectoryResource({ daemon_id: "daemon-A", local_path: "/repos/laravel", label: "Laravel" })],
      total: 1,
    });
    const { container, queryClient } = renderHint("proj-1", agent);
    await waitFor(() => expect(queryClient.isFetching()).toBe(0));
    expect(container).toBeEmptyDOMElement();
  });

  it("renders nothing when project_id is null", () => {
    const { container } = renderHint(null);
    expect(container.firstChild).toBeNull();
  });

  it("renders nothing when there's no local daemon", () => {
    mockListResources.mockResolvedValue({
      resources: [
        makeLocalDirectoryResource({
          daemon_id: "daemon-A",
          local_path: "/Users/foo/work",
          label: "work",
        }),
      ],
      total: 1,
    });
    const { container } = renderHint("proj-1");
    expect(container.firstChild).toBeNull();
  });

  it("renders the hint when a local_directory resource matches this daemon", async () => {
    mockDaemonStatus.daemonId = "daemon-A";
    mockDaemonStatus.running = true;
    mockListResources.mockResolvedValue({
      resources: [
        makeLocalDirectoryResource({
          daemon_id: "daemon-A",
          local_path: "/Users/foo/work",
          label: "work",
        }),
      ],
      total: 1,
    });
    renderHint("proj-1");
    await waitFor(() => {
      expect(screen.getByText("work")).toBeInTheDocument();
    });
    expect(screen.getByText(/Users\/foo\/work/)).toBeInTheDocument();
  });

  // The banner used to say "in-place" for every local_directory resource.
  // Under worktree mode that is simply false — the agent runs in an isolated
  // worktree and the named directory does not change — and it points the user
  // at the wrong place to look for the work.
  it("describes worktree mode as isolated, never in-place", async () => {
    mockDaemonStatus.daemonId = "daemon-A";
    mockDaemonStatus.running = true;
    mockListResources.mockResolvedValue({
      resources: [
        makeLocalDirectoryResource({
          daemon_id: "daemon-A",
          local_path: "/Users/foo/work",
          label: "work",
          execution_mode: "worktree",
        }),
      ],
      total: 1,
    });
    renderHint("proj-1");
    await waitFor(() => {
      expect(screen.getByText(/isolated worktree of/i)).toBeInTheDocument();
    });
    expect(screen.queryByText(/in-place/i)).not.toBeInTheDocument();
    // The mode's whole point is that results arrive as a branch; a user who
    // only reads this banner still has to know where to find them.
    expect(screen.getByText(/agent\/….*branch/i)).toBeInTheDocument();
    // The path stays: it identifies the repository the branch lands in.
    expect(screen.getByText(/Users\/foo\/work/)).toBeInTheDocument();
  });

  it("describes an explicit in_place resource as in-place", async () => {
    mockDaemonStatus.daemonId = "daemon-A";
    mockDaemonStatus.running = true;
    mockListResources.mockResolvedValue({
      resources: [
        makeLocalDirectoryResource({
          daemon_id: "daemon-A",
          local_path: "/Users/foo/work",
          label: "work",
          execution_mode: "in_place",
        }),
      ],
      total: 1,
    });
    renderHint("proj-1");
    await waitFor(() => {
      expect(screen.getByText(/in-place/i)).toBeInTheDocument();
    });
    expect(screen.queryByText(/isolated worktree/i)).not.toBeInTheDocument();
  });

  // Absent (pre-mode resources) and anything a newer server might send both
  // mean "assume the working copy is at stake" — claiming isolation we cannot
  // verify is the one wrong answer here.
  it("treats an absent or unknown mode as in-place", async () => {
    mockDaemonStatus.daemonId = "daemon-A";
    mockDaemonStatus.running = true;
    mockListResources.mockResolvedValue({
      resources: [
        makeLocalDirectoryResource({
          daemon_id: "daemon-A",
          local_path: "/Users/foo/legacy",
          label: "legacy",
        }),
        makeLocalDirectoryResource({
          daemon_id: "daemon-A",
          local_path: "/Users/foo/future",
          label: "future",
          execution_mode: "snapshot",
        }),
      ],
      total: 2,
    });
    renderHint("proj-1");
    await waitFor(() => {
      expect(screen.getAllByText(/in-place/i)).toHaveLength(2);
    });
    expect(screen.queryByText(/isolated worktree/i)).not.toBeInTheDocument();
  });

  it("ignores other daemons when there is no agent assignee", async () => {
    mockDaemonStatus.daemonId = "daemon-A";
    mockDaemonStatus.running = true;
    mockListResources.mockResolvedValue({
      resources: [
        makeLocalDirectoryResource({
          daemon_id: "daemon-B",
          local_path: "/Users/foo/other-machine",
          label: "elsewhere",
        }),
      ],
      total: 1,
    });
    const { container, queryClient } = renderHint("proj-1");
    await waitFor(() => expect(queryClient.isFetching()).toBe(0));
    expect(container.querySelector("div[class*='rounded-md']")).toBeNull();
  });

  // local_directory bindings are private to their creator. The API already
  // filters by owner; this check is defense in depth against a list cached
  // for another account.
  it("hides a matching directory created by someone else", async () => {
    mockDaemonStatus.daemonId = "daemon-A";
    mockDaemonStatus.running = true;
    mockListResources.mockResolvedValue({
      resources: [
        makeLocalDirectoryResource({ daemon_id: "daemon-A", local_path: "/Users/foo/mine", label: "mine" }),
        makeLocalDirectoryResource({
          daemon_id: "daemon-A",
          local_path: "/Users/foo/theirs",
          label: "theirs",
          created_by: "user-other",
        }),
        makeLocalDirectoryResource({
          daemon_id: "daemon-A",
          local_path: "/Users/foo/legacy-owner",
          label: "legacy-owner",
          created_by: null,
        }),
      ],
      total: 3,
    });
    const { container } = renderHint("proj-1");
    expect(await screen.findByText("mine")).toBeInTheDocument();
    expect(container.textContent).not.toMatch(/theirs|legacy-owner/);
    expect(container.textContent).not.toMatch(/Users\/foo\/(theirs|legacy-owner)/);
  });

  it("keeps the cached list private when the authenticated user changes", async () => {
    mockDaemonStatus.daemonId = "daemon-A";
    mockDaemonStatus.running = true;
    mockListResources.mockResolvedValue({
      resources: [
        makeLocalDirectoryResource({ daemon_id: "daemon-A", local_path: "/repos/u1", label: "u1-repo", created_by: "user-1" }),
        makeLocalDirectoryResource({ daemon_id: "daemon-A", local_path: "/repos/u2", label: "u2-repo", created_by: "user-2" }),
      ],
      total: 2,
    });

    mockAuth.userId = "user-1";
    const { container, queryClient, rerenderHint } = renderHint("proj-1");
    expect(await screen.findByText("u1-repo")).toBeInTheDocument();
    expect(container.textContent).not.toMatch(/u2-repo|\/repos\/u2/);

    // Switching accounts reads the same cached response; the client-side
    // filter must not surface the previous viewer's directory.
    mockAuth.userId = "user-2";
    rerenderHint();
    expect(await screen.findByText("u2-repo")).toBeInTheDocument();
    expect(container.textContent).not.toMatch(/u1-repo|\/repos\/u1/);
    expect(mockListResources).toHaveBeenCalledTimes(1);
    expect(queryClient.isFetching()).toBe(0);

    // A signed-out viewer sees nothing, not the last user's directory.
    mockAuth.userId = null;
    rerenderHint();
    await waitFor(() => expect(container).toBeEmptyDOMElement());
  });
});

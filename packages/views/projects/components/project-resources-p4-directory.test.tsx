// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { toast } from "sonner";
import { renderWithI18n } from "../../test/i18n";

const h = vi.hoisted(() => ({
  resources: [] as Array<Record<string, unknown>>,
  running: true,
  loading: false,
  workspaceDepots: [] as Array<Record<string, unknown>>,
  createMock: vi.fn(),
  updateMock: vi.fn(),
  pickDirectory: vi.fn(),
  validateLocalDirectory: vi.fn(),
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { user: { id: string } }) => unknown) =>
    selector({ user: { id: "u1" } }),
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (options: { queryKey?: unknown[] }) => {
    const key = options?.queryKey?.[0];
    if (key === "project-resources") return { data: h.loading ? undefined : h.resources };
    return { data: [] };
  },
  queryOptions: (options: unknown) => options,
}));

vi.mock("@multica/core/projects", () => ({
  projectResourcesOptions: () => ({ queryKey: ["project-resources"], queryFn: vi.fn() }),
  useCreateProjectResource: () => ({ mutateAsync: h.createMock, isPending: false }),
  useUpdateProjectResource: () => ({ mutateAsync: h.updateMock }),
  useDeleteProjectResource: () => ({ mutateAsync: vi.fn() }),
}));

vi.mock("@multica/core/config", () => ({
  useConfigStore: (selector: (state: { localWorktreeSupported: boolean }) => unknown) =>
    selector({ localWorktreeSupported: true }),
}));
vi.mock("@multica/core/runtimes", () => ({
  runtimeListOptions: () => ({ queryKey: ["runtimes"], queryFn: vi.fn() }),
  runtimeAdvertisesLocalWorktree: () => true,
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "workspace-1", slug: "ws", repos: [], p4_depots: h.workspaceDepots }),
}));
vi.mock("../../platform/local-directory", () => ({
  isDesktopShell: () => true,
  pickDirectory: (...args: unknown[]) => h.pickDirectory(...args),
  validateLocalDirectory: (...args: unknown[]) => h.validateLocalDirectory(...args),
}));
vi.mock("../../platform/use-local-daemon-status", () => ({
  useLocalDaemonStatus: () => ({
    daemonId: "daemon-1",
    deviceName: "desk",
    get running() {
      return h.running;
    },
  }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

import { ProjectResourcesSection } from "./project-resources-section";

function submitDepot() {
  fireEvent.click(screen.getByRole("button", { name: /add resource/i }));
  fireEvent.change(screen.getByLabelText("perforce.example.com:1666"), {
    target: { value: "p4:1666" },
  });
  fireEvent.change(screen.getByLabelText("//depot/project"), {
    target: { value: "//depot/game" },
  });
  const form = screen.getByLabelText("perforce.example.com:1666").closest("form");
  if (!form) throw new Error("p4 form missing");
  fireEvent.click(within(form).getByRole("button", { name: /^add$/i }));
}

describe("ProjectResourcesSection — Perforce asks for a local directory first", () => {
  beforeEach(() => {
    h.resources = [];
    h.running = true;
    h.loading = false;
    h.workspaceDepots = [];
    h.createMock.mockReset().mockResolvedValue({});
    h.updateMock.mockReset().mockResolvedValue({});
    h.pickDirectory.mockReset();
    h.validateLocalDirectory.mockReset();
    vi.mocked(toast.error).mockClear();
    vi.mocked(toast.success).mockClear();
  });

  const depot = { id: "p4-1", resource_type: "perforce_depot", resource_ref: { port: "p4:1666", depot: "//depot/game" } };
  const directory = (owner = "u1", daemon = "daemon-1", mode = "in_place") => ({
    id: "dir-1", resource_type: "local_directory", created_by: owner,
    resource_ref: { local_path: "D:\\DJ", daemon_id: daemon, execution_mode: mode },
  });

  it.each([
    { directories: [] },
    { directories: [directory("u2")] },
    { directories: [directory("u1", "other-machine")] },
  ])("shows a red binding reminder without this user's directory on this machine ($directories)", ({ directories }) => {
    h.resources = [depot, ...directories];
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    expect(screen.getByRole("alert")).toHaveClass("text-destructive");
    expect(screen.getByRole("alert")).toHaveTextContent(/P4.*local.*directory|local.*P4.*workspace/i);
    expect(h.createMock).not.toHaveBeenCalled();
  });

  it("removes the binding reminder when this user's in-place binding arrives", () => {
    h.resources = [depot];
    const { rerender } = renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    expect(screen.getByRole("alert")).toBeInTheDocument();
    h.resources = [depot, directory()];
    rerender(<ProjectResourcesSection projectId="p1" />);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("shows the binding instruction in Chinese", () => {
    h.resources = [depot];
    renderWithI18n(<ProjectResourcesSection projectId="p1" />, { locale: "zh-Hans" });
    expect(screen.getByRole("alert")).toHaveTextContent("绑定已有的 P4 本地工作区目录");
  });

  it("explains an incompatible directory mode without silently changing it", () => {
    h.resources = [depot, directory("u1", "daemon-1", "worktree")];
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    expect(screen.getByRole("alert")).toHaveTextContent(/directly/i);
    expect(h.updateMock).not.toHaveBeenCalled();
  });

  it("shows the reminder for a workspace P4 fallback, after resources have loaded", () => {
    h.workspaceDepots = [{ port: "p4:1666", depot: "//depot/game" }];
    h.loading = true;
    const { rerender } = renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    h.loading = false;
    rerender(<ProjectResourcesSection projectId="p1" />);
    expect(screen.getByRole("alert")).toBeInTheDocument();
  });

  it.each([
    { resources: [{ id: "git", resource_type: "github_repo", resource_ref: { url: "https://github.com/example/game" } }] },
    { resources: [directory("u1", "daemon-1", "worktree")] },
    { resources: [directory("u1", "other-machine", "worktree")] },
  ])("does not mark an ordinary Git project unusable because the workspace has P4 ($resources)", ({ resources }) => {
    h.workspaceDepots = [{ port: "p4:1666", depot: "//depot/game" }];
    h.resources = resources;
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("picks the workspace folder and attaches it in place before the depot", async () => {
    h.pickDirectory.mockResolvedValue({ ok: true, path: "D:\\DJ", basename: "DJ" });
    h.validateLocalDirectory.mockResolvedValue({ ok: true, is_git_repo: false });

    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    submitDepot();

    await waitFor(() => expect(h.createMock).toHaveBeenCalledTimes(2));
    expect(h.createMock.mock.calls[0]?.[0]).toMatchObject({
      resource_type: "local_directory",
      resource_ref: {
        local_path: "D:\\DJ",
        daemon_id: "daemon-1",
        label: "DJ",
        execution_mode: "in_place",
      },
    });
    expect(h.createMock.mock.calls[1]?.[0]).toMatchObject({
      resource_type: "perforce_depot",
      resource_ref: { port: "p4:1666", depot: "//depot/game" },
    });
  });

  it("does not attach the depot when the directory picker is cancelled", async () => {
    h.pickDirectory.mockResolvedValue({ ok: false, reason: "cancelled" });

    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    submitDepot();

    await waitFor(() => expect(h.pickDirectory).toHaveBeenCalled());
    expect(h.createMock).not.toHaveBeenCalled();
    expect(
      (screen.getByLabelText("perforce.example.com:1666") as HTMLInputElement).value,
    ).toBe("p4:1666");
  });

  it("does not reuse another user's P4 directory on the same daemon", async () => {
    h.resources = [{
      id: "peer-dir", resource_type: "local_directory", created_by: "u2",
      resource_ref: { local_path: "D:\\Bob\\P4", daemon_id: "daemon-1", execution_mode: "in_place" },
    }];
    h.pickDirectory.mockResolvedValue({ ok: false, reason: "cancelled" });
    const { container } = renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    expect(container.innerHTML).not.toContain("Bob");
    submitDepot();

    await waitFor(() => expect(h.pickDirectory).toHaveBeenCalled());
    expect(h.createMock).not.toHaveBeenCalled();
  });

  it("preserves a parallel directory until its mode is explicitly changed", async () => {
    h.resources = [
      {
        id: "dir-1",
        project_id: "p1",
        workspace_id: "workspace-1",
        resource_type: "local_directory",
        resource_ref: {
          local_path: "D:\\DJ",
          daemon_id: "daemon-1",
          label: "DJ",
          execution_mode: "worktree",
        },
        position: 0,
        created_at: "2026-10-13T00:00:00Z",
        created_by: "u1",
      },
    ];

    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    submitDepot();

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        "Change this directory's execution mode to edit the folder directly before attaching a Perforce depot.",
      ),
    );
    expect(h.updateMock).not.toHaveBeenCalled();
    expect(h.createMock).not.toHaveBeenCalled();
    expect(h.pickDirectory).not.toHaveBeenCalled();
    expect(screen.getByLabelText("perforce.example.com:1666")).toHaveValue("p4:1666");
  });

  it("attaches the depot directly when this machine already has an in-place directory", async () => {
    h.resources = [
      {
        id: "dir-1",
        project_id: "p1",
        workspace_id: "workspace-1",
        resource_type: "local_directory",
        resource_ref: {
          local_path: "D:\\DJ",
          daemon_id: "daemon-1",
          label: "DJ",
          execution_mode: "in_place",
        },
        position: 0,
        created_at: "2026-10-13T00:00:00Z",
        created_by: "u1",
      },
    ];

    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    submitDepot();

    await waitFor(() => expect(h.createMock).toHaveBeenCalledTimes(1));
    expect(h.pickDirectory).not.toHaveBeenCalled();
    expect(h.createMock.mock.calls[0]?.[0].resource_type).toBe("perforce_depot");
  });

  it("refuses the depot when the desktop daemon is not running", async () => {
    h.running = false;

    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    submitDepot();

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        "Specify this machine's workspace folder before attaching a Perforce depot. The agent edits that folder and does not sync a new copy.",
      ),
    );
    expect(h.pickDirectory).not.toHaveBeenCalled();
    expect(h.createMock).not.toHaveBeenCalled();
  });

  it("keeps an existing directory and form unchanged when depot creation fails", async () => {
    const ref = { local_path: "D:\\DJ", daemon_id: "daemon-1", execution_mode: "in_place" };
    h.resources = [{ id: "dir-1", resource_type: "local_directory", created_by: "u1", resource_ref: ref }];
    h.createMock.mockRejectedValue(new Error("Invalid depot"));

    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    submitDepot();

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("Invalid depot"));
    expect(h.updateMock).not.toHaveBeenCalled();
    expect(h.resources[0]?.resource_ref).toEqual(ref);
    expect(screen.getByLabelText("//depot/project")).toHaveValue("//depot/game");
  });

  it("lets an existing Perforce project explicitly repair a parallel directory", async () => {
    h.resources = [
      { id: "dir-1", resource_type: "local_directory", created_by: "u1", resource_ref: { local_path: "D:\\DJ", daemon_id: "daemon-1", execution_mode: "worktree" } },
      { id: "p4-1", resource_type: "perforce_depot", resource_ref: { port: "p4:1666", depot: "//depot/game" } },
    ];
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    fireEvent.click(screen.getByRole("button", { name: "Change how runs use this folder" }));

    const dialog = screen.getByRole("dialog");
    expect(within(dialog).queryByText("Run in parallel, isolated")).toBeNull();
    expect(h.updateMock).not.toHaveBeenCalled();
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(h.updateMock).toHaveBeenCalledWith({
      resourceId: "dir-1",
      data: { resource_ref: { local_path: "D:\\DJ", daemon_id: "daemon-1", execution_mode: "in_place" } },
    }));
    expect(h.createMock).not.toHaveBeenCalled();
  });
});

// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { toast } from "sonner";
import { renderWithI18n } from "../../test/i18n";

const h = vi.hoisted(() => ({
  resources: [] as Array<Record<string, unknown>>,
  running: true,
  createMock: vi.fn(),
  updateMock: vi.fn(),
  pickDirectory: vi.fn(),
  validateLocalDirectory: vi.fn(),
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (options: { queryKey?: unknown[] }) => {
    const key = options?.queryKey?.[0];
    if (key === "project-resources") return { data: h.resources };
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
  useCurrentWorkspace: () => ({ id: "workspace-1", slug: "ws", repos: [] }),
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
    h.createMock.mockReset().mockResolvedValue({});
    h.updateMock.mockReset().mockResolvedValue({});
    h.pickDirectory.mockReset();
    h.validateLocalDirectory.mockReset();
    vi.mocked(toast.error).mockClear();
    vi.mocked(toast.success).mockClear();
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
    h.resources = [{ id: "dir-1", resource_type: "local_directory", resource_ref: ref }];
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
      { id: "dir-1", resource_type: "local_directory", resource_ref: { local_path: "D:\\DJ", daemon_id: "daemon-1", execution_mode: "worktree" } },
      { id: "p4-1", resource_type: "perforce_depot", resource_ref: { port: "p4:1666", depot: "//depot/game" } },
    ];
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    fireEvent.click(screen.getByTitle("Change how runs use this folder"));

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

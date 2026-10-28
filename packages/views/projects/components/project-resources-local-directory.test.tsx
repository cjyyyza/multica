// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { screen } from "@testing-library/react";
import { renderWithI18n } from "../../test/i18n";
import en from "../../locales/en/projects.json";

const RESOURCE = {
  id: "res-1",
  project_id: "p1",
  workspace_id: "workspace-1",
  resource_type: "local_directory",
  resource_ref: {
    local_path: "G:\\Game Projects\\DJ01",
    daemon_id: "daemon-1",
    label: "game-client",
    execution_mode: "worktree",
  },
  // Written by a label update (`multica project resource update --label`):
  // the column the row must read before the copy left inside the ref.
  label: "Game Client",
  position: 0,
  created_at: "2026-08-18T00:00:00Z",
  created_by: "u1",
};

const auth = vi.hoisted(() => ({ userId: "u1" as string | null }));
let resources: Array<Omit<typeof RESOURCE, "created_by"> & { created_by?: string | null }> = [RESOURCE];
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { user: { id: string } | null }) => unknown) =>
    selector({ user: auth.userId ? { id: auth.userId } : null }),
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (options: { queryKey?: unknown[] }) => {
    const key = options?.queryKey?.[0];
    if (key === "project-resources") return { data: resources };
    return { data: [] };
  },
  queryOptions: (options: unknown) => options,
}));

vi.mock("@multica/core/projects", () => ({
  projectResourcesOptions: () => ({ queryKey: ["project-resources"], queryFn: vi.fn() }),
  useCreateProjectResource: () => ({ mutateAsync: vi.fn() }),
  useUpdateProjectResource: () => ({ mutateAsync: vi.fn() }),
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
  pickDirectory: vi.fn(),
  validateLocalDirectory: vi.fn(),
}));
vi.mock("../../platform/use-local-daemon-status", () => ({
  useLocalDaemonStatus: () => ({ daemonId: "daemon-1", deviceName: "MacBook", running: true }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

import { ProjectResourcesSection } from "./project-resources-section";

const strings = en.resources;

describe("ProjectResourcesSection — local directory row", () => {
  beforeEach(() => {
    resources = [RESOURCE];
    auth.userId = "u1";
  });

  it("isolates local directories by user even on the same daemon", () => {
    resources = [RESOURCE, {
      ...RESOURCE, id: "peer", created_by: "u2", label: "/private/bob/p4",
      resource_ref: { ...RESOURCE.resource_ref, local_path: "/private/bob/p4" },
    }];
    const { container, rerender } = renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    expect(screen.getByText(RESOURCE.resource_ref.local_path)).toBeInTheDocument();
    expect(container.innerHTML).not.toContain("/private/bob/p4");

    // Reuse the same query result through an account switch: cached rows must
    // never display the previous user's directory.
    auth.userId = "u2";
    rerender(<ProjectResourcesSection projectId="p1" />);
    expect(screen.queryByText(RESOURCE.resource_ref.local_path)).not.toBeInTheDocument();
    expect(screen.getByText("/private/bob/p4")).toBeInTheDocument();
    expect(container.innerHTML).not.toContain(RESOURCE.resource_ref.local_path);
  });

  it("keeps this user's directory on another machine visible", () => {
    resources = [{ ...RESOURCE, resource_ref: { ...RESOURCE.resource_ref, daemon_id: "my-laptop" } }];
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    expect(screen.getByText(RESOURCE.resource_ref.local_path)).toBeInTheDocument();
  });

  it.each([null, undefined])("hides directories without an owner (%s)", (createdBy) => {
    resources = [{ ...RESOURCE, created_by: createdBy }];
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    expect(screen.queryByText(RESOURCE.resource_ref.local_path)).not.toBeInTheDocument();
  });

  it("hides private directories while the signed-in user is unknown", () => {
    auth.userId = null;
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    expect(screen.queryByText(RESOURCE.resource_ref.local_path)).not.toBeInTheDocument();
  });

  it("shows the complete Windows path instead of either saved label", () => {
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    expect(screen.getByText(RESOURCE.resource_ref.local_path)).toBeInTheDocument();
    expect(screen.queryByText("Game Client")).not.toBeInTheDocument();
    expect(screen.queryByText("game-client")).not.toBeInTheDocument();
  });

  // The row used to carry a pencil that turned the name into a text box. A
  // folder is identified by its path, so the only thing that "edit" changed
  // was the row's title, and beside the branch and remove controls it read as
  // a broken edit action (MUL-7525). What remains are the two settings that
  // change how tasks run on the folder.
  it("offers the execution mode and remove actions, and no rename", () => {
    renderWithI18n(<ProjectResourcesSection projectId="p1" />);
    expect(screen.getByRole("button", { name: strings.mode_edit_tooltip })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: strings.remove_tooltip })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /rename/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  });
});

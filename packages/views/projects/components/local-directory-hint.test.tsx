// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { renderWithI18n } from "../../test/i18n";
import { LocalDirectoryHint } from "./local-directory-hint";

const state = vi.hoisted(() => ({ userId: "u1" as string | null, resources: [] as unknown[] }));
vi.mock("@multica/core/auth", () => {
  const getState = () => ({ user: state.userId ? { id: state.userId } : null });
  return { useAuthStore: Object.assign((selector: (value: ReturnType<typeof getState>) => unknown) => selector(getState()), { getState }) };
});
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace" }));
vi.mock("@multica/core/projects", () => ({ projectResourcesOptions: () => ({ queryKey: ["resources"] }) }));
vi.mock("@tanstack/react-query", () => ({ useQuery: () => ({ data: state.resources }) }));
vi.mock("../../platform", () => ({ useLocalDaemonStatus: () => ({ daemonId: "daemon" }) }));

describe("LocalDirectoryHint ownership", () => {
  beforeEach(() => {
    state.userId = "u1";
    state.resources = ["u1", "u2", null].map((owner, index) => ({
      id: String(index), resource_type: "local_directory", created_by: owner,
      label: `private-label-${index}`, resource_ref: { daemon_id: "daemon", local_path: `/private/path-${index}` },
    }));
  });

  it("filters labels and paths again when the account changes with the same cached response", () => {
    const { rerender, container } = renderWithI18n(<LocalDirectoryHint projectId="project" />);
    expect(screen.getByText("private-label-0")).toBeInTheDocument();
    expect(container.textContent).not.toMatch(/private-label-[12]|path-[12]/);
    state.userId = "u2";
    rerender(<LocalDirectoryHint projectId="project" />);
    expect(screen.getByText("private-label-1")).toBeInTheDocument();
    expect(container.textContent).not.toMatch(/private-label-[02]|path-[02]/);
    state.userId = null;
    rerender(<LocalDirectoryHint projectId="project" />);
    expect(container).toBeEmptyDOMElement();
  });
});

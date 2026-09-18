import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithI18n } from "../../test/i18n";
import { parseStatusMapText, statusMapToText } from "./yixiezuo-tab";

const mockUpsert = vi.hoisted(() => vi.fn());
const mockDelete = vi.hoisted(() => vi.fn());
const mockInvalidate = vi.hoisted(() => vi.fn());
const mockToastSuccess = vi.hoisted(() => vi.fn());
const mockToastError = vi.hoisted(() => vi.fn());

const envelopeRef = vi.hoisted(() => ({
  current: {
    connection: null as null | {
      id: string;
      cli_bin: string;
      gcp_host: string;
      list_query_id: string;
      external_project_id: string;
      tracker_id: string;
      project_id: string | null;
      status_map: Record<string, string>;
      last_pulled_at: string | null;
      last_pushed_at: string | null;
    },
    can_manage: true,
  },
}));

const projectsRef = vi.hoisted(() => ({
  current: [{ id: "proj-1", title: "Board project" }],
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (opts: { queryKey: unknown[] }) => {
    const key = JSON.stringify(opts.queryKey);
    if (key.includes("yixiezuo")) {
      return { data: envelopeRef.current, isLoading: false };
    }
    if (key.includes("projects")) {
      return { data: projectsRef.current, isLoading: false };
    }
    return { data: undefined, isLoading: false };
  },
  useQueryClient: () => ({ invalidateQueries: mockInvalidate }),
  queryOptions: <T,>(opts: T) => opts,
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));

vi.mock("@multica/core/yixiezuo", () => ({
  yixiezuoConnectionOptions: () => ({
    queryKey: ["yixiezuo", "workspace-1", "connection"],
    queryFn: vi.fn(),
  }),
  yixiezuoKeys: {
    connection: (wsId: string) => ["yixiezuo", wsId, "connection"],
  },
}));

vi.mock("@multica/core/projects", () => ({
  projectListOptions: () => ({
    queryKey: ["projects", "workspace-1", "list"],
    queryFn: vi.fn(),
  }),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    upsertYixiezuoConnection: mockUpsert,
    deleteYixiezuoConnection: mockDelete,
  },
}));

vi.mock("sonner", () => ({
  toast: { success: mockToastSuccess, error: mockToastError },
}));

import { YixiezuoTab } from "./yixiezuo-tab";

describe("status map text", () => {
  it("round-trips configured pairs", () => {
    expect(parseStatusMapText("开发中=in_progress\n已解决=done")).toEqual({
      开发中: "in_progress",
      已解决: "done",
    });
    expect(statusMapToText({ 开发中: "in_progress" })).toBe("开发中=in_progress");
  });
});

describe("YixiezuoTab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockUpsert.mockResolvedValue({ connection: { id: "c1" }, can_manage: true });
    mockDelete.mockResolvedValue(undefined);
    envelopeRef.current = {
      connection: null,
      can_manage: true,
    };
  });

  it("does not save without a query_id", async () => {
    const user = userEvent.setup();
    renderWithI18n(<YixiezuoTab />);
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(mockUpsert).not.toHaveBeenCalled();
  });

  it("shows the local-only sync command and saves the connection", async () => {
    const user = userEvent.setup();
    renderWithI18n(<YixiezuoTab />);
    expect(screen.getByText("multica yixiezuo sync --watch")).toBeInTheDocument();
    expect(
      screen.getByText(/The server never calls 易协作|After saving, keep this running/),
    ).toBeInTheDocument();

    await user.clear(screen.getByLabelText("CLI"));
    await user.type(screen.getByLabelText("CLI"), "popo-cli");
    await user.type(screen.getByLabelText("易协作 host"), "dj01.pm.netease.com");
    await user.type(screen.getByLabelText("Saved filter ID"), "9");
    await user.type(screen.getByLabelText("Status map"), "开发中=in_progress");
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(mockUpsert).toHaveBeenCalledWith(
      "workspace-1",
      expect.objectContaining({
        cli_bin: "popo-cli",
        gcp_host: "dj01.pm.netease.com",
        list_query_id: "9",
        project_id: null,
        status_map: { 开发中: "in_progress" },
      }),
    );
  });

  it("hides save for members when nothing is connected", () => {
    envelopeRef.current.can_manage = false;
    renderWithI18n(<YixiezuoTab />);
    expect(screen.getByText("Ask a workspace admin to connect 易协作.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save" })).not.toBeInTheDocument();
  });

  it("disconnects an existing connection", async () => {
    envelopeRef.current.connection = {
      id: "c1",
      cli_bin: "popo-cli",
      gcp_host: "dj01.pm.netease.com",
      list_query_id: "9",
      external_project_id: "7",
      tracker_id: "34",
      project_id: null,
      status_map: {},
      last_pulled_at: null,
      last_pushed_at: null,
    };
    const user = userEvent.setup();
    renderWithI18n(<YixiezuoTab />);
    await user.click(screen.getByRole("button", { name: "Disconnect" }));
    const dialog = screen.getByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Disconnect" }));
    expect(mockDelete).toHaveBeenCalledWith("workspace-1");
  });
});

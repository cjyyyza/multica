import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithI18n } from "../../test/i18n";
import { YixiezuoImportForm } from "./yixiezuo-import";
const mocks = vi.hoisted(() => ({ preview: vi.fn(), imported: vi.fn(), push: vi.fn(), snapshot: null as unknown, options: [] as unknown[] }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws" }));
vi.mock("@multica/core/paths", () => ({ useWorkspacePaths: () => ({ issueDetail: (id: string) => "/issues/" + id }) }));
vi.mock("../../navigation", () => ({ useNavigation: () => ({ push: mocks.push }) }));
vi.mock("@multica/ui/markdown", () => ({ Markdown: ({ children }: { children: string }) => <div>{children}</div> }));
vi.mock("@multica/core/api", () => ({ api: { previewYixiezuoIssue: (...args: unknown[]) => mocks.preview(...args), importYixiezuoIssue: (...args: unknown[]) => mocks.imported(...args) } }));
vi.mock("@tanstack/react-query", async (original) => ({
  ...await original<typeof import("@tanstack/react-query")>(),
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
  useQuery: (opts: { queryKey: unknown[] }) => opts.queryKey.includes("operation") ? { data: mocks.snapshot, error: null } : { data: [] },
  useMutation: (opts: { mutationFn: () => Promise<unknown>; onSuccess?: (value: unknown) => void }) => ({ mutate: async () => { const result = await opts.mutationFn(); await opts.onSuccess?.(result); }, isPending: false, error: null, reset: vi.fn() }),
}));

beforeEach(() => { vi.clearAllMocks(); mocks.snapshot = null; mocks.preview.mockResolvedValue({ id: "op" }); mocks.imported.mockResolvedValue({ issue: { id: "existing" }, existing: true }); });
describe("single source issue import", () => {
  it("queues only the entered source and does not import on preview", async () => {
    const user = userEvent.setup(); renderWithI18n(<YixiezuoImportForm />);
    await user.type(screen.getByLabelText("易协作 issue URL"), "https://dj01.pm.netease.com/issues/7");
    await user.click(screen.getByRole("button", { name: "Read preview" }));
    expect(mocks.preview).toHaveBeenCalledWith("ws", "https://dj01.pm.netease.com/issues/7");
    expect(mocks.imported).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "Confirm import" })).not.toBeInTheDocument();
  });
  it("opens the existing task after explicit confirmation", async () => {
    mocks.snapshot = { id: "op", state: "succeeded", snapshot: { source: { id: "7", url: "https://dj01.pm.netease.com/issues/7" }, title: "Source issue", status: "Ready", description: "Steps", attachments: [], comments: [], fields: [], warnings: [] } };
    const user = userEvent.setup(); renderWithI18n(<YixiezuoImportForm />);
    await user.type(screen.getByLabelText("易协作 issue URL"), "https://dj01.pm.netease.com/issues/7");
    await user.click(screen.getByRole("button", { name: "Read preview" }));
    await user.click(screen.getByRole("button", { name: "Confirm import" }));
    expect(mocks.imported).toHaveBeenCalledWith("ws", "op", null);
    expect(mocks.push).toHaveBeenCalledWith("/issues/existing");
  });
});

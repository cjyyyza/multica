import { beforeEach, expect, it, vi } from "vitest";
import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithI18n } from "../../test/i18n";
import { YixiezuoSource } from "./yixiezuo-source";

const mocks = vi.hoisted(() => ({ publish: vi.fn(), refresh: vi.fn(), upload: vi.fn(), state: "imported", revision: 3 }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws" }));
vi.mock("./yixiezuo-import", () => ({ YixiezuoSourcePreview: () => <div /> }));
vi.mock("@multica/core/api", () => ({ api: { publishYixiezuoResult: (...args: unknown[]) => mocks.publish(...args), refreshYixiezuoImport: (...args: unknown[]) => mocks.refresh(...args), uploadFile: (...args: unknown[]) => mocks.upload(...args) } }));
vi.mock("@tanstack/react-query", async (original) => ({
  ...await original<typeof import("@tanstack/react-query")>(),
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
  useQuery: () => ({ data: { import: { issue_id: "issue", revision: mocks.revision, state: mocks.state, snapshot: { source: { id: "7", url: "https://dj01.pm.netease.com/issues/7" }, status: "Ready", lock_version: 1, digest: "source-digest", statuses: [{ id: 2, name: "QA" }] } } } }),
  useMutation: (opts: { mutationFn: () => Promise<unknown>; onSuccess?: (value: unknown) => void }) => ({ mutate: async () => { const result = await opts.mutationFn(); await opts.onSuccess?.(result); }, isPending: false, error: null, reset: vi.fn() }),
}));

beforeEach(() => { vi.clearAllMocks(); mocks.state = "imported"; mocks.revision = 3; mocks.publish.mockResolvedValue({ id: "op" }); });

it("requires acceptance and sends the reviewed revision and source digest", async () => {
  const user = userEvent.setup();
  renderWithI18n(<YixiezuoSource issueId="issue" />);
  expect(mocks.publish).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "Publish result" }));
  await user.type(screen.getByLabelText("Fix, validation and acceptance result"), "Fixed and verified");
  expect(screen.getByRole("button", { name: "Confirm publication" })).toBeDisabled();
  await user.click(screen.getByRole("checkbox"));
  await user.click(screen.getByRole("button", { name: "Confirm publication" }));
  expect(mocks.publish).toHaveBeenCalledWith("ws", "issue", { summary: "Fixed and verified", status_name: "", revision: 3, confirmed: true, source_digest: "source-digest" });
});

it("blocks confirmation when a newer revision arrives during review", async () => {
  const user = userEvent.setup();
  const view = renderWithI18n(<YixiezuoSource issueId="issue" />);
  await user.click(screen.getByRole("button", { name: "Publish result" }));
  await user.type(screen.getByLabelText("Fix, validation and acceptance result"), "Verified");
  await user.click(screen.getByRole("checkbox"));
  mocks.revision = 4;
  view.rerender(<YixiezuoSource issueId="issue" />);
  expect(screen.getByRole("button", { name: "Confirm publication" })).toBeDisabled();
  expect(mocks.publish).not.toHaveBeenCalled();
});

it("requires a source refresh after an uncertain external result", () => {
  mocks.state = "unknown";
  renderWithI18n(<YixiezuoSource issueId="issue" />);
  expect(screen.getByRole("button", { name: "Publish result" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "Refresh source" })).toBeEnabled();
  expect(mocks.publish).not.toHaveBeenCalled();
});

function evidenceInput() {
  const input = document.body.querySelector('input[type="file"]');
  if (!input) throw new Error("evidence file input missing");
  return input;
}

function uploadedFile() {
  return {
    id: "att-1",
    filename: "shot.png",
    url: "https://cdn.example/shot.png",
    content_type: "image/png",
    size_bytes: 3,
    created_at: "2026-01-01T00:00:00.000Z",
  };
}

it("uploads evidence files with the comment upload API and sends their ids", async () => {
  const user = userEvent.setup();
  mocks.upload.mockResolvedValue(uploadedFile());
  renderWithI18n(<YixiezuoSource issueId="issue" />);
  await user.click(screen.getByRole("button", { name: "Publish result" }));
  expect(screen.getByText("Evidence files")).toBeInTheDocument();
  await user.type(screen.getByLabelText("Fix, validation and acceptance result"), "Fixed and verified");
  fireEvent.change(evidenceInput(), { target: { files: [new File(["png"], "shot.png", { type: "image/png" })] } });
  expect(await screen.findByText("shot.png")).toBeInTheDocument();
  expect(mocks.upload).toHaveBeenCalledWith(expect.any(File), { issueId: "issue", commentId: undefined, chatSessionId: undefined });
  await user.click(screen.getByRole("checkbox"));
  await user.click(screen.getByRole("button", { name: "Confirm publication" }));
  expect(mocks.publish).toHaveBeenCalledWith("ws", "issue", {
    summary: "Fixed and verified",
    status_name: "",
    revision: 3,
    confirmed: true,
    source_digest: "source-digest",
    attachment_ids: ["att-1"],
  });
});

it("keeps confirmation closed while an evidence file is still uploading", async () => {
  const user = userEvent.setup();
  let resolveUpload: (value: ReturnType<typeof uploadedFile>) => void = () => {};
  mocks.upload.mockReturnValue(new Promise((resolve) => { resolveUpload = resolve; }));
  renderWithI18n(<YixiezuoSource issueId="issue" />);
  await user.click(screen.getByRole("button", { name: "Publish result" }));
  await user.type(screen.getByLabelText("Fix, validation and acceptance result"), "Verified");
  fireEvent.change(evidenceInput(), { target: { files: [new File(["png"], "shot.png", { type: "image/png" })] } });
  await user.click(screen.getByRole("checkbox"));
  const confirm = screen.getByRole("button", { name: "Confirm publication" });
  await waitFor(() => expect(confirm).toBeDisabled());
  expect(confirm).toHaveAttribute("aria-busy", "true");
  expect(mocks.publish).not.toHaveBeenCalled();
  await act(async () => { resolveUpload(uploadedFile()); });
  expect(await screen.findByText("shot.png")).toBeInTheDocument();
  await waitFor(() => expect(confirm).toBeEnabled());
});

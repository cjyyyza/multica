import { beforeEach, expect, it, vi } from "vitest";
import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithI18n } from "../../test/i18n";
import { YixiezuoSource } from "./yixiezuo-source";

const mocks = vi.hoisted(() => ({ publish: vi.fn(), refresh: vi.fn(), upload: vi.fn(), state: "imported", revision: 3, changelists: [] as unknown[] }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws" }));
vi.mock("./yixiezuo-import", () => ({ YixiezuoSourcePreview: () => <div /> }));
vi.mock("@multica/core/api", () => ({ api: { publishYixiezuoResult: (...args: unknown[]) => mocks.publish(...args), refreshYixiezuoImport: (...args: unknown[]) => mocks.refresh(...args), uploadFile: (...args: unknown[]) => mocks.upload(...args) } }));
vi.mock("@tanstack/react-query", async (original) => ({
  ...await original<typeof import("@tanstack/react-query")>(),
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
  useQuery: (options: { queryKey?: unknown[] }) => options.queryKey?.[0] === "p4"
    ? ({ data: { changelists: mocks.changelists } })
    : ({ data: { import: { issue_id: "issue", revision: mocks.revision, state: mocks.state, snapshot: { source: { id: "7", url: "https://dj01.pm.netease.com/issues/7" }, status: "Ready", lock_version: 1, digest: "source-digest", statuses: [{ id: 2, name: "QA" }] } } } }),
  useMutation: (opts: { mutationFn: () => Promise<unknown>; onSuccess?: (value: unknown) => void }) => ({ mutate: async () => { const result = await opts.mutationFn(); await opts.onSuccess?.(result); }, isPending: false, error: null, reset: vi.fn() }),
}));

beforeEach(() => { vi.clearAllMocks(); mocks.state = "imported"; mocks.revision = 3; mocks.changelists = []; mocks.publish.mockResolvedValue({ id: "op" }); });

function changelist(overrides: Record<string, unknown>) {
  return {
    id: "cl-1", workspace_id: "ws", port: "p4:1666", number: 795095, original_number: 795095,
    status: "shelved", client: "dj-win", p4_user: "yujian", description: "refs #7", ticket: "7",
    files_count: 1, exclusive_files: [], warnings: [], swarm_review_id: null, swarm_review_number: null,
    swarm_review_url: null, swarm_review_state: null, task_id: null, link_source: "task",
    submitted_at: null, created_at: "", updated_at: "", ...overrides,
  };
}

it("prefills the issue's changelists into the result", async () => {
  const user = userEvent.setup();
  mocks.changelists = [changelist({ number: 795115, original_number: 795095, status: "submitted", swarm_review_number: 123 })];
  renderWithI18n(<YixiezuoSource issueId="issue" />);
  await user.click(screen.getByRole("button", { name: "Publish result" }));
  const result = screen.getByLabelText("Fix, validation and acceptance result");
  expect(result).toHaveValue("CL 795115 (Submitted, was 795095) · Swarm #123");
  await user.type(result, "\nFixed and verified");
  await user.click(screen.getByRole("checkbox"));
  await user.click(screen.getByRole("button", { name: "Confirm publication" }));
  expect(mocks.publish).toHaveBeenCalledWith("ws", "issue", expect.objectContaining({
    summary: "CL 795115 (Submitted, was 795095) · Swarm #123\nFixed and verified",
  }));
});

it("leaves the result empty for an issue with no changelists", async () => {
  const user = userEvent.setup();
  renderWithI18n(<YixiezuoSource issueId="issue" />);
  await user.click(screen.getByRole("button", { name: "Publish result" }));
  expect(screen.getByLabelText("Fix, validation and acceptance result")).toHaveValue("");
});

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

it("stages evidence without changing the reviewed issue revision and sends its ids", async () => {
  const user = userEvent.setup();
  mocks.upload.mockImplementation(async (_file: File, context?: { issueId?: string }) => {
    if (context?.issueId) mocks.revision += 1;
    return uploadedFile();
  });
  renderWithI18n(<YixiezuoSource issueId="issue" />);
  await user.click(screen.getByRole("button", { name: "Publish result" }));
  expect(screen.getByText("Evidence files")).toBeInTheDocument();
  await user.type(screen.getByLabelText("Fix, validation and acceptance result"), "Fixed and verified");
  fireEvent.change(evidenceInput(), { target: { files: [new File(["png"], "shot.png", { type: "image/png" })] } });
  expect(await screen.findByText("shot.png")).toBeInTheDocument();
  expect(mocks.upload).toHaveBeenCalledWith(expect.any(File), { issueId: undefined, commentId: undefined, chatSessionId: undefined });
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

it("keeps an external issue edit unconfirmed when an evidence upload completes", async () => {
  const user = userEvent.setup();
  mocks.upload.mockImplementation(async () => {
    mocks.revision += 1;
    return uploadedFile();
  });
  renderWithI18n(<YixiezuoSource issueId="issue" />);
  await user.click(screen.getByRole("button", { name: "Publish result" }));
  await user.type(screen.getByLabelText("Fix, validation and acceptance result"), "Verified");
  fireEvent.change(evidenceInput(), { target: { files: [new File(["png"], "shot.png", { type: "image/png" })] } });
  expect(await screen.findByText("shot.png")).toBeInTheDocument();
  await user.click(screen.getByRole("checkbox"));
  expect(screen.getByRole("button", { name: "Confirm publication" })).toBeDisabled();
  expect(mocks.publish).not.toHaveBeenCalled();
});

it("does not attach a pending upload to a different issue after navigation", async () => {
  const user = userEvent.setup();
  let finish: (value: ReturnType<typeof uploadedFile>) => void = () => {};
  mocks.upload.mockReturnValue(new Promise((resolve) => { finish = resolve; }));
  const view = renderWithI18n(<YixiezuoSource issueId="issue" />);
  await user.click(screen.getByRole("button", { name: "Publish result" }));
  fireEvent.change(evidenceInput(), { target: { files: [new File(["png"], "shot.png", { type: "image/png" })] } });
  view.rerender(<YixiezuoSource issueId="other-issue" />);
  await act(async () => { finish(uploadedFile()); });
  expect(screen.queryByText("shot.png")).not.toBeInTheDocument();
});

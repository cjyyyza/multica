// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactElement } from "react";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { IssueSchema } from "@multica/core/api/schemas";
import {
  DEFAULT_REQUIREMENT_PERMISSIONS,
  requirementKeys,
  RequirementDetailSchema,
  type RequirementDetail,
} from "@multica/core/requirements";
import { renderWithI18n } from "../../test/i18n";
import { RequirementCard } from "./requirement-card";
import { RequirementApprovalCard } from "./requirement-approval-card";
import { RequirementReviewCard } from "./requirement-review-card";
import { RequirementSyncStatus } from "./requirement-sync-status";
import { RequirementFlowPanel } from "./requirement-flow-panel";

vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/paths")>();
  return { ...actual, useWorkspacePaths: () => actual.paths.workspace("acme") };
});
vi.mock("../../navigation", () => ({
  AppLink: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a>,
  useNavigation: () => ({ push: vi.fn() }),
}));

const permissions = {
  ...DEFAULT_REQUIREMENT_PERMISSIONS,
  canRead: true,
  canOpen: true,
  canManageParticipants: true,
  canSubmitCandidate: true,
  canApprove: true,
  canAccept: true,
  canReject: true,
  canCancel: true,
  canReconcile: true,
};

const candidateSnapshot = {
  goal: "Ship v1",
  nonGoals: ["Out of scope"],
  scope: ["Requirement flow"],
  constraints: ["Keep one Issue"],
  acceptanceCriteria: ["Approve creates one Issue"],
  projectId: "project-1",
  agentId: "agent-1",
};

const issue = IssueSchema.parse({
  id: "issue-1",
  workspace_id: "ws-1",
  number: 1,
  identifier: "MUL-1",
  title: "Requirement issue",
  description: null,
  status: "in_review",
  priority: "medium",
  assignee_type: "agent",
  assignee_id: "agent-1",
  creator_type: "member",
  creator_id: "user-1",
  parent_issue_id: null,
  project_id: null,
  position: 0,
  stage: null,
  start_date: null,
  due_date: null,
  metadata: {},
  properties: {},
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
  revision: 9,
  origin_type: "requirement_chat",
  origin_id: "chat-1",
});

function makeDetail(overrides: Record<string, unknown> = {}): RequirementDetail {
  return RequirementDetailSchema.parse({
    id: "req-1",
    workspaceId: "ws-1",
    requesterId: "user-1",
    revision: 4,
    state: "awaiting_approval",
    intakeState: "awaiting_approval",
    chatSessionId: "chat-1",
    participants: [{ id: "participant-1", userId: "user-1", roles: ["requester", "approver"] }],
    latestCandidate: {
      id: "candidate-1",
      requirementId: "req-1",
      revision: 2,
      snapshot: candidateSnapshot,
      digest: "digest-1",
      createdByTaskId: "task-1",
    },
    permissions,
    ...overrides,
  });
}

function renderCard(ui: ReactElement, api: Record<string, unknown> = {}) {
  setApiInstance({
    listMembers: vi.fn().mockResolvedValue([
      { user_id: "user-1", name: "Ada", role: "member" },
      { user_id: "user-2", name: "Ben", role: "member" },
    ]),
    listProjects: vi.fn().mockResolvedValue({
      projects: [{
        id: "project-1",
        workspace_id: "ws-1",
        title: "Core project",
        description: null,
        icon: null,
        status: "active",
        priority: "medium",
        lead_type: null,
        lead_id: null,
        created_at: "2026-01-01T00:00:00Z",
        updated_at: "2026-01-01T00:00:00Z",
      }],
      total: 1,
    }),
    listAgents: vi.fn().mockResolvedValue([{ id: "agent-1", name: "Planner" }]),
    ...api,
  } as unknown as ApiClient);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = renderWithI18n(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>);
  return { ...view, qc };
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("RequirementApprovalCard", () => {
  it("approves the candidate reviewed by the user", async () => {
    const approveRequirement = vi.fn().mockResolvedValue({
      requirement: makeDetail(),
      issue,
    });
    renderCard(<RequirementApprovalCard wsId="ws-1" detail={makeDetail()} />, { approveRequirement });

    fireEvent.click(screen.getByRole("button", { name: "Review and approve" }));
    fireEvent.click(await screen.findByRole("button", { name: "Create Issue and start agent" }));
    await waitFor(() => expect(approveRequirement).toHaveBeenCalledWith("ws-1", "req-1", {
      expectedRevision: 4,
      candidateId: "candidate-1",
      candidateDigest: "digest-1",
      idempotencyKey: expect.any(String),
    }));
  });

  it("shows only selected source attachment names and types", async () => {
    const detail = makeDetail({
      sourceSnapshot: {
        source: { host: "dj01.pm.netease.com", id: "100", url: "https://dj01.pm.netease.com/issues/100" },
        title: "Source requirement",
        digest: "source-digest",
        attachments: [
          { id: "source-image", name: "design.png", url: "https://media.invalid/design.png", content_type: "image/png" },
          { id: "source-text", name: "notes.txt", url: "https://media.invalid/notes.txt", content_type: "text/plain" },
        ],
      },
      latestCandidate: {
        id: "candidate-attachments",
        requirementId: "req-1",
        revision: 2,
        snapshot: { ...candidateSnapshot, attachmentIds: ["source-image"] },
        digest: "digest-attachments",
        createdByTaskId: "task-1",
      },
    });
    renderCard(<RequirementApprovalCard wsId="ws-1" detail={detail} />);
    fireEvent.click(screen.getByRole("button", { name: "Review and approve" }));

    expect(await screen.findByText("design.png")).toBeInTheDocument();
    expect(screen.getByText("image/png")).toBeInTheDocument();
    expect(screen.getByText("Attachments")).toBeInTheDocument();
    expect(screen.queryByText("notes.txt")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create Issue and start agent" })).toBeEnabled();
  });

  it("fails closed when a selected attachment is absent from the source preview", async () => {
    const detail = makeDetail({
      sourceSnapshot: {
        source: { host: "dj01.pm.netease.com", id: "100", url: "https://dj01.pm.netease.com/issues/100" },
        title: "Source requirement",
        digest: "source-digest",
        attachments: [],
      },
      latestCandidate: {
        id: "candidate-unknown-attachment",
        requirementId: "req-1",
        revision: 2,
        snapshot: { ...candidateSnapshot, attachmentIds: ["missing-source-id"] },
        digest: "digest-unknown-attachment",
        createdByTaskId: "task-1",
      },
    });
    renderCard(<RequirementApprovalCard wsId="ws-1" detail={detail} />);
    fireEvent.click(screen.getByRole("button", { name: "Review and approve" }));

    expect(await screen.findByText("Unknown attachment (missing-source-id)")).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("Some selected attachments are not present in the source preview. Approval is unavailable.");
    expect(screen.getByRole("button", { name: "Create Issue and start agent" })).toBeDisabled();
  });

  it("blocks confirmation when candidate or revision changes during review", async () => {
    const approveRequirement = vi.fn();
    const initial = makeDetail();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    setApiInstance({ approveRequirement } as unknown as ApiClient);
    const view = renderWithI18n(
      <QueryClientProvider client={qc}><RequirementApprovalCard wsId="ws-1" detail={initial} /></QueryClientProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Review and approve" }));
    const changed = makeDetail({
      revision: 5,
      latestCandidate: {
        id: "candidate-2",
        requirementId: "req-1",
        revision: 3,
        snapshot: { ...candidateSnapshot, goal: "Ship v2" },
        digest: "digest-2",
        createdByTaskId: "task-2",
      },
    });
    view.rerender(
      <QueryClientProvider client={qc}><RequirementApprovalCard wsId="ws-1" detail={changed} /></QueryClientProvider>,
    );

    expect(await screen.findByText(/candidate or revision changed/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create Issue and start agent" })).toBeDisabled();
    expect(approveRequirement).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Review latest candidate" }));
    expect(screen.getByText(/Ship v2/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create Issue and start agent" })).toBeEnabled();
  });

  it("hides approval when the permission is absent", () => {
    const detail = makeDetail({ permissions: { ...permissions, canApprove: false } });
    renderCard(<RequirementApprovalCard wsId="ws-1" detail={detail} />);
    expect(screen.queryByRole("button", { name: "Review and approve" })).not.toBeInTheDocument();
  });

  it("renders every candidate field with accessible structure and opens review from the keyboard", async () => {
    const user = userEvent.setup();
    renderCard(<RequirementApprovalCard wsId="ws-1" detail={makeDetail()} />);
    const review = screen.getByRole("button", { name: "Review and approve" });
    review.focus();
    await user.keyboard("{Enter}");

    expect(await screen.findByText("Ship v1")).toBeInTheDocument();
    expect(screen.getByText("Out of scope")).toBeInTheDocument();
    expect(screen.getByText("Requirement flow")).toBeInTheDocument();
    expect(screen.getByText("Keep one Issue")).toBeInTheDocument();
    expect(screen.getByText("Approve creates one Issue")).toBeInTheDocument();
    expect(screen.getByText("Core project")).toBeInTheDocument();
    expect(screen.getByText("Planner")).toBeInTheDocument();
    expect(screen.getByText("Non-goals")).toBeInTheDocument();
    expect(screen.getByText("Scope")).toBeInTheDocument();
    expect(screen.getByText("Constraints")).toBeInTheDocument();
    expect(screen.getByText("Acceptance criteria")).toBeInTheDocument();
    expect(screen.getByText(/Creates one Issue and starts the agent/)).toBeInTheDocument();
    expect(screen.getAllByText(/Candidate v2/).length).toBeGreaterThan(0);
    expect(document.querySelectorAll("dl").length).toBeGreaterThan(0);
    expect(document.querySelectorAll("ul li").length).toBeGreaterThan(0);
  });

  it("renders empty arrays and unknown ids without losing the review", async () => {
    const detail = makeDetail({
      latestCandidate: {
        id: "candidate-2",
        requirementId: "req-1",
        revision: 3,
        snapshot: {
          ...candidateSnapshot,
          nonGoals: [],
          scope: [],
          constraints: [],
          acceptanceCriteria: [],
          projectId: "99999999-9999-4999-9999-999999999999",
          agentId: "88888888-8888-4888-8888-888888888888",
        },
        digest: "digest-2",
        createdByTaskId: "task-2",
      },
    });
    renderCard(<RequirementApprovalCard wsId="ws-1" detail={detail} />, {
      listProjects: vi.fn().mockResolvedValue({ projects: [], total: 0 }),
      listAgents: vi.fn().mockResolvedValue([]),
    });
    fireEvent.click(screen.getByRole("button", { name: "Review and approve" }));

    expect((await screen.findAllByText("None")).length).toBe(4);
    expect(screen.getByText("Unknown project (99999999...)")).toBeInTheDocument();
    expect(screen.getByText("Unknown agent (88888888...)")).toBeInTheDocument();
  });

  it("shows No project when the candidate has no project", () => {
    const detail = makeDetail({
      latestCandidate: {
        id: "candidate-2",
        requirementId: "req-1",
        revision: 3,
        snapshot: { ...candidateSnapshot, projectId: null },
        digest: "digest-2",
        createdByTaskId: "task-2",
      },
    });
    renderCard(<RequirementApprovalCard wsId="ws-1" detail={detail} />);
    fireEvent.click(screen.getByRole("button", { name: "Review and approve" }));
    expect(screen.getByText("No project")).toBeInTheDocument();
  });
});

describe("RequirementReviewCard", () => {
  it("reviews the summary and source status before accepting the Issue revision", async () => {
    const acceptRequirement = vi.fn().mockResolvedValue(makeDetail());
    const detail = makeDetail({
      state: "in_review", issue, issueId: issue.id,
      sourceSnapshot: {
        source: { host: "dj01.pm.netease.com", id: "100", url: "https://dj01.pm.netease.com/issues/100" },
        title: "Source", digest: "source-digest", statuses: [{ id: 5, name: "Done" }],
      },
    });
    renderCard(<RequirementReviewCard wsId="ws-1" detail={detail} />, { acceptRequirement });

    fireEvent.click(screen.getByRole("button", { name: "Accept" }));
    expect(acceptRequirement).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText("Delivery summary and links"), { target: { value: "Verified delivery https://example.test/report" } });
    fireEvent.change(screen.getByLabelText("Yixiezuo status"), { target: { value: "Done" } });
    fireEvent.click(screen.getByRole("button", { name: "Accept and publish" }));

    await waitFor(() => expect(acceptRequirement).toHaveBeenCalledWith("ws-1", "req-1", {
      expectedIssueRevision: 9,
      summary: "Verified delivery https://example.test/report",
      statusName: "Done",
      idempotencyKey: expect.any(String),
    }));
  });

  it("reuses the accept key when retrying after a committed response is lost", async () => {
    const acceptRequirement = vi.fn()
      .mockRejectedValueOnce(new Error("response lost"))
      .mockResolvedValueOnce(makeDetail({ state: "done", issue, issueId: issue.id }));
    const detail = makeDetail({ state: "in_review", issue, issueId: issue.id });
    renderCard(<RequirementReviewCard wsId="ws-1" detail={detail} />, { acceptRequirement });

    fireEvent.click(screen.getByRole("button", { name: "Accept" }));
    fireEvent.change(screen.getByLabelText("Delivery summary and links"), { target: { value: "Reviewed delivery" } });
    fireEvent.click(screen.getByRole("button", { name: "Accept and publish" }));
    expect(await screen.findByText("response lost")).toBeInTheDocument();
    const firstKey = acceptRequirement.mock.calls[0]?.[2]?.idempotencyKey;
    expect(firstKey).toEqual(expect.any(String));

    fireEvent.click(screen.getByRole("button", { name: "Accept and publish" }));
    await waitFor(() => expect(acceptRequirement).toHaveBeenCalledTimes(2));
    expect(acceptRequirement.mock.calls[1]?.[2]).toEqual({
      expectedIssueRevision: 9,
      summary: "Reviewed delivery",
      statusName: "",
      idempotencyKey: firstKey,
    });
  });

  it("prefills the execution result rather than presenting the plan goal as a delivery", () => {
    const detail = makeDetail({
      state: "in_review", issue, issueId: issue.id,
      run: { id: "run-1", status: "completed", result: { output: "Verified file: https://example.test/artifact" } },
    });
    renderCard(<RequirementReviewCard wsId="ws-1" detail={detail} />);
    fireEvent.click(screen.getByRole("button", { name: "Accept" }));
    expect(screen.getByLabelText("Delivery summary and links")).toHaveValue("Verified file: https://example.test/artifact");
    expect(screen.getByLabelText("Delivery summary and links")).not.toHaveValue(candidateSnapshot.goal);
  });

  it("blocks acceptance when the Issue changes during the publication review", async () => {
    const acceptRequirement = vi.fn();
    const initial = makeDetail({ state: "in_review", issue, issueId: issue.id });
    const view = renderCard(<RequirementReviewCard wsId="ws-1" detail={initial} />, { acceptRequirement });
    fireEvent.click(screen.getByRole("button", { name: "Accept" }));
    fireEvent.change(screen.getByLabelText("Delivery summary and links"), { target: { value: "Reviewed delivery" } });
    const changed = makeDetail({ state: "in_review", issue: { ...issue, revision: 10 }, issueId: issue.id });
    view.rerender(<QueryClientProvider client={view.qc}><RequirementReviewCard wsId="ws-1" detail={changed} /></QueryClientProvider>);

    expect(await screen.findByText(/The Issue changed/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Accept and publish" })).toBeDisabled();
    expect(screen.getByLabelText("Delivery summary and links")).toHaveValue("Reviewed delivery");
    expect(acceptRequirement).not.toHaveBeenCalled();
  });

  it("requires a non-empty delivery summary and creates a new key for a changed review", async () => {
    const acceptRequirement = vi.fn()
      .mockRejectedValueOnce(new Error("summary rejected"))
      .mockResolvedValueOnce(makeDetail());
    const detail = makeDetail({ state: "in_review", issue, issueId: issue.id });
    renderCard(<RequirementReviewCard wsId="ws-1" detail={detail} />, { acceptRequirement });
    fireEvent.click(screen.getByRole("button", { name: "Accept" }));
    fireEvent.change(screen.getByLabelText("Delivery summary and links"), { target: { value: " " } });
    expect(screen.getByRole("button", { name: "Accept and publish" })).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Delivery summary and links"), { target: { value: "First review" } });
    fireEvent.click(screen.getByRole("button", { name: "Accept and publish" }));
    await screen.findByText("summary rejected");
    const firstKey = acceptRequirement.mock.calls[0]?.[2]?.idempotencyKey;
    fireEvent.change(screen.getByLabelText("Delivery summary and links"), { target: { value: "Corrected review" } });
    fireEvent.click(screen.getByRole("button", { name: "Accept and publish" }));
    await waitFor(() => expect(acceptRequirement).toHaveBeenCalledTimes(2));
    expect(acceptRequirement.mock.calls[1]?.[2]?.idempotencyKey).not.toBe(firstKey);
  });

  it("requires a reject reason and preserves it after failure", async () => {
    const rejectRequirement = vi.fn()
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValueOnce(makeDetail());
    const detail = makeDetail({ state: "in_review", issue, issueId: issue.id });
    renderCard(<RequirementReviewCard wsId="ws-1" detail={detail} />, { rejectRequirement });

    fireEvent.click(screen.getByRole("button", { name: "Request changes" }));
    const textarea = screen.getByLabelText("Reason");
    fireEvent.change(textarea, { target: { value: "Needs acceptance criteria" } });
    fireEvent.click(screen.getByRole("button", { name: "Request changes" }));
    await screen.findByText("offline");
    expect(textarea).toHaveValue("Needs acceptance criteria");

    fireEvent.click(screen.getByRole("button", { name: "Request changes" }));
    await waitFor(() => expect(rejectRequirement).toHaveBeenCalledTimes(2));
    expect(rejectRequirement).toHaveBeenLastCalledWith("ws-1", "req-1", {
      expectedIssueRevision: 9,
      reason: "Needs acceptance criteria",
      idempotencyKey: expect.any(String),
    });
    expect(screen.queryByLabelText("Reason")).not.toBeInTheDocument();
  });

  it("keeps cancellation and its failure visible inside the confirmation dialog", async () => {
    const cancelRequirement = vi.fn().mockRejectedValue(new Error("cancel failed"));
    const detail = makeDetail({ state: "in_review", issue, issueId: issue.id });
    renderCard(<RequirementReviewCard wsId="ws-1" detail={detail} />, { cancelRequirement });

    fireEvent.click(screen.getByRole("button", { name: "Cancel requirement" }));
    fireEvent.click(screen.getAllByRole("button", { name: "Cancel requirement" }).at(-1)!);

    expect(await screen.findByText("cancel failed")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Cancel this requirement?" })).toBeInTheDocument();
    expect(cancelRequirement).toHaveBeenCalledWith("ws-1", "req-1", {
      expectedIssueRevision: 9,
      idempotencyKey: expect.any(String),
    });
  });

  it("uses expectedRevision only for pre-Issue cancellation", async () => {
    const cancelRequirement = vi.fn().mockResolvedValue(makeDetail());
    renderCard(<RequirementReviewCard wsId="ws-1" detail={makeDetail()} />, { cancelRequirement });

    fireEvent.click(screen.getByRole("button", { name: "Cancel requirement" }));
    fireEvent.click(screen.getAllByRole("button", { name: "Cancel requirement" }).at(-1)!);

    await waitFor(() => expect(cancelRequirement).toHaveBeenCalledWith("ws-1", "req-1", {
      expectedRevision: 4,
      idempotencyKey: expect.any(String),
    }));
  });

  it("does not cancel a bound Issue without its revision", () => {
    const cancelRequirement = vi.fn();
    const detail = makeDetail({ state: "in_review", issueId: issue.id, issue: null });
    renderCard(<RequirementReviewCard wsId="ws-1" detail={detail} />, { cancelRequirement });

    expect(screen.getByRole("button", { name: "Cancel requirement" })).toBeDisabled();
    expect(cancelRequirement).not.toHaveBeenCalled();
  });
});

describe("RequirementCard participants", () => {
  it("edits participants from the member query and saves the desired set", async () => {
    const updateRequirementParticipants = vi.fn().mockResolvedValue(makeDetail());
    renderCard(<RequirementCard wsId="ws-1" detail={makeDetail()} />, { updateRequirementParticipants });

    fireEvent.click(await screen.findByRole("button", { name: "Edit participants" }));
    fireEvent.change(screen.getByLabelText("Add member"), { target: { value: "user-2" } });
    fireEvent.click(screen.getByRole("button", { name: "Save participants" }));

    await waitFor(() => expect(updateRequirementParticipants).toHaveBeenCalledWith("ws-1", "req-1", {
      expectedRevision: 4,
      participants: [
        { userId: "user-1", roles: ["requester", "approver"] },
        { userId: "user-2", roles: ["viewer"] },
      ],
      idempotencyKey: expect.any(String),
    }));
  });

  it("does not offer editing without participant permission", () => {
    renderCard(<RequirementCard wsId="ws-1" detail={makeDetail({ permissions: { ...permissions, canManageParticipants: false } })} />);
    expect(screen.queryByRole("button", { name: "Edit participants" })).not.toBeInTheDocument();
  });
});

describe("RequirementSyncStatus", () => {
  it("does not auto-retry unknown and reuses the key only after a failed readback", async () => {
    const reconcileRequirement = vi.fn()
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValueOnce(makeDetail());
    const detail = makeDetail({
      operations: [{ id: "operation-1", kind: "writeback", state: "unknown" }],
    });
    renderCard(<RequirementSyncStatus wsId="ws-1" detail={detail} />, { reconcileRequirement });
    expect(reconcileRequirement).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Read back operation" }));
    await screen.findByText("offline");
    fireEvent.click(screen.getByRole("button", { name: "Read back operation" }));
    await waitFor(() => expect(reconcileRequirement).toHaveBeenCalledTimes(2));

    const firstKey = reconcileRequirement.mock.calls[0]?.[2]?.idempotencyKey;
    const secondKey = reconcileRequirement.mock.calls[1]?.[2]?.idempotencyKey;
    expect(firstKey).toEqual(expect.any(String));
    expect(secondKey).toBe(firstKey);
    expect(reconcileRequirement.mock.calls[0]?.slice(0, 2)).toEqual(["ws-1", "req-1"]);
    expect(reconcileRequirement.mock.calls[0]?.[2]?.operationId).toBe("operation-1");
  });

  it("reconciles a confirmed-failed delivery after done", async () => {
    const reconcileRequirement = vi.fn().mockResolvedValue(makeDetail());
    const detail = makeDetail({
      state: "done",
      deliveries: [{ id: "delivery-1", kind: "requirement_status", state: "failed" }],
    });
    renderCard(<RequirementSyncStatus wsId="ws-1" detail={detail} />, { reconcileRequirement });
    expect(reconcileRequirement).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Reconcile delivery" }));
    await waitFor(() => expect(reconcileRequirement).toHaveBeenCalledWith("ws-1", "req-1", {
      operationId: undefined,
      deliveryId: "delivery-1",
      idempotencyKey: expect.any(String),
    }));
  });

  it("keeps unknown delivery reconcile available after cancellation without automatic retry", async () => {
    const reconcileRequirement = vi.fn().mockResolvedValue(makeDetail());
    const detail = makeDetail({
      state: "cancelled",
      cancelledAt: "2026-01-02T00:00:00Z",
      deliveries: [{ id: "delivery-2", kind: "requirement_status", state: "unknown" }],
    });
    renderCard(<RequirementSyncStatus wsId="ws-1" detail={detail} />, { reconcileRequirement });
    expect(reconcileRequirement).not.toHaveBeenCalled();
    expect(screen.getByText("Unknown is never retried automatically.")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Reconcile delivery" }));
    await waitFor(() => expect(reconcileRequirement).toHaveBeenCalledWith("ws-1", "req-1", {
      operationId: undefined,
      deliveryId: "delivery-2",
      idempotencyKey: expect.any(String),
    }));
  });

  it("shows operation request hash when present", () => {
    const detail = makeDetail({
      operations: [{ id: "operation-hash", kind: "reconcile", state: "needs_reconcile", requestHash: "hash-123" }],
    });
    renderCard(<RequirementSyncStatus wsId="ws-1" detail={detail} />);
    expect(screen.getByText("request_hash: hash-123")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Read back operation" })).not.toBeInTheDocument();
  });

  it("does not offer publication readback for read-only operations", () => {
    const detail = makeDetail({
      operations: [
        { id: "preview-1", kind: "preview", state: "failed" },
        { id: "refresh-1", kind: "refresh", state: "unknown" },
      ],
    });
    renderCard(<RequirementSyncStatus wsId="ws-1" detail={detail} />);
    expect(screen.queryByRole("button", { name: "Read back operation" })).not.toBeInTheDocument();
  });
});

describe("RequirementCard and FlowPanel projections", () => {
  it("distinguishes a completed Run from Requirement done", () => {
    const detail = makeDetail({
      state: "in_review",
      run: { id: "task-1", status: "completed" },
    });
    renderCard(<RequirementCard wsId="ws-1" detail={detail} />);

    expect(screen.getByText("Latest run")).toBeInTheDocument();
    expect(screen.getByText("completed")).toBeInTheDocument();
    expect(screen.getByText("Run completed; the requirement is not done yet.")).toBeInTheDocument();
  });

  it("keeps existing review, cancel, and reconcile visible when new intake is unavailable", () => {
    const detail = makeDetail({
      state: "in_review",
      issue,
      issueId: issue.id,
      run: { id: "task-1", status: "completed" },
      operations: [{ id: "operation-1", kind: "writeback", state: "unknown" }],
      permissions: { ...permissions, canOpen: false },
    });
    renderCard(
      <RequirementFlowPanel
        wsId="ws-1"
        surface={{ detail, error: null, isLoading: false, canCompose: false, projectLocked: true }}
      />,
    );

    expect(screen.getByRole("button", { name: "Accept" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cancel requirement" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Read back operation" })).toBeInTheDocument();
    expect(screen.getByText("Run completed; the requirement is not done yet.")).toBeInTheDocument();
  });
});

describe("RequirementFlowPanel load failure", () => {
  const failedSurface = {
    detail: null,
    error: new Error("detail failed"),
    isLoading: false,
    canCompose: false,
    projectLocked: false,
  };

  it("shows the failure with a retry action that calls the surface refetch", () => {
    const refetch = vi.fn();
    renderCard(
      <RequirementFlowPanel wsId="ws-1" surface={{ ...failedSurface, refetch }} />,
    );

    expect(screen.getByRole("alert")).toHaveTextContent("detail failed");
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));

    expect(refetch).toHaveBeenCalledTimes(1);
  });

  it("refetches the Requirement queries when the surface has no refetch", () => {
    const { qc } = renderCard(
      <RequirementFlowPanel wsId="ws-1" surface={failedSurface} />,
    );
    const invalidate = vi.spyOn(qc, "invalidateQueries");

    fireEvent.click(screen.getByRole("button", { name: "Try again" }));

    expect(invalidate).toHaveBeenCalledWith({ queryKey: requirementKeys.all("ws-1") });
  });
});

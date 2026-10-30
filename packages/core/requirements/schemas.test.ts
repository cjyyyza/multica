// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  DEFAULT_REQUIREMENT_PERMISSIONS,
  RequirementApproveResponseSchema,
  RequirementCandidateSnapshotSchema,
  RequirementDeliverySchema,
  RequirementDetailSchema,
  RequirementOpenResponseSchema,
  RequirementOperationSchema,
} from "./schemas";

const baseDetail = {
  id: "req-1",
  workspaceId: "ws-1",
  requesterId: "user-1",
};

it("retains the verified source draft as an intake operation", () => {
  expect(RequirementOperationSchema.parse({ id: "draft-1", kind: "draft", state: "succeeded" }))
    .toMatchObject({ kind: "draft", state: "succeeded" });
});

const baseIssue = {
  id: "issue-1",
  workspace_id: "ws-1",
  number: 1,
  identifier: "MUL-1",
  title: "Requirement issue",
  description: null,
  status: "todo",
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
};

describe("RequirementCandidateSnapshotSchema", () => {
  const snapshot = {
    goal: "Ship requirements",
    nonGoals: ["Out of scope"],
    scope: ["Requirement flow"],
    constraints: ["Keep one Issue"],
    acceptanceCriteria: ["Approve creates one Issue"],
    projectId: "project-1",
    agentId: "agent-1",
  };

  it("accepts every structured field and allows an empty array", () => {
    expect(RequirementCandidateSnapshotSchema.parse(snapshot)).toEqual(snapshot);
    expect(RequirementCandidateSnapshotSchema.parse({
      ...snapshot,
      nonGoals: [],
      scope: [],
      constraints: [],
      acceptanceCriteria: [],
      projectId: null,
    })).toMatchObject({ nonGoals: [], scope: [], constraints: [], acceptanceCriteria: [], projectId: null });
  });

  it("supports optional source attachment IDs without accepting other attachment payloads", () => {
    expect(RequirementCandidateSnapshotSchema.parse({
      ...snapshot,
      attachmentIds: ["source-1", "source-2"],
    })).toMatchObject({ attachmentIds: ["source-1", "source-2"] });
    expect(RequirementCandidateSnapshotSchema.safeParse({ ...snapshot, attachmentIds: "source-1" }).success).toBe(false);
    expect(RequirementCandidateSnapshotSchema.safeParse({ ...snapshot, attachmentIds: null }).success).toBe(false);
    expect(RequirementCandidateSnapshotSchema.safeParse({ ...snapshot, attachments: ["source-1"] }).success).toBe(false);
  });

  it("rejects unknown fields, missing fields, wrong types, and legacy generic snapshots", () => {
    expect(RequirementCandidateSnapshotSchema.safeParse({ ...snapshot, unexpected: true }).success).toBe(false);
    expect(RequirementCandidateSnapshotSchema.safeParse({ goal: "Ship requirements" }).success).toBe(false);
    expect(RequirementCandidateSnapshotSchema.safeParse({ ...snapshot, scope: "Requirement flow" }).success).toBe(false);
    expect(RequirementCandidateSnapshotSchema.safeParse({ ...snapshot, projectId: undefined }).success).toBe(false);
    expect(RequirementCandidateSnapshotSchema.safeParse({ ...snapshot, agentId: null }).success).toBe(false);
  });

  it("accepts a native squad candidate without inventing a plan task", () => {
    const parsed = RequirementDetailSchema.parse({
      ...baseDetail,
      squadId: "squad-1",
      latestCandidate: {
        id: "candidate-1",
        requirementId: "req-1",
        snapshot,
        digest: "digest-1",
        createdByTaskId: null,
      },
    });
    expect(parsed.squadId).toBe("squad-1");
    expect(parsed.latestCandidate?.createdByTaskId).toBeNull();
  });

  it("drops a legacy candidate from detail without failing the whole record", () => {
    const parsed = RequirementDetailSchema.parse({
      ...baseDetail,
      latestCandidate: {
        id: "candidate-1",
        requirementId: "req-1",
        snapshot: { goal: "Legacy generic candidate" },
        digest: "digest-1",
        createdByTaskId: "task-1",
      },
    });
    expect(parsed.id).toBe("req-1");
    expect(parsed.latestCandidate).toBeNull();
  });
});

describe("RequirementDetailSchema", () => {
  it("defaults absent optional projections without inventing CAS revisions", () => {
    const parsed = RequirementDetailSchema.parse(baseDetail);

    expect(parsed).toMatchObject({
      state: "unknown",
      intakeState: "unknown",
      latestCandidate: null,
      issue: null,
      run: null,
      participants: [],
      operations: [],
      deliveries: [],
      permissions: DEFAULT_REQUIREMENT_PERMISSIONS,
    });
    expect(parsed.revision).toBeUndefined();
    expect(RequirementDetailSchema.safeParse({ ...baseDetail, revision: "1" }).success).toBe(false);
  });

  it("normalizes every unknown state and operation or delivery enum to unknown", () => {
    const detail = RequirementDetailSchema.parse({
      ...baseDetail,
      state: "future_requirement_state",
      intakeState: "future_intake_state",
      participants: [{ id: "participant-1", userId: "user-1", roles: ["requester", "future_role"] }],
      operations: [{ id: "operation-1", kind: "future_kind", state: "future_state" }],
      deliveries: [{ id: "delivery-1", kind: "future_kind", state: "future_state" }],
    });

    expect(detail.state).toBe("unknown");
    expect(detail.intakeState).toBe("unknown");
    expect(detail.participants[0]?.roles).toEqual(["requester", "unknown"]);
    expect(detail.operations[0]).toMatchObject({ kind: "unknown", state: "unknown" });
    expect(detail.deliveries[0]).toMatchObject({ kind: "unknown", state: "unknown" });

    expect(RequirementOperationSchema.parse({ id: "operation-2", kind: "intake", state: "needs_reconcile" }))
      .toMatchObject({ kind: "intake", state: "needs_reconcile" });
    expect(RequirementDeliverySchema.parse({ id: "delivery-2", kind: "requirement_draft", state: "unknown" }))
      .toMatchObject({ kind: "requirement_draft", state: "unknown" });
    expect(RequirementOperationSchema.parse({
      id: "operation-reconcile", kind: "reconcile", state: "needs_reconcile",
      targetOperationId: "operation-1", requestHash: "hash-1",
    })).toMatchObject({ targetOperationId: "operation-1", requestHash: "hash-1" });
    expect(RequirementDeliverySchema.parse({
      id: "delivery-reconcile", kind: "requirement_status", state: "accepted",
      transportAttempt: 2, requestHash: "hash-2",
    })).toMatchObject({ transportAttempt: 2, requestHash: "hash-2" });
  });

  it("fails permissions closed individually and when the whole object is malformed", () => {
    const partial = RequirementDetailSchema.parse({
      ...baseDetail,
      permissions: { canRead: true, canOpen: "yes", canApprove: true },
    });
    expect(partial.permissions).toEqual({
      ...DEFAULT_REQUIREMENT_PERMISSIONS,
      canRead: true,
      canApprove: true,
    });

    const malformed = RequirementDetailSchema.parse({
      ...baseDetail,
      permissions: ["canRead", "canApprove"],
    });
    expect(malformed.permissions).toEqual(DEFAULT_REQUIREMENT_PERMISSIONS);
  });

  it("isolates malformed issue, run, candidate, and collection items from the detail", () => {
    const parsed = RequirementDetailSchema.parse({
      ...baseDetail,
      issue: baseIssue,
      run: { id: 42 },
      latestCandidate: { id: "candidate-without-required-fields" },
      participants: [
        { id: "participant-1", userId: "user-1", roles: ["requester"] },
        { id: 42 },
      ],
      operations: [
        { id: "operation-1", kind: "writeback", state: "running" },
        { id: 42 },
      ],
      deliveries: "not-an-array",
    });

    expect(parsed.id).toBe("req-1");
    expect(parsed.issue?.id).toBe("issue-1");
    expect(parsed.issue?.revision).toBeUndefined();
    expect(parsed.run).toBeNull();
    expect(parsed.latestCandidate).toBeNull();
    expect(parsed.participants.map((participant) => participant.id)).toEqual(["participant-1"]);
    expect(parsed.operations.map((operation) => operation.id)).toEqual(["operation-1"]);
    expect(parsed.deliveries).toEqual([]);

    const malformedIssue = RequirementDetailSchema.parse({
      ...baseDetail,
      issue: { id: "incomplete" },
    });
    expect(malformedIssue.id).toBe("req-1");
    expect(malformedIssue.issue).toBeNull();
  });

  it("parses camelCase source, candidate, and binding fields", () => {
    const parsed = RequirementDetailSchema.parse({
      ...baseDetail,
      revision: 7,
      state: "awaiting_approval",
      intakeState: "awaiting_approval",
      sourceHost: "yixiezuo",
      externalId: "42",
      sourceUrl: "https://source.example/42",
      sourceSnapshot: { title: "Requirement" },
      sourceDigest: "sha256:source",
      sourceLockVersion: 2,
      agentId: "agent-1",
      projectId: "project-1",
      chatSessionId: "chat-1",
      issueId: "issue-1",
      intakeOperationId: "operation-intake",
      latestCandidate: {
        id: "candidate-1",
        requirementId: "req-1",
        snapshot: {
          goal: "Requirement",
          nonGoals: ["Out of scope"],
          scope: ["Requirement flow"],
          constraints: ["Keep one Issue"],
          acceptanceCriteria: ["Approve creates one Issue"],
          projectId: "project-1",
          agentId: "agent-1",
        },
        digest: "sha256:candidate",
        createdByTaskId: "task-1",
        createdAt: "2026-01-01T00:00:00Z",
      },
    });

    expect(parsed).toMatchObject({
      revision: 7,
      sourceHost: "yixiezuo",
      externalId: "42",
      sourceSnapshot: { title: "Requirement" },
      sourceLockVersion: 2,
      chatSessionId: "chat-1",
      issueId: "issue-1",
      latestCandidate: {
        id: "candidate-1",
        requirementId: "req-1",
        createdByTaskId: "task-1",
      },
    });
    expect(parsed.latestCandidate?.revision).toBeUndefined();
  });

  it("requires durable requirement and requester identities", () => {
    expect(RequirementDetailSchema.safeParse({}).success).toBe(false);
    expect(RequirementDetailSchema.safeParse({ id: "req-1", workspaceId: "ws-1" }).success).toBe(false);
  });
});

describe("Requirement action envelopes", () => {
  it("requires a session for open and a real issue for approve", () => {
    expect(RequirementOpenResponseSchema.safeParse({ requirement: baseDetail }).success).toBe(false);
    expect(RequirementOpenResponseSchema.safeParse({
      requirement: baseDetail,
      session: { id: "chat-1" },
    }).success).toBe(true);
    expect(RequirementApproveResponseSchema.safeParse({
      requirement: baseDetail,
      issue: {},
    }).success).toBe(false);
    expect(RequirementApproveResponseSchema.safeParse({
      requirement: baseDetail,
      issue: baseIssue,
    }).success).toBe(true);
  });
});

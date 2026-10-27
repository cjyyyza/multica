import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";
import type {
  CreateRequirementRequest,
  UpdateRequirementParticipantsRequest,
  OpenRequirementRequest,
  SubmitRequirementCandidateRequest,
  ApproveRequirementRequest,
  AcceptRequirementRequest,
  RejectRequirementRequest,
  CancelRequirementRequest,
  ReconcileRequirementRequest,
} from "../requirements/types";

const requirement = (overrides: Record<string, unknown> = {}) => ({
  id: "req-1",
  workspaceId: "ws-1",
  requesterId: "user-1",
  ...overrides,
});

const issue = {
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

const createInput: CreateRequirementRequest = {
  sourceHost: "yixiezuo",
  externalId: "42",
  sourceUrl: "https://source.example/requirements/42",
  sourceSnapshot: { title: "Ship requirements" },
  sourceDigest: "sha256:source",
  sourceLockVersion: 3,
  agentId: "agent-1",
  projectId: null,
  participants: [{ userId: "user-1", roles: ["requester", "approver"] }],
  intakeOperationId: "operation-1",
};

const participantsInput: UpdateRequirementParticipantsRequest = {
  expectedRevision: 4,
  participants: [{ userId: "user-1", roles: ["requester", "approver"] }],
};
const openInput: OpenRequirementRequest = { expectedRevision: 4 };
const candidateInput: SubmitRequirementCandidateRequest = {
  expectedRevision: 4,
  snapshot: {
    goal: "Ship requirements",
    nonGoals: ["Out of scope"],
    scope: ["Requirement flow"],
    constraints: ["Keep one Issue"],
    acceptanceCriteria: ["Approve creates one Issue"],
    projectId: "project-1",
    agentId: "agent-1",
  },
};
const approveInput: ApproveRequirementRequest = {
  expectedRevision: 4,
  candidateId: "candidate-1",
  candidateDigest: "sha256:candidate",
};
const acceptInput: AcceptRequirementRequest = { expectedIssueRevision: 8 };
const rejectInput: RejectRequirementRequest = {
  expectedIssueRevision: 8,
  reason: "Needs another pass",
};
const cancelInput: CancelRequirementRequest = {
  expectedIssueRevision: 8,
  reason: "Withdrawn",
};
const reconcileInput: ReconcileRequirementRequest = { operationId: "operation-1", deliveryId: "delivery-1" };

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function requestHeaders(init: RequestInit | undefined): Record<string, string> {
  return (init?.headers ?? {}) as Record<string, string>;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("ApiClient Requirement HTTP contract", () => {
  it("saves POPO intake configuration through a validated response", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ id: "install-1", status: "active", requirement_template_url: "https://dj01.pm.netease.com/issues/108402" }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");
    const saved = await client.setPopoRequirementIntake("ws/1", "install/1", "https://dj01.pm.netease.com/issues/108402");
    expect(saved.requirement_template_url).toBe("https://dj01.pm.netease.com/issues/108402");
    const call = fetchMock.mock.calls[0];
    if (!call) throw new Error("The settings mutation did not send an HTTP request");
    const [url, options] = call;
    expect(url).toBe("https://api.example.test/api/workspaces/ws%2F1/popo/installations/install%2F1/requirement-intake");
    expect(options.method).toBe("PATCH");
    expect(JSON.parse(options.body)).toEqual({ template_url: "https://dj01.pm.netease.com/issues/108402" });
    fetchMock.mockResolvedValue(jsonResponse({ id: "install-1", requirement_template_url: 42 }));
    expect((await client.setPopoRequirementIntake("ws-1", "install-1", "")).id).toBe("");
  });
  it("uses the frozen routes, methods, bodies, and caller-supplied idempotency keys", async () => {
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      if (url.endsWith("/open")) {
        return Promise.resolve(jsonResponse({
          requirement: requirement({ chatSessionId: "chat-1" }),
          session: { id: "chat-1" },
          pendingTask: { task_id: "task-1", status: "queued" },
        }));
      }
      if (url.endsWith("/approve")) {
        return Promise.resolve(jsonResponse({ requirement: requirement(), issue }));
      }
      return Promise.resolve(jsonResponse(requirement()));
    });
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");

    await client.createRequirement("ws/1", { ...createInput, idempotencyKey: "create-key" });
    await client.getRequirement("ws/1", "req/1");
    await client.getRequirementByChat("ws/1", "chat/1");
    await client.updateRequirementParticipants("ws/1", "req/1", {
      ...participantsInput,
      idempotencyKey: "participants-key",
    });
    await client.openRequirement("ws/1", "req/1", {
      ...openInput,
      idempotencyKey: "open-key",
    });
    await client.submitRequirementCandidate("ws/1", "req/1", {
      ...candidateInput,
      idempotencyKey: "candidate-key",
    });
    await client.approveRequirement("ws/1", "req/1", {
      ...approveInput,
      idempotencyKey: "approve-key",
    });
    await client.acceptRequirement("ws/1", "req/1", {
      ...acceptInput,
      idempotencyKey: "accept-key",
    });
    await client.rejectRequirement("ws/1", "req/1", {
      ...rejectInput,
      idempotencyKey: "reject-key",
    });
    await client.cancelRequirement("ws/1", "req/1", {
      ...cancelInput,
      idempotencyKey: "cancel-key",
    });
    await client.reconcileRequirement("ws/1", "req/1", {
      ...reconcileInput,
      idempotencyKey: "reconcile-key",
    });

    expect(fetchMock.mock.calls.map(([url, init]) => [
      url,
      (init as RequestInit | undefined)?.method ?? "GET",
    ])).toEqual([
      ["https://api.example.test/api/workspaces/ws%2F1/requirements", "POST"],
      ["https://api.example.test/api/workspaces/ws%2F1/requirements/req%2F1", "GET"],
      ["https://api.example.test/api/workspaces/ws%2F1/requirements?chat_session_id=chat%2F1", "GET"],
      ["https://api.example.test/api/workspaces/ws%2F1/requirements/req%2F1/participants", "POST"],
      ["https://api.example.test/api/workspaces/ws%2F1/requirements/req%2F1/open", "POST"],
      ["https://api.example.test/api/workspaces/ws%2F1/requirements/req%2F1/candidates", "POST"],
      ["https://api.example.test/api/workspaces/ws%2F1/requirements/req%2F1/approve", "POST"],
      ["https://api.example.test/api/workspaces/ws%2F1/requirements/req%2F1/accept", "POST"],
      ["https://api.example.test/api/workspaces/ws%2F1/requirements/req%2F1/reject", "POST"],
      ["https://api.example.test/api/workspaces/ws%2F1/requirements/req%2F1/cancel", "POST"],
      ["https://api.example.test/api/workspaces/ws%2F1/requirements/req%2F1/reconcile", "POST"],
    ]);

    const posts = fetchMock.mock.calls.filter(([, init]) =>
      (init as RequestInit | undefined)?.method === "POST"
    );
    expect(posts.map(([, init]) => requestHeaders(init as RequestInit)["Idempotency-Key"])).toEqual([
      "create-key",
      "participants-key",
      "open-key",
      "candidate-key",
      "approve-key",
      "accept-key",
      "reject-key",
      "cancel-key",
      "reconcile-key",
    ]);
    for (const [, init] of posts) {
      const body = JSON.parse(String((init as RequestInit).body)) as Record<string, unknown>;
      expect(body).not.toHaveProperty("idempotencyKey");
    }
    expect(JSON.parse(String((posts[0]?.[1] as RequestInit).body))).toEqual(createInput);
    expect(JSON.parse(String((posts[2]?.[1] as RequestInit).body))).toEqual(openInput);
    expect(JSON.parse(String((posts[4]?.[1] as RequestInit).body))).toEqual(approveInput);
    expect(JSON.parse(String((posts[6]?.[1] as RequestInit).body))).toEqual(rejectInput);
    expect(JSON.parse(String((posts[8]?.[1] as RequestInit).body))).toEqual(reconcileInput);
  });

  it("keeps the same key across a caller retry", async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(jsonResponse(requirement())));
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");
    const input = { ...acceptInput, idempotencyKey: "stable-accept-key" };

    await client.acceptRequirement("ws-1", "req-1", input);
    await client.acceptRequirement("ws-1", "req-1", input);

    expect(fetchMock.mock.calls.map(([, init]) =>
      requestHeaders(init as RequestInit)["Idempotency-Key"]
    )).toEqual(["stable-accept-key", "stable-accept-key"]);
  });

  it("rejects an empty idempotency key before any write request", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");
    const writes = [
      (idempotencyKey: string) => client.createRequirement("ws-1", { ...createInput, idempotencyKey }),
      (idempotencyKey: string) => client.updateRequirementParticipants("ws-1", "req-1", { ...participantsInput, idempotencyKey }),
      (idempotencyKey: string) => client.openRequirement("ws-1", "req-1", { ...openInput, idempotencyKey }),
      (idempotencyKey: string) => client.submitRequirementCandidate("ws-1", "req-1", { ...candidateInput, idempotencyKey }),
      (idempotencyKey: string) => client.approveRequirement("ws-1", "req-1", { ...approveInput, idempotencyKey }),
      (idempotencyKey: string) => client.acceptRequirement("ws-1", "req-1", { ...acceptInput, idempotencyKey }),
      (idempotencyKey: string) => client.rejectRequirement("ws-1", "req-1", { ...rejectInput, idempotencyKey }),
      (idempotencyKey: string) => client.cancelRequirement("ws-1", "req-1", { ...cancelInput, idempotencyKey }),
      (idempotencyKey: string) => client.reconcileRequirement("ws-1", "req-1", { ...reconcileInput, idempotencyKey }),
      (idempotencyKey: string) => client.createRequirementDraft("ws-1", { title: "Idea", body: "Idea", agentId: "agent-1", idempotencyKey }),
      (idempotencyKey: string) => client.reconcileRequirementDraft("ws-1", "op-1", { url: "https://source.example/42", idempotencyKey }),
    ];

    for (const write of writes) {
      await expect(write(" \t ")).rejects.toThrow(/non-empty Idempotency-Key/);
    }
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("queues an idea draft with the caller key and omits the default template", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({
      id: "draft-1",
      kind: "draft",
      state: "pending",
      error: "",
    }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");

    const created = await client.createRequirementDraft("ws-1", {
      title: "Remember me",
      body: "Remember me\nKeep the session",
      agentId: "agent-1",
      idempotencyKey: "draft-key",
    });
    expect(created).toMatchObject({ id: "draft-1", kind: "draft", state: "pending" });
    const [url, init] = fetchMock.mock.calls[0] ?? [];
    expect(url).toBe("https://api.example.test/api/workspaces/ws-1/yixiezuo/drafts");
    expect((init as RequestInit).method).toBe("POST");
    expect(requestHeaders(init as RequestInit)["Idempotency-Key"]).toBe("draft-key");
    expect(JSON.parse(String((init as RequestInit).body))).toEqual({
      title: "Remember me",
      body: "Remember me\nKeep the session",
      agent_id: "agent-1",
    });

    fetchMock.mockResolvedValueOnce(jsonResponse({ state: "pending" }));
    await expect(client.createRequirementDraft("ws-1", {
      title: "Remember me",
      body: "Remember me",
      agentId: "agent-1",
      idempotencyKey: "draft-key",
    })).rejects.toThrow(/Invalid 易协作 operation response/);
  });
});

describe("ApiClient Requirement malformed success responses", () => {
  it("rejects malformed detail responses from every detail-returning action", async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(jsonResponse({})));
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");
    const calls = [
      () => client.createRequirement("ws-1", { ...createInput, idempotencyKey: "create" }),
      () => client.getRequirement("ws-1", "req-1"),
      () => client.updateRequirementParticipants("ws-1", "req-1", { ...participantsInput, idempotencyKey: "participants" }),
      () => client.submitRequirementCandidate("ws-1", "req-1", { ...candidateInput, idempotencyKey: "candidate" }),
      () => client.acceptRequirement("ws-1", "req-1", { ...acceptInput, idempotencyKey: "accept" }),
      () => client.rejectRequirement("ws-1", "req-1", { ...rejectInput, idempotencyKey: "reject" }),
      () => client.cancelRequirement("ws-1", "req-1", { ...cancelInput, idempotencyKey: "cancel" }),
      () => client.reconcileRequirement("ws-1", "req-1", { ...reconcileInput, idempotencyKey: "reconcile" }),
    ];

    for (const call of calls) {
      await expect(call()).rejects.toThrow(/Malformed response/);
    }
  });

  it("rejects open and approve envelopes that cannot confirm their side effects", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ requirement: requirement() }))
      .mockResolvedValueOnce(jsonResponse({ requirement: requirement(), issue: {} }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");

    await expect(client.openRequirement("ws-1", "req-1", {
      ...openInput,
      idempotencyKey: "open",
    })).rejects.toThrow(/Malformed response/);
    await expect(client.approveRequirement("ws-1", "req-1", {
      ...approveInput,
      idempotencyKey: "approve",
    })).rejects.toThrow(/Malformed response/);
  });
});

describe("ApiClient getRequirementByChat degradation boundary", () => {
  it("returns null only for 404 and 403, preserves a literal null, and rejects malformed 200s", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ error: "not found" }, 404))
      .mockResolvedValueOnce(jsonResponse({ error: "forbidden" }, 403))
      .mockResolvedValueOnce(jsonResponse(null))
      .mockResolvedValueOnce(jsonResponse({}))
      .mockResolvedValueOnce(jsonResponse({ error: "unavailable" }, 503));
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");

    await expect(client.getRequirementByChat("ws-1", "chat-1")).resolves.toBeNull();
    await expect(client.getRequirementByChat("ws-1", "chat-1")).resolves.toBeNull();
    await expect(client.getRequirementByChat("ws-1", "chat-1")).resolves.toBeNull();
    await expect(client.getRequirementByChat("ws-1", "chat-1")).rejects.toThrow(/Malformed response/);
    await expect(client.getRequirementByChat("ws-1", "chat-1")).rejects.toMatchObject({ status: 503 });
  });
});

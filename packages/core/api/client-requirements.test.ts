import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient, REQUIREMENT_CANDIDATE_PROVENANCE_CAPABILITY } from "./client";
import type {
  CreateRequirementRequest,
  CreateRequirementDelegationRequest,
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

const delegationInput: CreateRequirementDelegationRequest = {
  externalRef: "popo-run-1",
  squadId: "squad-1",
  candidate: {
    goal: "Ship requirements",
    nonGoals: ["Direct agent runs"],
    scope: ["Squad execution"],
    constraints: ["Human approval first"],
    acceptanceCriteria: ["One squad Issue"],
    projectId: null,
  },
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
  it("advertises external candidate support and preserves its absent planning Run", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(requirement({
      latestCandidate: {
        id: "external-candidate", requirementId: "req-1", revision: 2,
        snapshot: candidateInput.snapshot, digest: "external-digest", createdByTaskId: null,
      },
    })));
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");
    const detail = await client.getRequirement("ws-1", "req-1");
    expect(detail.latestCandidate?.id).toBe("external-candidate");
    expect(detail.latestCandidate?.createdByTaskId).toBeNull();
    expect(requestHeaders(fetchMock.mock.calls[0]?.[1])).toMatchObject({
      "X-Client-Capabilities": REQUIREMENT_CANDIDATE_PROVENANCE_CAPABILITY,
    });
  });

  it("surfaces an upgrade response instead of presenting an empty candidate", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({
      code: "requirement_client_upgrade_required", error: "Update Multica to review this external candidate.",
    }, 426)));
    await expect(new ApiClient("https://api.example.test").getRequirement("ws-1", "req-1"))
      .rejects.toThrow("Update Multica to review this external candidate.");
  });
  it("starts native planning for an agent and lists caller-visible requirements", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(requirement({ state: "clarifying", chatSessionId: "chat-1" })))
      .mockResolvedValueOnce(jsonResponse({ requirements: [requirement(), { malformed: true }], hasMore: true, nextOffset: 10 }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");
    await client.createNativeRequirement("ws/1", { externalRef: "open/1", idea: "Fix the login bug", agentId: "agent-1", idempotencyKey: "plan-key" });
    const list = await client.listRequirements("ws/1", { externalRef: "open/1", limit: 10, offset: 0 });
    expect(list.requirements.map((item) => item.id)).toEqual(["req-1"]);
    expect(list.requirements[0]).not.toHaveProperty("run");
    expect(list.requirements[0]).not.toHaveProperty("latestCandidate");
    expect(list.hasMore).toBe(true);
    expect(list.nextOffset).toBe(10);
    expect(fetchMock.mock.calls[0]?.[0]).toBe("https://api.example.test/api/workspaces/ws%2F1/requirement-requests");
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit;
    expect(init.headers).toMatchObject({ "Idempotency-Key": "plan-key" });
    expect(JSON.parse(init.body as string)).toEqual({ externalRef: "open/1", idea: "Fix the login bug", agentId: "agent-1" });
    expect(fetchMock.mock.calls[1]?.[0]).toBe("https://api.example.test/api/workspaces/ws%2F1/requirements?externalRef=open%2F1&limit=10&offset=0");
  });

  it("rejects malformed native creation and listing responses", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(jsonResponse("invalid"))));
    const client = new ApiClient("https://api.example.test");
    await expect(client.createNativeRequirement("ws-1", { externalRef: "open-1", idea: "Fix a bug", squadId: "squad-1", idempotencyKey: "native-key" })).rejects.toThrow("Malformed response");
    await expect(client.listRequirements("ws-1")).rejects.toThrow("Malformed response");
  });

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
    await client.createRequirementDelegation("ws/1", { ...delegationInput, idempotencyKey: "delegation-key" });
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
      ["https://api.example.test/api/workspaces/ws%2F1/requirement-delegations", "POST"],
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
      "delegation-key",
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
    expect(JSON.parse(String((posts[1]?.[1] as RequestInit).body))).toEqual(delegationInput);
    expect(JSON.parse(String((posts[3]?.[1] as RequestInit).body))).toEqual(openInput);
    expect(JSON.parse(String((posts[5]?.[1] as RequestInit).body))).toEqual(approveInput);
    expect(JSON.parse(String((posts[7]?.[1] as RequestInit).body))).toEqual(rejectInput);
    expect(JSON.parse(String((posts[9]?.[1] as RequestInit).body))).toEqual(reconcileInput);
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
      (idempotencyKey: string) => client.createNativeRequirement("ws-1", { externalRef: "idea-1", idea: "Fix login", agentId: "agent-1", idempotencyKey }),
      (idempotencyKey: string) => client.sendRequirementMessage("ws-1", "req-1", { expectedRevision: 4, content: "Scope update", idempotencyKey }),
      (idempotencyKey: string) => client.createRequirement("ws-1", { ...createInput, idempotencyKey }),
      (idempotencyKey: string) => client.createRequirementDelegation("ws-1", { ...delegationInput, idempotencyKey }),
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
      () => client.createNativeRequirement("ws-1", { externalRef: "idea-1", idea: "Fix login", agentId: "agent-1", idempotencyKey: "plan" }),
      () => client.sendRequirementMessage("ws-1", "req-1", { expectedRevision: 4, content: "Scope update", idempotencyKey: "message" }),
      () => client.createRequirement("ws-1", { ...createInput, idempotencyKey: "create" }),
      () => client.createRequirementDelegation("ws-1", { ...delegationInput, idempotencyKey: "delegate" }),
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

describe("ApiClient native planning messages", () => {
  it("reuses the message key and revision without exposing them in query parameters", async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(jsonResponse(requirement({ revision: 5, state: "clarifying" }))));
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");
    const input = { expectedRevision: 4, content: "Only fix session renewal", idempotencyKey: "stable-reply" };
    await client.sendRequirementMessage("ws/1", "req/1", input);
    await client.sendRequirementMessage("ws/1", "req/1", input);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    for (const [url, init] of fetchMock.mock.calls) {
      expect(url).toBe("https://api.example.test/api/workspaces/ws%2F1/requirements/req%2F1/messages");
      expect(requestHeaders(init)["Idempotency-Key"]).toBe("stable-reply");
      expect(JSON.parse(init.body)).toEqual({ expectedRevision: 4, content: input.content });
    }
  });
});

describe("ApiClient execution replies", () => {
  const input = { expectedIssueRevision: 8, questionCommentId: "question-1", content: "Use staging", idempotencyKey: "execution-reply" };
  const reply = { id: "reply-1", issueId: "issue-1", questionCommentId: "question-1", content: "Use staging", authorId: "user-1", issueRevision: 9 };

  it("sends a question-scoped revision and retry key to the execution route", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ requirement: requirement(), reply }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");
    expect(await client.replyRequirement("ws/1", "req/1", input)).toMatchObject({ reply });
    const [url, init] = fetchMock.mock.calls[0] ?? [];
    expect(url).toBe("https://api.example.test/api/workspaces/ws%2F1/requirements/req%2F1/replies");
    expect(requestHeaders(init)["Idempotency-Key"]).toBe(input.idempotencyKey);
    expect(JSON.parse(init.body)).toEqual({ expectedIssueRevision: 8, questionCommentId: "question-1", content: "Use staging" });
    await expect(client.replyRequirement("ws-1", "req-1", { ...input, idempotencyKey: " " })).rejects.toThrow(/non-empty Idempotency-Key/);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it.each([{}, { requirement: requirement() }, { requirement: requirement(), reply: {} }, { requirement: {}, reply }, { requirement: requirement(), reply: { ...reply, issueRevision: "9" } }])("rejects malformed reply success envelopes %#", async (body) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(body)));
    await expect(new ApiClient("https://api.example.test").replyRequirement("ws-1", "req-1", input)).rejects.toThrow(/Malformed response/);
  });

  it("defaults absent or malformed question fields and denies an unknown reply permission", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(requirement()))
      .mockResolvedValueOnce(jsonResponse(requirement({ blockedQuestion: { commentId: "q" } })))
      .mockResolvedValueOnce(jsonResponse(requirement({ blockedQuestion: { commentId: "q", wakeupId: "w", targetUserId: "u", agentId: "a", content: "Target?", canReply: "yes" } })));
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");
    expect((await client.getRequirement("ws-1", "req-1")).blockedQuestion).toBeNull();
    expect((await client.getRequirement("ws-1", "req-1")).blockedQuestion).toBeNull();
    expect((await client.getRequirement("ws-1", "req-1")).blockedQuestion?.canReply).toBe(false);
  });
});

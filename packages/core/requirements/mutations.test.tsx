/**
 * @vitest-environment jsdom
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import { IssueSchema } from "../api/schemas";
import { RequirementDetailSchema, type RequirementApproveResponse, type RequirementDetail } from "./schemas";
import { requirementKeys } from "./queries";
import { issueKeys } from "../issues/queries";
import { useApproveRequirement, useCreateRequirement } from "./mutations";
import type { CreateRequirementRequest } from "./types";

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

function makeDetail(overrides: Record<string, unknown> = {}): RequirementDetail {
  return RequirementDetailSchema.parse({
    id: "req-1",
    workspaceId: "ws-1",
    requesterId: "user-1",
    revision: 1,
    state: "awaiting_approval",
    intakeState: "awaiting_approval",
    chatSessionId: "chat-1",
    ...overrides,
  });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function createWrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  };
}

describe("Requirement mutations", () => {
  let qc: QueryClient;

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  });

  afterEach(() => {
    qc.clear();
    vi.restoreAllMocks();
  });

  it("waits for approve before writing the authoritative detail and invalidates only exact keys", async () => {
    const initial = makeDetail();
    const approved = makeDetail({ revision: 2, state: "executing", issueId: "issue-1" });
    const response = deferred<RequirementApproveResponse>();
    const approveRequirement = vi.fn().mockReturnValue(response.promise);
    setApiInstance({ approveRequirement } as unknown as ApiClient);
    const detailKey = requirementKeys.detail("ws-1", "req-1");
    const byChatKey = requirementKeys.byChat("ws-1", "chat-1");
    qc.setQueryData(detailKey, initial);
    qc.setQueryData(byChatKey, initial);
    const invalidateSpy = vi.spyOn(qc, "invalidateQueries");
    const { result } = renderHook(() => useApproveRequirement("ws-1", "req-1"), {
      wrapper: createWrapper(qc),
    });

    let pending!: Promise<RequirementApproveResponse>;
    act(() => {
      pending = result.current.mutateAsync({
        expectedRevision: 1,
        candidateId: "candidate-1",
        candidateDigest: "sha256:candidate",
        idempotencyKey: "approve-stable-key",
      });
    });
    await waitFor(() => expect(approveRequirement).toHaveBeenCalledWith("ws-1", "req-1", {
      expectedRevision: 1,
      candidateId: "candidate-1",
      candidateDigest: "sha256:candidate",
      idempotencyKey: "approve-stable-key",
    }));

    expect(result.current.isPending).toBe(true);
    expect(qc.getQueryData(detailKey)).toBe(initial);
    expect(qc.getQueryData(byChatKey)).toBe(initial);
    expect(invalidateSpy).not.toHaveBeenCalled();

    await act(async () => {
      response.resolve({ requirement: approved, issue: IssueSchema.parse(baseIssue) });
      await pending;
    });

    expect(qc.getQueryData(detailKey)).toEqual(approved);
    expect(qc.getQueryData(byChatKey)).toEqual(approved);
    const invalidatedKeys = invalidateSpy.mock.calls.map(([filters]) => filters?.queryKey);
    expect(invalidatedKeys).toHaveLength(6);
    expect(invalidatedKeys).toContainEqual(detailKey);
    expect(invalidatedKeys).toContainEqual(byChatKey);
    expect(invalidatedKeys).toContainEqual(issueKeys.detail("ws-1", "issue-1"));
    expect(invalidatedKeys).toContainEqual(issueKeys.list("ws-1"));
    expect(invalidatedKeys).toContainEqual(issueKeys.tasks("issue-1"));
    expect(invalidatedKeys).toContainEqual(issueKeys.timeline("issue-1"));
    expect(invalidatedKeys).not.toContainEqual(requirementKeys.all("ws-1"));
  });

  it("does not optimistically create or navigate a requirement cache entry", async () => {
    const response = deferred<RequirementDetail>();
    const createRequirement = vi.fn().mockReturnValue(response.promise);
    setApiInstance({ createRequirement } as unknown as ApiClient);
    const { result } = renderHook(() => useCreateRequirement("ws-1"), {
      wrapper: createWrapper(qc),
    });
    const input: CreateRequirementRequest = {
      sourceHost: "yixiezuo",
      externalId: "42",
      sourceUrl: "https://source.example/42",
      sourceSnapshot: { title: "Requirement" },
      sourceDigest: "sha256:source",
      agentId: "agent-1",
    };

    let pending!: Promise<RequirementDetail>;
    act(() => {
      pending = result.current.mutateAsync({ ...input, idempotencyKey: "create-stable-key" });
    });
    await waitFor(() => expect(createRequirement).toHaveBeenCalledWith("ws-1", {
      ...input,
      idempotencyKey: "create-stable-key",
    }));
    expect(result.current.isPending).toBe(true);
    expect(qc.getQueryCache().getAll()).toHaveLength(0);

    const created = makeDetail({ revision: 1, state: "intake_pending", intakeState: "intake_pending" });
    await act(async () => {
      response.resolve(created);
      await pending;
    });

    expect(qc.getQueryData(requirementKeys.detail("ws-1", "req-1"))).toEqual(created);
    expect(qc.getQueryData(requirementKeys.byChat("ws-1", "chat-1"))).toEqual(created);
  });
});

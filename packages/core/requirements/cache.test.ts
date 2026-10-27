// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { chatKeys } from "../chat/queries";
import { issueKeys } from "../issues/queries";
import {
  invalidateApprovedRequirementIssue,
  invalidateOpenedRequirementChat,
} from "./cache";
import {
  type RequirementApproveResponse,
  type RequirementOpenResponse,
} from "./schemas";

const detail = {
  id: "req-1",
  workspaceId: "ws-1",
  requesterId: "user-1",
} as RequirementOpenResponse["requirement"];

const issue = {
  id: "issue-1",
} as RequirementApproveResponse["issue"];

describe("Requirement follow-up cache invalidation", () => {
  it("invalidates only the opened Chat projections", () => {
    const qc = new QueryClient();
    const invalidateSpy = vi.spyOn(qc, "invalidateQueries");
    invalidateOpenedRequirementChat(qc, "ws-1", {
      requirement: detail,
      session: { id: "chat-1" } as RequirementOpenResponse["session"],
    });

    expect(invalidateSpy.mock.calls.map(([filters]) => filters?.queryKey)).toEqual([
      chatKeys.sessions("ws-1"),
      chatKeys.session("ws-1", "chat-1"),
      chatKeys.messages("chat-1"),
      chatKeys.messagesPage("chat-1"),
      chatKeys.pendingTask("chat-1"),
    ]);
  });

  it("invalidates only the approved Issue projections", () => {
    const qc = new QueryClient();
    const invalidateSpy = vi.spyOn(qc, "invalidateQueries");
    invalidateApprovedRequirementIssue(qc, "ws-1", { requirement: detail, issue });

    expect(invalidateSpy.mock.calls.map(([filters]) => filters?.queryKey)).toEqual([
      issueKeys.detail("ws-1", "issue-1"),
      issueKeys.list("ws-1"),
      issueKeys.tasks("issue-1"),
      issueKeys.timeline("issue-1"),
    ]);
  });
});

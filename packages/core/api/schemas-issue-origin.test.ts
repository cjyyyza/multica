// @vitest-environment node
import { describe, expect, it } from "vitest";
import { IssueSchema } from "./schemas";

const baseIssue = {
  id: "issue-1",
  workspace_id: "ws-1",
  number: 1,
  identifier: "MUL-1",
  title: "Requirement issue",
  description: null,
  status: "todo",
  priority: "medium",
  assignee_type: null,
  assignee_id: null,
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

describe("IssueSchema additive origin compatibility", () => {
  it("accepts the canonical snake_case origin fields", () => {
    const parsed = IssueSchema.parse({
      ...baseIssue,
      origin_type: "requirement_chat",
      origin_id: "chat-1",
    });
    expect(parsed.origin_type).toBe("requirement_chat");
    expect(parsed.origin_id).toBe("chat-1");
  });

  it("keeps ordinary issues when old servers omit origin or send malformed additive values", () => {
    const legacy = IssueSchema.parse(baseIssue);
    expect(legacy.id).toBe("issue-1");
    expect(legacy.origin_type).toBeUndefined();
    expect(legacy.origin_id).toBeUndefined();

    const malformed = IssueSchema.parse({
      ...baseIssue,
      origin_type: 42,
      origin_id: { id: "chat-1" },
    });
    expect(malformed.id).toBe("issue-1");
    expect(malformed.origin_type).toBeUndefined();
    expect(malformed.origin_id).toBeUndefined();
  });

  it("degrades only the malformed origin field", () => {
    const parsed = IssueSchema.parse({ ...baseIssue, origin_type: "requirement_chat", origin_id: 42 });
    expect(parsed.origin_type).toBe("requirement_chat");
    expect(parsed.origin_id).toBeUndefined();
    expect(parsed.id).toBe("issue-1");
  });
});

describe("IssueSchema additive requirement projection", () => {
  it("accepts a Requirement id and a definite null projection", () => {
    expect(IssueSchema.parse({ ...baseIssue, requirement_id: "req-1" }).requirement_id).toBe("req-1");
    expect(IssueSchema.parse({ ...baseIssue, requirement_id: null }).requirement_id).toBeNull();
  });

  it("leaves requirement_id undefined when an old backend omits it", () => {
    expect(IssueSchema.parse(baseIssue).requirement_id).toBeUndefined();
  });

  it("degrades only a malformed requirement_id", () => {
    const parsed = IssueSchema.parse({
      ...baseIssue,
      origin_type: "requirement_chat",
      origin_id: "chat-1",
      requirement_id: { id: "req-1" },
    });
    expect(parsed.requirement_id).toBeUndefined();
    expect(parsed.origin_id).toBe("chat-1");
    expect(parsed.id).toBe("issue-1");
  });
});

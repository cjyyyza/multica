// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { P4Changelist } from "../types";
import { p4ChangelistSummaryEntries } from "./summary";

function changelist(overrides: Partial<P4Changelist>): P4Changelist {
  return {
    id: "cl-1",
    workspace_id: "ws-1",
    port: "perforce.example.com:1666",
    number: 795095,
    original_number: 795095,
    status: "shelved",
    client: "dj-win",
    p4_user: "yujian",
    description: "refs #12345 fix the thing",
    ticket: "12345",
    files_count: 2,
    exclusive_files: [],
    warnings: [],
    swarm_review_id: null,
    swarm_review_number: null,
    swarm_review_url: null,
    swarm_review_state: null,
    task_id: "task-1",
    link_source: "task",
    submitted_at: null,
    created_at: "",
    updated_at: "",
    ...overrides,
  };
}

describe("p4ChangelistSummaryEntries", () => {
  it("names the pending number a submit replaced", () => {
    expect(
      p4ChangelistSummaryEntries([
        changelist({ number: 795115, original_number: 795095, status: "submitted", swarm_review_number: 123 }),
      ]),
    ).toEqual([
      { number: 795115, status: "submitted", originalNumber: 795095, swarmReviewNumber: 123 },
    ]);
  });

  it("leaves out the original number while it still matches", () => {
    expect(p4ChangelistSummaryEntries([changelist({})])[0]).toEqual({
      number: 795095,
      status: "shelved",
      originalNumber: null,
      swarmReviewNumber: null,
    });
  });

  // Both would name work the reader cannot look up.
  it("skips deleted changelists and rows without a number", () => {
    expect(
      p4ChangelistSummaryEntries([
        changelist({ id: "cl-1", status: "deleted" }),
        changelist({ id: "cl-2", number: 0, original_number: 0 }),
      ]),
    ).toEqual([]);
  });

  it("ignores a review number of zero", () => {
    expect(p4ChangelistSummaryEntries([changelist({ swarm_review_number: 0 })])[0]?.swarmReviewNumber).toBeNull();
  });

  it("keeps the server's order", () => {
    const entries = p4ChangelistSummaryEntries([
      changelist({ id: "cl-2", number: 795115 }),
      changelist({ id: "cl-1", number: 795095 }),
    ]);
    expect(entries.map((entry) => entry.number)).toEqual([795115, 795095]);
  });
});

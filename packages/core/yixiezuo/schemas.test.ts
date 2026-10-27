// @vitest-environment node
import { describe, expect, it } from "vitest";
import { YixiezuoOperationSchema, YixiezuoImportEnvelopeSchema, YixiezuoPMResultSchema, YixiezuoPMTriageResultSchema, YixiezuoPMReconcileSchema, YixiezuoPMTriageCandidatesSchema } from "./schemas";

describe("manual 易协作 API boundaries", () => {
  it("preserves unconfirmed draft creation without turning it into a source preview", () => {
    expect(YixiezuoOperationSchema.parse({ id: "draft", kind: "draft", state: "unknown" }))
      .toMatchObject({ kind: "draft", state: "unknown", snapshot: null });
  });
  it("preserves a prepared review as distinct from an external publication", () => {
    expect(YixiezuoOperationSchema.parse({ id: "review", kind: "review", state: "succeeded" }).kind).toBe("review");
  });
  it("degrades unknown operation states without reporting success", () => {
    expect(YixiezuoOperationSchema.parse({ id: "op", kind: "preview", state: "new_state" }).state).toBe("unknown");
    expect(YixiezuoOperationSchema.parse({
      id: "reconcile", kind: "reconcile", state: "needs_reconcile",
      target_operation_id: "publish-1", request_hash: "hash-1",
    })).toMatchObject({ kind: "reconcile", state: "needs_reconcile", target_operation_id: "publish-1", request_hash: "hash-1" });
  });
  it("rejects malformed source material and executable URLs", () => {
    expect(YixiezuoOperationSchema.safeParse({ id: "op", state: "succeeded", snapshot: { title: "x", digest: "hash", source: { host: "x", id: "1", url: "javascript:alert(1)" } } }).success).toBe(false);
  });
  it("requires an operation identity before a mutation can proceed", () => {
    expect(YixiezuoOperationSchema.safeParse({ state: "succeeded" }).success).toBe(false);
  });
  it("treats an absent import as absent", () => {
    expect(YixiezuoImportEnvelopeSchema.parse({})).toEqual({ import: null });
  });
  it("accepts mirrored attachments served from the local uploads route", () => {
    const attachment = { id: "104221", name: "acceptance.txt", url: "https://example.pm.netease.com/attachments/104221/acceptance.txt",
      local_id: "attachment-1", local_url: "/uploads/workspaces/workspace-1/attachment-1.txt", content_type: "text/plain" };
    const operation = { id: "preview-1", kind: "preview", state: "succeeded", snapshot: {
      source: { host: "example.pm.netease.com", id: "108402", url: "https://example.pm.netease.com/issues/108402" },
      title: "Source with attachment", digest: "digest-1", attachments: [attachment],
    } };
    expect(YixiezuoOperationSchema.parse(operation).snapshot?.attachments[0]).toEqual(attachment);
    for (const local_url of ["javascript:alert(1)", "//evil.example/file", "/other/file"]) {
      expect(YixiezuoOperationSchema.safeParse({ ...operation, snapshot: { ...operation.snapshot, attachments: [{ ...attachment, local_url }] } }).success).toBe(false);
    }
  });
});

describe("read-only PM API boundaries", () => {
  const id = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
  it("does not turn unknown or missing result states into success", () => {
    expect(YixiezuoPMResultSchema.parse({ state: "future_state", warnings: null, changes: null }).state).toBe("failed");
    expect(YixiezuoPMTriageResultSchema.parse({ issue_id: id }).state).toBe("failed");
  });
  it("requires durable identities on successful sync and notification receipts", () => {
    expect(YixiezuoPMResultSchema.safeParse({ external_id: "12", state: "created" }).success).toBe(false);
    expect(YixiezuoPMTriageResultSchema.safeParse({ issue_id: id, state: "notified" }).success).toBe(false);
    expect(YixiezuoPMResultSchema.parse({ external_id: "12", issue_id: id, state: "unchanged" }).changes).toEqual([]);
  });
  it("rejects executable links, malformed candidates and missing scan lists", () => {
    expect(YixiezuoPMResultSchema.safeParse({ state: "failed", url: "javascript:alert(1)" }).success).toBe(false);
    expect(YixiezuoPMTriageCandidatesSchema.safeParse({ issues: [{ id: "display-name" }] }).success).toBe(false);
    expect(YixiezuoPMReconcileSchema.safeParse({}).success).toBe(false);
  });
});

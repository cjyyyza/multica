// @vitest-environment node
import { describe, expect, it } from "vitest";
import { YixiezuoOperationSchema, YixiezuoImportEnvelopeSchema, YixiezuoPMResultSchema, YixiezuoPMTriageResultSchema, YixiezuoPMReconcileSchema, YixiezuoPMTriageCandidatesSchema } from "./schemas";

describe("manual 易协作 API boundaries", () => {
  it("preserves a prepared review as distinct from an external publication", () => {
    expect(YixiezuoOperationSchema.parse({ id: "review", kind: "review", state: "succeeded" }).kind).toBe("review");
  });
  it("degrades unknown operation states without reporting success", () => {
    expect(YixiezuoOperationSchema.parse({ id: "op", kind: "preview", state: "new_state" }).state).toBe("unknown");
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

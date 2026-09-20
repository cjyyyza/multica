// @vitest-environment node
import { describe, expect, it } from "vitest";
import { YixiezuoOperationSchema, YixiezuoImportEnvelopeSchema } from "./schemas";

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

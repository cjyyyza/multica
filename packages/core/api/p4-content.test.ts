// @vitest-environment node
import { describe, expect, it } from "vitest";
import { parseWithFallback } from "./schema";
import { EMPTY_P4_CONTENT_REQUEST, P4ContentRequestSchema } from "./schemas";

describe("P4 content response compatibility", () => {
  it("keeps unknown sources and missing optional fields renderable", () => {
    const request = P4ContentRequestSchema.parse({ id: "request-1", status: "completed", result: { source: "future-source", files: [{ depot_path: "//depot/A.cpp", action: "future-action" }] } });
    expect(request.result).toMatchObject({ source: "unknown", diff: "", binary: false, truncated: false });
    expect(request.result?.files[0]).toMatchObject({ action: "future-action", type: "", revision: 0 });
  });

  it.each([null, [], { status: "completed" }, { id: 4, status: "pending" }])("degrades malformed request %j to a failure", (raw) => {
    expect(parseWithFallback(raw, P4ContentRequestSchema, EMPTY_P4_CONTENT_REQUEST, { endpoint: "p4-content-test" })).toEqual(EMPTY_P4_CONTENT_REQUEST);
  });

  it("does not poll an unknown status or claim a malformed result succeeded", () => {
    expect(P4ContentRequestSchema.parse({ id: "r", status: "future" }).status).toBe("failed");
    expect(P4ContentRequestSchema.parse({ id: "r", status: "completed", result: "invalid" }).result).toBeNull();
  });
});

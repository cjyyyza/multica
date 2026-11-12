// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { p4ChangelistContentOptions, p4Keys } from "./queries";

const api = vi.hoisted(() => ({ requestIssueP4Content: vi.fn(), getIssueP4Content: vi.fn() }));
vi.mock("../api", () => ({ api }));

afterEach(() => { vi.useRealTimers(); vi.resetAllMocks(); });

describe("changelist content query", () => {
  it("waits for the daemon result and scopes the cache to workspace, CL and file", async () => {
    vi.useFakeTimers();
    api.requestIssueP4Content.mockResolvedValue({ id: "r", status: "pending" });
    api.getIssueP4Content.mockResolvedValueOnce({ id: "r", status: "running" }).mockResolvedValueOnce({ id: "r", status: "completed", result: { source: "shelf", diff: "+new" } });
    const qc = new QueryClient();
    const pending = qc.fetchQuery(p4ChangelistContentOptions("ws", "issue", "cl", "//depot/A.cpp"));
    await vi.advanceTimersByTimeAsync(1000);
    expect(await pending).toEqual({ source: "shelf", diff: "+new" });
    expect(api.requestIssueP4Content).toHaveBeenCalledWith("issue", "cl", "//depot/A.cpp", expect.any(AbortSignal));
    expect(p4Keys.content("other", "issue", "cl", "//depot/A.cpp")).not.toEqual(p4Keys.content("ws", "issue", "cl", "//depot/A.cpp"));
    qc.clear();
  });

  it("surfaces a failed result instead of displaying an empty diff", async () => {
    api.requestIssueP4Content.mockResolvedValue({ id: "r", status: "failed", error: "P4 login expired" });
    const qc = new QueryClient();
    await expect(qc.fetchQuery(p4ChangelistContentOptions("ws", "issue", "cl"))).rejects.toThrow("P4 login expired");
    qc.clear();
  });
});

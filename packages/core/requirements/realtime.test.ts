// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { applyRequirementEventToCache } from "./realtime";
import { requirementKeys } from "./queries";
import { RequirementDetailSchema, type RequirementDetail } from "./schemas";

function detail(revision: number): RequirementDetail {
  return RequirementDetailSchema.parse({
    id: "req-1",
    workspaceId: "ws-1",
    requesterId: "user-1",
    revision,
    chatSessionId: "chat-1",
  });
}

describe("Requirement realtime cache", () => {
  it("invalidates detail and byChat without patching full-detail payloads", () => {
    const qc = new QueryClient();
    const cached = detail(1);
    qc.setQueryData(requirementKeys.detail("ws-1", "req-1"), cached);
    qc.setQueryData(requirementKeys.byChat("ws-1", "chat-1"), cached);
    const invalidateSpy = vi.spyOn(qc, "invalidateQueries");

    applyRequirementEventToCache(qc, "ws-1", {
      workspaceId: "ws-1",
      requirementId: "req-1",
      revision: 2,
      chatSessionId: "chat-1",
    });

    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: requirementKeys.detail("ws-1", "req-1") });
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: requirementKeys.byChat("ws-1", "chat-1") });
    expect(qc.getQueryData(requirementKeys.detail("ws-1", "req-1"))).toBe(cached);
    expect(qc.getQueryData(requirementKeys.byChat("ws-1", "chat-1"))).toBe(cached);
  });

  it("invalidates only detail when the event has no chat pointer", () => {
    const qc = new QueryClient();
    const invalidateSpy = vi.spyOn(qc, "invalidateQueries");

    applyRequirementEventToCache(qc, "ws-1", {
      workspaceId: "ws-1",
      requirementId: "req-1",
      revision: 2,
    });

    expect(invalidateSpy).toHaveBeenCalledTimes(1);
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: requirementKeys.detail("ws-1", "req-1") });
  });

  it("invalidates malformed payloads instead of fabricating a detail", () => {
    const qc = new QueryClient();
    const invalidateSpy = vi.spyOn(qc, "invalidateQueries");

    applyRequirementEventToCache(qc, "ws-1", {
      workspaceId: "ws-1",
      requirementId: "",
      revision: 2,
    });

    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: requirementKeys.all("ws-1") });
    expect(qc.getQueryData(requirementKeys.detail("ws-1", "broken"))).toBeUndefined();
  });

  it("ignores an event from another workspace", () => {
    const qc = new QueryClient();
    const invalidateSpy = vi.spyOn(qc, "invalidateQueries");

    applyRequirementEventToCache(qc, "ws-1", {
      workspaceId: "ws-2",
      requirementId: "req-1",
      revision: 2,
      chatSessionId: "chat-1",
    });

    expect(invalidateSpy).not.toHaveBeenCalled();
  });
});

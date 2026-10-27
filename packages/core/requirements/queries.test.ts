// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import {
  requirementByChatOptions,
  requirementDetailOptions,
  requirementKeys,
} from "./queries";
import { RequirementDetailSchema } from "./schemas";

const detail = RequirementDetailSchema.parse({
  id: "req-1",
  workspaceId: "ws-1",
  requesterId: "user-1",
});

describe("Requirement query options", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("scopes detail and byChat keys by workspace and calls the matching API", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const getRequirement = vi.fn().mockResolvedValue(detail);
    const getRequirementByChat = vi.fn().mockResolvedValue(null);
    setApiInstance({ getRequirement, getRequirementByChat } as unknown as ApiClient);

    await expect(qc.fetchQuery(requirementDetailOptions("ws-1", "req-1"))).resolves.toEqual(detail);
    await expect(qc.fetchQuery(requirementByChatOptions("ws-1", "chat-1"))).resolves.toBeNull();

    expect(requirementKeys.detail("ws-1", "req-1")).toEqual(["requirements", "ws-1", "detail", "req-1"]);
    expect(requirementKeys.byChat("ws-1", "chat-1")).toEqual(["requirements", "ws-1", "byChat", "chat-1"]);
    expect(getRequirement).toHaveBeenCalledWith("ws-1", "req-1");
    expect(getRequirementByChat).toHaveBeenCalledWith("ws-1", "chat-1");
    expect(requirementDetailOptions("", "req-1").enabled).toBe(false);
    expect(requirementDetailOptions("ws-1", "").enabled).toBe(false);
    expect(requirementByChatOptions("ws-1", "").enabled).toBe(false);
    qc.clear();
  });
});

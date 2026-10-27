/**
 * @vitest-environment jsdom
 */
import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider, QueryObserver } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import type { WSClient } from "../api/ws-client";
import { requirementKeys } from "../requirements/queries";
import { RequirementDetailSchema } from "../requirements/schemas";
import type { RequirementEventPayload } from "../types/events";
import { useRealtimeSync, type RealtimeSyncStores } from "./use-realtime-sync";

vi.mock("../search-index/instance", () => ({
  forgetLocalSearchIndex: vi.fn(async () => undefined),
}));

vi.mock("../platform/workspace-storage", () => ({
  getCurrentWsId: () => "ws-1",
  getCurrentSlug: () => "test-ws",
  createWorkspaceAwareStorage: (adapter: unknown) => adapter,
  registerForWorkspaceRehydration: () => {},
}));

vi.mock("../paths", () => ({
  useHasOnboarded: () => true,
  resolvePostAuthDestination: () => "/",
}));

function createMockWs(): WSClient {
  return {
    on: vi.fn(() => () => {}),
    onAny: vi.fn(() => () => {}),
    onReconnect: vi.fn(() => () => {}),
  } as unknown as WSClient;
}

function createStores(): RealtimeSyncStores {
  return {
    authStore: Object.assign(() => ({}), {
      getState: () => ({ user: { id: "u1" } }),
      subscribe: () => () => {},
      setState: () => {},
      destroy: () => {},
    }),
  } as unknown as RealtimeSyncStores;
}

describe("useRealtimeSync Requirement events", () => {
  it.each(["task:running", "task:completed", "task:failed", "task:cancelled", "task:progress", "task:message"])("refreshes Requirement projections only for lifecycle events: %s", async (type) => {
    vi.useFakeTimers();
    try {
      const qc = new QueryClient();
      const key = requirementKeys.detail("ws-1", "req-1");
      const cached = RequirementDetailSchema.parse({ id: "req-1", workspaceId: "ws-1", requesterId: "u1", revision: 4, state: "executing", issueId: "issue-1" });
      qc.setQueryData(key, cached);
      const read = vi.fn().mockResolvedValue({ ...cached, state: "in_review", permissions: { ...cached.permissions, canAccept: true } });
      const observer = new QueryObserver(qc, { queryKey: key, queryFn: read, staleTime: Infinity });
      const unsubscribe = observer.subscribe(() => {});
      const invalidate = vi.spyOn(qc, "invalidateQueries");
      const ws = createMockWs();
      function Wrapper({ children }: { children: ReactNode }) {
        return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
      }
      const view = renderHook(() => useRealtimeSync(ws, createStores()), { wrapper: Wrapper });
      invalidate.mockClear();
      const onAny = vi.mocked(ws.onAny).mock.calls[0]![0];
      act(() => onAny({ type, payload: { task_id: "task-1", issue_id: "issue-1" } } as Parameters<typeof onAny>[0]));
      await act(async () => { await vi.advanceTimersByTimeAsync(150); });
      if (type === "task:progress" || type === "task:message") {
        expect(read).not.toHaveBeenCalled();
        expect(qc.getQueryData(key)).toBe(cached);
      } else {
        expect(read).toHaveBeenCalledTimes(1);
        expect(qc.getQueryData(key)).toMatchObject({ state: "in_review", revision: 4, permissions: { canAccept: true } });
      }
      expect(invalidate).not.toHaveBeenCalledWith({ queryKey: requirementKeys.all("ws-2") });
      view.unmount();
      unsubscribe();
      observer.destroy();
      qc.clear();
    } finally {
      vi.useRealTimers();
    }
  });
  it("subscribes to created and updated and invalidates authorized readbacks", () => {
    const qc = new QueryClient();
    const invalidateSpy = vi.spyOn(qc, "invalidateQueries");
    const ws = createMockWs();
    function Wrapper({ children }: { children: ReactNode }) {
      return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
    }
    renderHook(() => useRealtimeSync(ws, createStores()), { wrapper: Wrapper });

    const registrations = vi.mocked(ws.on).mock.calls;
    expect(registrations.map(([type]) => type)).toEqual(expect.arrayContaining([
      "requirement:created",
      "requirement:updated",
    ]));
    const registration = registrations.find(([type]) => type === "requirement:updated");
    const handler = registration?.[1] as (payload: RequirementEventPayload) => void;

    act(() => handler({
      workspaceId: "ws-1",
      requirementId: "req-1",
      revision: 3,
      chatSessionId: "chat-1",
    }));

    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: requirementKeys.detail("ws-1", "req-1") });
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: requirementKeys.byChat("ws-1", "chat-1") });
    expect(qc.getQueryData(requirementKeys.detail("ws-1", "req-1"))).toBeUndefined();
  });
});

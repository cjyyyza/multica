"use client";

import { useEffect, useRef } from "react";
import { createSafeId } from "@multica/core/utils";
import {
  useOpenRequirement,
  type RequirementDetail,
  type RequirementOpenResponse,
} from "@multica/core/requirements";

export function useRequirementOpenIntent({
  wsId,
  requirementId,
  detail,
  isActive,
  onOpened,
}: {
  wsId: string;
  requirementId: string | null;
  detail: RequirementDetail | null;
  isActive: boolean;
  onOpened: (sessionId: string) => void;
}) {
  const open = useOpenRequirement(wsId, requirementId ?? "");
  const consumed = useRef<string | null>(null);
  const superseded = useRef<string | null>(null);
  const inFlight = useRef<Promise<RequirementOpenResponse> | null>(null);
  const mounted = useRef(false);
  const latest = useRef({ requirementId, isActive, onOpened });
  latest.current = { requirementId, isActive, onOpened };

  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; };
  }, []);

  useEffect(() => {
    if (!isActive) return;
    if (!requirementId) {
      consumed.current = null;
      superseded.current = null;
      inFlight.current = null;
      return;
    }
    if (superseded.current === requirementId || consumed.current === requirementId) return;
    if (!detail) return;

    if (detail.chatSessionId) {
      consumed.current = requirementId;
      latest.current.onOpened(detail.chatSessionId);
      return;
    }
    if (inFlight.current) return;
    consumed.current = requirementId;
    if (detail.revision === undefined || !detail.permissions.canOpen || detail.issueId || detail.cancelledAt) return;

    const promise = open.mutateAsync({
      expectedRevision: detail.revision,
      idempotencyKey: createSafeId(),
    });
    inFlight.current = promise;
    void promise.then((result) => {
      inFlight.current = null;
      if (!mounted.current || !latest.current.isActive) return;
      if (latest.current.requirementId !== requirementId || superseded.current === requirementId) return;
      latest.current.onOpened(result.session.id);
    }).catch(() => {
      inFlight.current = null;
    });
    // The consumed and in-flight refs intentionally bridge StrictMode's effect replay.
    // eslint-disable-next-line react-hooks/exhaustive-deps -- open only after this intent's detail arrives
  }, [requirementId, detail, isActive]);

  return {
    error: open.error,
    isPending: open.isPending || !!inFlight.current,
    supersede: () => {
      if (requirementId) superseded.current = requirementId;
    },
  };
}

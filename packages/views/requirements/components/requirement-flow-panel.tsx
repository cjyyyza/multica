"use client";

import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ChevronRight } from "lucide-react";
import { requirementKeys } from "@multica/core/requirements";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { RequirementCard } from "./requirement-card";
import { RequirementSyncStatus } from "./requirement-sync-status";
import { RequirementStageRail, type RequirementFlowContext } from "./requirement-stage-rail";
import type { RequirementSurfaceState } from "../use-requirement-surface";
import { useT } from "../../i18n";

type RequirementFlowPanelSurface = RequirementSurfaceState & {
  /**
   * Retry a failed read from the owner of the query observer. The projected
   * surface state has no observer handle, so the panel invalidates this
   * workspace's Requirement queries when it is absent.
   */
  refetch?: () => void | Promise<unknown>;
};

export function RequirementFlowPanel({
  wsId,
  surface,
  className,
  context = "chat",
  isRunning = false,
}: {
  wsId: string;
  surface: RequirementFlowPanelSurface;
  className?: string;
  context?: RequirementFlowContext;
  /** A plan turn is running on this Requirement chat. Issue surfaces pass false. */
  isRunning?: boolean;
}) {
  const { t } = useT("requirements");
  const { t: tUi } = useT("ui");
  const qc = useQueryClient();
  const [detailsOpen, setDetailsOpen] = useState(false);
  if (surface.isLoading) return null;
  if (surface.error) {
    const retry = () => {
      if (surface.refetch) {
        void surface.refetch();
        return;
      }
      void qc.invalidateQueries({ queryKey: requirementKeys.all(wsId) });
    };
    return (
      <div className={cn("flex flex-wrap items-center gap-2", className)}>
        <p role="alert" className="text-caption text-destructive">{surface.error.message || t(($) => $.panel.load_failed)}</p>
        <Button type="button" size="sm" variant="outline" onClick={retry}>
          {tUi(($) => $.error_boundary.try_again)}
        </Button>
      </div>
    );
  }
  const detail = surface.detail;
  if (!detail) return null;

  return (
    <div className={cn("space-y-3", className)} data-testid="requirement-flow-panel">
      <RequirementStageRail
        wsId={wsId}
        detail={detail}
        canCompose={surface.canCompose}
        isRunning={isRunning}
        context={context}
      />
      <section className="rounded-lg border border-border bg-background">
        <button
          type="button"
          aria-expanded={detailsOpen}
          onClick={() => setDetailsOpen((open) => !open)}
          className="flex w-full items-center gap-1 px-3 py-2 text-caption font-medium text-muted-foreground outline-none transition-colors hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring"
        >
          <ChevronRight className={cn("size-3.5 transition-transform", detailsOpen && "rotate-90")} />
          {t(($) => $.flow.details)}
        </button>
        {detailsOpen && (
          <div className="space-y-3 border-t border-border p-3">
            <RequirementCard wsId={wsId} detail={detail} />
            <RequirementSyncStatus wsId={wsId} detail={detail} />
          </div>
        )}
      </section>
    </div>
  );
}

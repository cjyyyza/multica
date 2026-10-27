"use client";

import { useQueryClient } from "@tanstack/react-query";
import { requirementKeys } from "@multica/core/requirements";
import { AppLink } from "../../navigation";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { RequirementCard } from "./requirement-card";
import { RequirementApprovalCard } from "./requirement-approval-card";
import { RequirementReviewCard } from "./requirement-review-card";
import { RequirementSyncStatus } from "./requirement-sync-status";
import { RequirementIntakeDialog } from "./requirement-intake-dialog";
import type { RequirementSurfaceState } from "../use-requirement-surface";
import { useT } from "../../i18n";
import { cn } from "@multica/ui/lib/utils";

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
}: {
  wsId: string;
  surface: RequirementFlowPanelSurface;
  className?: string;
}) {
  const { t } = useT("requirements");
  const { t: tUi } = useT("ui");
  const qc = useQueryClient();
  const paths = useWorkspacePaths();
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
  const cancelled = detail.state === "cancelled" || !!detail.cancelledAt;
  const closed = cancelled || !!detail.issueId;

  return (
    <div className={cn("space-y-3", className)} data-testid="requirement-flow-panel">
      <RequirementCard wsId={wsId} detail={detail} />
      {!closed && detail.latestCandidate && <RequirementApprovalCard wsId={wsId} detail={detail} />}
      <RequirementReviewCard wsId={wsId} detail={detail} />
      <RequirementSyncStatus wsId={wsId} detail={detail} />
      {closed && (
        <div className="space-y-2 rounded-lg border border-border p-3">
          <p className="text-caption text-muted-foreground">{t(($) => $.panel.closed)}</p>
          <div className="flex flex-wrap gap-2">
            {detail.issueId && (
              <AppLink href={paths.issueDetail(detail.issueId)} className="text-caption underline">
                {t(($) => $.panel.open_issue)}
              </AppLink>
            )}
            <RequirementIntakeDialog />
          </div>
        </div>
      )}
    </div>
  );
}

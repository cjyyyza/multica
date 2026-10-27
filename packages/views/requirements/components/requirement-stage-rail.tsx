"use client";

import { useState } from "react";
import { Check, ChevronRight, MoreHorizontal, Plus } from "lucide-react";
import type { RequirementDetail } from "@multica/core/requirements";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { cn } from "@multica/ui/lib/utils";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";
import { RequirementApprovalAction } from "./requirement-approval-card";
import { RequirementCancelDialog, RequirementReviewCard } from "./requirement-review-card";
import { RequirementIntakeDialog } from "./requirement-intake-dialog";

export type RequirementFlowContext = "chat" | "issue";

const STAGES = ["source", "clarify", "approve", "execute", "review"] as const;

const STAGE_BY_STATE: Record<RequirementDetail["state"], number> = {
  intake_pending: 1,
  clarifying: 1,
  awaiting_approval: 2,
  executing: 3,
  blocked: 3,
  in_review: 4,
  done: STAGES.length,
  cancelled: -1,
  unknown: -1,
};

const SYNC_ISSUE_STATES = new Set(["failed", "unknown", "needs_reconcile"]);

function hasSyncIssue(detail: RequirementDetail): boolean {
  return !!detail.lastError ||
    detail.operations.some((operation) => SYNC_ISSUE_STATES.has(operation.state)) ||
    detail.deliveries.some((delivery) => SYNC_ISSUE_STATES.has(delivery.state));
}

function syncIssueDetail(detail: RequirementDetail): string | null {
  if (detail.lastError) return detail.lastError;
  const operation = detail.operations.find((item) => SYNC_ISSUE_STATES.has(item.state));
  if (operation?.error) return operation.error;
  const delivery = detail.deliveries.find((item) => SYNC_ISSUE_STATES.has(item.state));
  return delivery?.lastError ?? null;
}

function cancelRevisionUnavailable(detail: RequirementDetail): boolean {
  return detail.issueId ? detail.issue?.revision === undefined : detail.revision === undefined;
}

/**
 * One sticky rail for a Requirement surface: current stage, next action, at
 * most one primary control, and the only visible sync alert. Everything that
 * explains how the requirement got here lives in the panel's Details disclosure.
 */
export function RequirementStageRail({
  wsId,
  detail,
  canCompose,
  isRunning,
  context,
}: {
  wsId: string;
  detail: RequirementDetail;
  canCompose: boolean;
  isRunning: boolean;
  context: RequirementFlowContext;
}) {
  const { t } = useT("requirements");
  const paths = useWorkspacePaths();
  const [cancelOpen, setCancelOpen] = useState(false);
  const [intakeOpen, setIntakeOpen] = useState(false);

  const cancelled = detail.state === "cancelled" || !!detail.cancelledAt;
  const stage = cancelled ? -1 : STAGE_BY_STATE[detail.state];
  const sourceCreated = !!detail.sourceUrl || !!detail.intakeOperationId;
  const planTurn = isRunning && (
    detail.state === "intake_pending" ||
    detail.state === "clarifying" ||
    detail.state === "awaiting_approval"
  );

  let nextAction: string;
  if (cancelled) {
    nextAction = t(($) => $.flow.next.cancelled);
  } else if (detail.state === "intake_pending" || detail.state === "clarifying") {
    nextAction = planTurn
      ? t(($) => $.flow.next.working)
      : canCompose
        ? t(($) => $.flow.next.clarify)
        : t(($) => $.flow.next.clarify_viewer);
  } else if (detail.state === "awaiting_approval") {
    nextAction = planTurn
      ? t(($) => $.flow.next.working)
      : !detail.latestCandidate
        ? t(($) => $.flow.next.awaiting_candidate)
        : detail.permissions.canApprove
          ? t(($) => $.flow.next.approve)
          : t(($) => $.flow.next.awaiting_approver);
  } else {
    switch (detail.state) {
      case "executing":
        nextAction = t(($) => $.flow.next.execute);
        break;
      case "blocked":
        nextAction = t(($) => $.flow.next.blocked);
        break;
      case "in_review":
        nextAction = t(($) => $.flow.next.review);
        break;
      case "done":
        nextAction = t(($) => $.flow.next.done);
        break;
      case "cancelled":
        nextAction = t(($) => $.flow.next.cancelled);
        break;
      default:
        nextAction = t(($) => $.flow.next.unknown);
        break;
    }
  }

  const issueHref = detail.issueId ? paths.issueDetail(detail.issueId) : null;
  const openIssue = issueHref && context === "chat" && !cancelled && (detail.state === "executing" || detail.state === "blocked")
    ? <Button render={<AppLink href={issueHref} />}>{t(($) => $.panel.open_issue)}</Button>
    : null;
  const approval = !cancelled && detail.state === "awaiting_approval" && !planTurn && detail.permissions.canApprove && detail.latestCandidate
    ? <RequirementApprovalAction wsId={wsId} detail={detail} />
    : null;
  const canCancel = detail.permissions.canCancel && !cancelled;
  const syncAlert = hasSyncIssue(detail);
  const syncDetail = syncAlert ? syncIssueDetail(detail) : null;
  const stageLabels = [
    t(($) => $.flow.progress.source),
    t(($) => $.flow.progress.clarify),
    t(($) => $.flow.progress.approve),
    t(($) => $.flow.progress.execute),
    t(($) => $.flow.progress.review),
  ];

  const moreMenu = (
    <DropdownMenu>
      <DropdownMenuTrigger render={<Button type="button" size="sm" variant="outline" />}>
        <MoreHorizontal className="size-4" />
        {t(($) => $.flow.more)}
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-auto min-w-44">
        <DropdownMenuItem onClick={() => setIntakeOpen(true)}>
          <Plus className="size-3.5" />
          {t(($) => $.intake.title)}
        </DropdownMenuItem>
        {canCancel && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              variant="destructive"
              disabled={cancelRevisionUnavailable(detail)}
              onClick={() => setCancelOpen(true)}
            >
              {t(($) => $.review.cancel)}
            </DropdownMenuItem>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );

  const reviewActions = !cancelled && detail.state === "in_review"
    ? <RequirementReviewCard wsId={wsId} detail={detail} variant="rail" trailing={moreMenu} />
    : null;

  return (
    <div
      role="status"
      aria-label={t(($) => $.flow.title)}
      className="sticky top-0 z-10 space-y-2 rounded-lg border border-border bg-background p-3"
    >
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span className="text-caption text-muted-foreground">{t(($) => $.flow.title)}</span>
        <span className="text-body font-medium">{t(($) => $.states[cancelled ? "cancelled" : detail.state])}</span>
      </div>
      <p className="text-caption text-muted-foreground">{nextAction}</p>

      <ol className="flex flex-wrap items-center gap-x-1.5 gap-y-1" aria-label={t(($) => $.flow.progress.label)}>
        {STAGES.map((name, index) => {
          const done = index === 0 ? sourceCreated : index < stage;
          const current = index === stage;
          return (
            <li
              key={name}
              aria-current={current ? "step" : undefined}
              className="flex items-center gap-1.5 text-micro"
            >
              <span
                aria-hidden
                className={cn(
                  "flex size-4 shrink-0 items-center justify-center rounded-full border text-[0.625rem] leading-none",
                  done && "border-primary bg-primary text-primary-foreground",
                  current && "border-primary font-medium text-primary",
                  !done && !current && "border-border text-muted-foreground",
                )}
              >
                {done ? <Check className="size-2.5" /> : index + 1}
              </span>
              <span className={cn(
                "whitespace-nowrap",
                done && "text-muted-foreground",
                current && "font-medium text-foreground",
                !done && !current && "text-muted-foreground",
              )}>
                {stageLabels[index]}
              </span>
              {index < STAGES.length - 1 && <ChevronRight aria-hidden className="size-3 shrink-0 text-faint-foreground" />}
            </li>
          );
        })}
      </ol>

      {syncAlert && (
        <p role="alert" data-testid="requirement-sync-alert" className="break-words text-caption text-destructive">
          {syncDetail
            ? t(($) => $.flow.sync_alert_detail, { error: syncDetail })
            : t(($) => $.flow.sync_alert)}
        </p>
      )}

      {reviewActions ?? (
        <div className="flex flex-wrap items-center gap-2">
          {approval ?? openIssue}
          {moreMenu}
        </div>
      )}

      {issueHref && context === "chat" && (detail.state === "done" || cancelled) && (
        <AppLink className="text-caption underline" href={issueHref}>{t(($) => $.panel.open_issue)}</AppLink>
      )}

      <RequirementCancelDialog wsId={wsId} detail={detail} open={cancelOpen} onOpenChange={setCancelOpen} />
      <RequirementIntakeDialog open={intakeOpen} onOpenChange={setIntakeOpen} showTrigger={false} />
    </div>
  );
}

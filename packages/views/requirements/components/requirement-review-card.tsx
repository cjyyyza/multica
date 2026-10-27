"use client";

import { useId, useRef, useState } from "react";
import { createSafeId } from "@multica/core/utils";
import {
  useAcceptRequirement,
  useCancelRequirement,
  useRejectRequirement,
  parseYixiezuoRequirementSnapshot,
  type RequirementDetail,
} from "@multica/core/requirements";
import { Button } from "@multica/ui/components/ui/button";
import { Label } from "@multica/ui/components/ui/label";
import { Textarea } from "@multica/ui/components/ui/textarea";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { useT } from "../../i18n";

export function RequirementReviewCard({ wsId, detail }: { wsId: string; detail: RequirementDetail }) {
  const { t } = useT("requirements");
  const accept = useAcceptRequirement(wsId, detail.id);
  const reject = useRejectRequirement(wsId, detail.id);
  const cancel = useCancelRequirement(wsId, detail.id);
  const acceptAttempt = useRef<{ requirementId: string; issueRevision: number; summary: string; statusName: string; key: string } | null>(null);
  const [acceptReview, setAcceptReview] = useState<{ issueRevision: number; summary: string; statusName: string } | null>(null);
  const acceptFormId = useId();
  const rejectKey = useRef<string | null>(null);
  const cancelKey = useRef<string | null>(null);
  const [rejecting, setRejecting] = useState(false);
  const [reason, setReason] = useState("");
  const [reasonError, setReasonError] = useState(false);
  const [cancelOpen, setCancelOpen] = useState(false);
  const issueRevision = detail.issue?.revision;
  const inReview = detail.state === "in_review";
  const cancelled = detail.state === "cancelled" || !!detail.cancelledAt;
  const cancelRevisionUnavailable = detail.issueId
    ? issueRevision === undefined
    : detail.revision === undefined;
  const error = reject.error;
  const source = parseYixiezuoRequirementSnapshot(detail.sourceSnapshot);
  const acceptStale = !!acceptReview && issueRevision !== acceptReview.issueRevision;

  const beginAccept = () => {
    if (issueRevision === undefined) return;
    const result = detail.run?.result;
    const output = result && typeof result === "object" && "output" in result && typeof result.output === "string"
      ? result.output
      : "";
    setAcceptReview({ issueRevision, summary: output.length <= 20000 ? output : "", statusName: "" });
    accept.reset();
  };

  const submitAccept = async () => {
    if (!acceptReview || acceptStale || !acceptReview.summary.trim()) return;
    const summary = acceptReview.summary.trim();
    const { issueRevision: reviewedRevision, statusName } = acceptReview;
    const previous = acceptAttempt.current;
    const key = previous?.requirementId === detail.id && previous.issueRevision === reviewedRevision && previous.summary === summary && previous.statusName === statusName
      ? previous.key
      : createSafeId();
    acceptAttempt.current = { requirementId: detail.id, issueRevision: reviewedRevision, summary, statusName, key };
    try {
      await accept.mutateAsync({ expectedIssueRevision: reviewedRevision, summary, statusName, idempotencyKey: key });
      acceptAttempt.current = null;
      setAcceptReview(null);
    } catch {
      // A lost response may have committed; retry the same action with the same key.
    }
  };

  const submitReject = async () => {
    const trimmed = reason.trim();
    if (!trimmed || issueRevision === undefined) {
      setReasonError(!trimmed);
      return;
    }
    const key = rejectKey.current ?? createSafeId();
    rejectKey.current = key;
    try {
      await reject.mutateAsync({
        expectedIssueRevision: issueRevision,
        reason: trimmed,
        idempotencyKey: key,
      });
      rejectKey.current = null;
      setReason("");
      setRejecting(false);
    } catch {
      // Keep the reason and key so an explicit retry reuses the same action.
    }
  };

  const confirmCancel = async () => {
    const revision = detail.issueId
      ? issueRevision !== undefined
        ? { expectedIssueRevision: issueRevision }
        : null
      : detail.revision !== undefined
        ? { expectedRevision: detail.revision }
        : null;
    if (!revision) return;
    const key = cancelKey.current ?? createSafeId();
    cancelKey.current = key;
    try {
      await cancel.mutateAsync({
        ...revision,
        idempotencyKey: key,
      });
      cancelKey.current = null;
      setCancelOpen(false);
    } catch {
      // Cancellation stays open until the server confirms it; a retry reuses the key.
    }
  };

  return (
    <section aria-label={t(($) => $.review.title)} className="space-y-3 rounded-lg border border-border p-3">
      <h3 className="text-body font-medium">{t(($) => $.review.title)}</h3>
      <div className="flex flex-wrap gap-2">
        {inReview && detail.permissions.canAccept && (
          <Button
            type="button"
            disabled={accept.isPending || issueRevision === undefined}
            aria-busy={accept.isPending}
            onClick={beginAccept}
          >
            {t(($) => $.review.accept)}
          </Button>
        )}
        {inReview && detail.permissions.canReject && !rejecting && (
          <Button type="button" variant="outline" disabled={issueRevision === undefined} onClick={() => {
            rejectKey.current = createSafeId();
            reject.reset();
            setRejecting(true);
          }}>
            {t(($) => $.review.reject)}
          </Button>
        )}
        {!cancelled && detail.permissions.canCancel && (
          <Button type="button" variant="outline" disabled={cancelRevisionUnavailable} onClick={() => {
            cancelKey.current = createSafeId();
            cancel.reset();
            setCancelOpen(true);
          }}>
            {t(($) => $.review.cancel)}
          </Button>
        )}
      </div>
      {rejecting && (
        <form className="space-y-2" onSubmit={(event) => { event.preventDefault(); void submitReject(); }}>
          <Label htmlFor={`requirement-reject-${detail.id}`}>{t(($) => $.review.reject_reason)}</Label>
          <Textarea
            id={`requirement-reject-${detail.id}`}
            required
            value={reason}
            onChange={(event) => { setReason(event.target.value); setReasonError(false); }}
            placeholder={t(($) => $.review.reject_placeholder)}
            disabled={reject.isPending}
          />
          {reasonError && <p role="alert" className="text-caption text-destructive">{t(($) => $.review.reject_required)}</p>}
          <div className="flex gap-2">
            <Button type="button" variant="outline" disabled={reject.isPending} onClick={() => setRejecting(false)}>{t(($) => $.review.close)}</Button>
            <Button type="submit" disabled={reject.isPending || !reason.trim()} aria-busy={reject.isPending}>{t(($) => $.review.reject_confirm)}</Button>
          </div>
        </form>
      )}
      {error && <p role="alert" className="text-caption text-destructive">{error.message}</p>}
      <AlertDialog open={!!acceptReview && inReview} onOpenChange={(open) => { if (!open && !accept.isPending) setAcceptReview(null); }}>
        <AlertDialogContent className="max-h-[90dvh] overflow-y-auto">
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.review.accept_title)}</AlertDialogTitle>
            <AlertDialogDescription>{t(($) => $.review.accept_consequence)}</AlertDialogDescription>
          </AlertDialogHeader>
          <Label htmlFor={acceptFormId}>{t(($) => $.review.result_summary)}</Label>
          <Textarea
            id={acceptFormId}
            value={acceptReview?.summary ?? ""}
            onChange={(event) => setAcceptReview((current) => current ? { ...current, summary: event.target.value } : null)}
            placeholder={t(($) => $.review.result_placeholder)}
            maxLength={20000}
            rows={6}
            disabled={accept.isPending}
          />
          <Label htmlFor={`${acceptFormId}-status`}>{t(($) => $.review.source_status)}</Label>
          <select
            id={`${acceptFormId}-status`}
            className="h-9 rounded-md border border-input bg-background px-3 text-body"
            value={acceptReview?.statusName ?? ""}
            onChange={(event) => setAcceptReview((current) => current ? { ...current, statusName: event.target.value } : null)}
            disabled={accept.isPending}
          >
            <option value="">{t(($) => $.review.keep_source_status)}</option>
            {(source?.statuses ?? []).map((status) => <option value={status.name} key={status.id}>{status.name}</option>)}
          </select>
          {acceptStale && <p role="alert" className="text-caption text-destructive">{t(($) => $.review.accept_stale)}</p>}
          {accept.error && <p role="alert" className="text-caption text-destructive">{accept.error.message}</p>}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={accept.isPending}>{t(($) => $.review.close)}</AlertDialogCancel>
            <Button type="button" disabled={accept.isPending || acceptStale || !acceptReview?.summary.trim() || !detail.permissions.canAccept} aria-busy={accept.isPending} onClick={() => void submitAccept()}>
              {t(($) => $.review.accept_confirm)}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      <AlertDialog open={cancelOpen} onOpenChange={(open) => { if (!open && !cancel.isPending) setCancelOpen(false); }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.review.cancel_title)}</AlertDialogTitle>
            <AlertDialogDescription>{t(($) => $.review.cancel_consequence)}</AlertDialogDescription>
          </AlertDialogHeader>
          {cancel.error && <p role="alert" className="text-caption text-destructive">{cancel.error.message}</p>}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={cancel.isPending}>{t(($) => $.review.close)}</AlertDialogCancel>
            <Button type="button" variant="destructive" disabled={cancel.isPending} aria-busy={cancel.isPending} onClick={() => void confirmCancel()}>
              {t(($) => $.review.cancel_confirm)}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  );
}

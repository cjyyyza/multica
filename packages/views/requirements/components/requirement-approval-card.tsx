"use client";

import { useRef, useState } from "react";
import { createSafeId } from "@multica/core/utils";
import {
  parseYixiezuoRequirementSnapshot,
  resolveRequirementCandidateAttachments,
  useApproveRequirement,
  type RequirementDetail,
} from "@multica/core/requirements";
import { Button } from "@multica/ui/components/ui/button";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { RequirementSnapshotView } from "./requirement-snapshot";
import { useT } from "../../i18n";

interface ReviewSnapshot {
  candidateId: string;
  candidateDigest: string;
  requirementRevision: number;
}

export function RequirementApprovalCard({ wsId, detail }: { wsId: string; detail: RequirementDetail }) {
  const { t } = useT("requirements");
  const approve = useApproveRequirement(wsId, detail.id);
  const approveKey = useRef<string | null>(null);
  const [review, setReview] = useState<ReviewSnapshot | null>(null);
  const candidate = detail.latestCandidate;
  if (!candidate) return null;

  const sourceSnapshot = parseYixiezuoRequirementSnapshot(detail.sourceSnapshot);
  const attachmentsUnavailable = resolveRequirementCandidateAttachments(candidate.snapshot, sourceSnapshot)
    .some(({ attachment }) => !attachment);
  const stale = !!review && (
    review.candidateId !== candidate.id ||
    review.candidateDigest !== candidate.digest ||
    review.requirementRevision !== detail.revision
  );
  const beginReview = () => {
    if (detail.revision === undefined) return;
    approve.reset();
    approveKey.current = createSafeId();
    setReview({
      candidateId: candidate.id,
      candidateDigest: candidate.digest,
      requirementRevision: detail.revision,
    });
  };
  const confirm = async () => {
    if (!review || stale || attachmentsUnavailable) return;
    const key = approveKey.current ?? createSafeId();
    approveKey.current = key;
    try {
      await approve.mutateAsync({
        expectedRevision: review.requirementRevision,
        candidateId: review.candidateId,
        candidateDigest: review.candidateDigest,
        idempotencyKey: key,
      });
      approveKey.current = null;
      setReview(null);
    } catch {
      // The same reviewed candidate and key remain available for an explicit retry.
    }
  };

  return (
    <section aria-label={t(($) => $.approval.title)} className="space-y-3 rounded-lg border border-border p-3">
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-body font-medium">{t(($) => $.approval.title)}</h3>
        <span className="text-caption text-muted-foreground">
          {t(($) => $.approval.candidate_revision, { revision: candidate.revision ?? "—" })}
          {" · "}
          <code title={candidate.digest}>{candidate.digest.replace(/^sha256:/, "").slice(0, 12)}</code>
        </span>
      </div>
      {detail.permissions.canApprove && (
        <Button type="button" variant="outline" onClick={beginReview} disabled={detail.revision === undefined}>
          {t(($) => $.approval.review)}
        </Button>
      )}
      <AlertDialog open={!!review} onOpenChange={(open) => { if (!open && !approve.isPending) setReview(null); }}>
        <AlertDialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-2xl">
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.approval.confirm_title)}</AlertDialogTitle>
            <AlertDialogDescription>{t(($) => $.approval.consequence)}</AlertDialogDescription>
          </AlertDialogHeader>
          {stale ? (
            <div className="space-y-3">
              <p role="alert" className="text-caption text-destructive">{t(($) => $.approval.stale)}</p>
              <Button type="button" variant="outline" onClick={beginReview}>{t(($) => $.approval.review_latest)}</Button>
            </div>
          ) : (
            <>
              <p className="text-caption text-muted-foreground">
                {t(($) => $.approval.candidate_revision, { revision: candidate.revision ?? "—" })}
                {" · "}
                <code title={candidate.digest}>{candidate.digest.replace(/^sha256:/, "").slice(0, 12)}</code>
              </p>
              <RequirementSnapshotView wsId={wsId} snapshot={candidate.snapshot} sourceSnapshot={sourceSnapshot} />
            </>
          )}
          {approve.error && <p role="alert" className="text-caption text-destructive">{approve.error.message}</p>}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={approve.isPending}>{t(($) => $.approval.cancel)}</AlertDialogCancel>
            <Button type="button" disabled={approve.isPending || stale || attachmentsUnavailable} aria-busy={approve.isPending} onClick={() => void confirm()}>
              {t(($) => $.approval.confirm)}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  );
}

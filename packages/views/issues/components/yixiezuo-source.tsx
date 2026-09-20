"use client";

import { useEffect, useId, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { yixiezuoImportOptions, yixiezuoImportStatesOptions, yixiezuoKeys } from "@multica/core/yixiezuo";
import { Button } from "@multica/ui/components/ui/button";
import { Label } from "@multica/ui/components/ui/label";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger } from "@multica/ui/components/ui/dialog";
import { useT } from "../../i18n";
import { YixiezuoSourcePreview } from "./yixiezuo-import";

export function YixiezuoSource({ issueId, revision }: { issueId: string; revision?: number }) {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const { t } = useT("settings");
  const formId = useId();
  const [open, setOpen] = useState(false);
  const [summary, setSummary] = useState("");
  const [statusName, setStatusName] = useState("");
  const [reviewedRevision, setReviewedRevision] = useState<number | null>(null);
  const [confirmed, setConfirmed] = useState(false);
  const [sourceDigest, setSourceDigest] = useState("");
  const data = useQuery(yixiezuoImportOptions(wsId, issueId));
  const invalidate = () => qc.invalidateQueries({ queryKey: yixiezuoKeys.all(wsId) });
  useEffect(() => { void qc.invalidateQueries({ queryKey: yixiezuoKeys.imported(wsId, issueId) }); }, [qc, wsId, issueId, revision]);
  const refresh = useMutation({ mutationFn: () => api.refreshYixiezuoImport(wsId, issueId), onSuccess: invalidate, onError: invalidate });
  const publish = useMutation({ mutationFn: () => api.publishYixiezuoResult(wsId, issueId, { summary, status_name: statusName, revision: reviewedRevision ?? -1, confirmed, source_digest: sourceDigest }), onSuccess: async () => { await invalidate(); setOpen(false); setConfirmed(false); }, onError: invalidate });
  const imported = data.data?.import;
  if (!imported) return data.error ? <p role="alert" className="text-caption text-destructive">{data.error.message}</p> : null;
  const busy = ["pending", "running"].includes(imported.state) || refresh.isPending || publish.isPending;
  const needsRefresh = ["conflict", "unknown"].includes(imported.state);
  const canPublish = !busy && !needsRefresh && imported.snapshot.lock_version !== null;
  const staleReview = reviewedRevision !== imported.revision || sourceDigest !== imported.snapshot.digest;
  return <section className="space-y-3 rounded-lg border border-border p-3">
    <a className="text-caption font-medium underline" href={imported.snapshot.source.url} target="_blank" rel="noreferrer">{t(($) => $.yixiezuo.source_issue, { id: imported.snapshot.source.id })}</a>
    <p role="status" className="text-caption text-muted-foreground">{t(($) => $.yixiezuo.states[imported.state])}</p>
    <p className="text-caption text-muted-foreground">{t(($) => $.yixiezuo.source_status, { status: imported.snapshot.status })}</p>
    {imported.operation?.error && <p role="alert" className="break-words text-caption text-destructive">{imported.operation.error}</p>}
    {(refresh.error || publish.error) && <p role="alert" className="text-caption text-destructive">{(refresh.error || publish.error)?.message}</p>}
    <details><summary className="cursor-pointer text-caption">{t(($) => $.yixiezuo.material)}</summary><div className="mt-3 max-h-96 overflow-y-auto"><YixiezuoSourcePreview snapshot={imported.snapshot} /></div></details>
    <div className="flex flex-wrap gap-2">
      <Button size="sm" variant="outline" disabled={busy} onClick={() => refresh.mutate()}>{t(($) => $.yixiezuo.refresh)}</Button>
      <Dialog open={open} onOpenChange={(next) => { setOpen(next); if (next) { setReviewedRevision(imported.revision); setSourceDigest(imported.snapshot.digest); setConfirmed(false); setStatusName(""); publish.reset(); } }}>
        <DialogTrigger render={<Button size="sm" variant="outline" disabled={!canPublish} />}>{t(($) => $.yixiezuo.publish)}</DialogTrigger>
        <DialogContent className="max-h-[90dvh] overflow-y-auto">
          <DialogHeader><DialogTitle>{t(($) => $.yixiezuo.publish)}</DialogTitle></DialogHeader>
          <p className="text-caption text-muted-foreground">{t(($) => $.yixiezuo.publish_consequence)}</p>
          <Label htmlFor={formId}>{t(($) => $.yixiezuo.result_label)}</Label>
          <Textarea id={formId} value={summary} onChange={(event) => { setSummary(event.target.value); setConfirmed(false); }} rows={7} maxLength={20000} />
          <Label htmlFor={`${formId}-status`}>{t(($) => $.yixiezuo.target_status)}</Label>
          <select id={`${formId}-status`} className="h-9 rounded-md border border-input bg-background px-3 text-body" value={statusName} onChange={(event) => { setStatusName(event.target.value); setConfirmed(false); }}>
            <option value="">{t(($) => $.yixiezuo.keep_status)}</option>
            {imported.snapshot.statuses.map((status) => <option value={status.name} key={status.id}>{status.name}</option>)}
          </select>
          <label className="flex items-start gap-2 text-caption"><input type="checkbox" checked={confirmed} onChange={(event) => setConfirmed(event.target.checked)} />{t(($) => $.yixiezuo.acceptance)}</label>
          {publish.error && <p role="alert" className="text-caption text-destructive">{publish.error.message}</p>}
          {staleReview && <p role="alert" className="text-caption text-destructive">{t(($) => $.yixiezuo.review_changed)}</p>}
          <Button disabled={!confirmed || !summary.trim() || publish.isPending || staleReview} aria-busy={publish.isPending} onClick={() => publish.mutate()}>{t(($) => $.yixiezuo.confirm_publish)}</Button>
        </DialogContent>
      </Dialog>
    </div>
  </section>;
}

export function YixiezuoCardBadge({ issueId, externalId }: { issueId: string; externalId: string }) {
  const wsId = useWorkspaceId();
  const { t } = useT("settings");
  // All visible imported cards share one small workspace query, not N snapshots.
  const { data } = useQuery(yixiezuoImportStatesOptions(wsId));
  const source = data?.find((entry) => entry.issue_id === issueId);
  return <span className="mt-1 flex flex-wrap gap-x-2 gap-y-0.5 text-caption text-muted-foreground" title={source ? `${source.source_url}\n${source.source_status}` : undefined}>
    <span>{t(($) => $.yixiezuo.source_issue, { id: externalId })}</span>
    {source && <span>{t(($) => $.yixiezuo.states[source.state])}</span>}
  </span>;
}

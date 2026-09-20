"use client";

import { useId, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Download, ExternalLink } from "lucide-react";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { projectListOptions } from "@multica/core/projects";
import { issueKeys } from "@multica/core/issues/queries";
import { yixiezuoOperationOptions, type YixiezuoSnapshot } from "@multica/core/yixiezuo";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger } from "@multica/ui/components/ui/dialog";
import { Markdown } from "@multica/ui/markdown";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";

export function YixiezuoSourcePreview({ snapshot }: { snapshot: YixiezuoSnapshot }) {
  const { t } = useT("settings");
  let description = snapshot.description;
  for (const attachment of snapshot.attachments) {
    if (attachment.local_url) {
      description = description.replaceAll(attachment.url, attachment.local_url).replaceAll(`/attachments/${attachment.id}/${encodeURIComponent(attachment.name)}`, attachment.local_url);
    }
  }
  return <div className="space-y-4 break-words">
    <a className="inline-flex items-center gap-1 text-caption underline" href={snapshot.source.url} target="_blank" rel="noreferrer">
      {t(($) => $.yixiezuo.source_issue, { id: snapshot.source.id })}<ExternalLink className="size-3" />
    </a>
    <p className="text-body font-medium">{snapshot.title}</p>
    <p className="text-caption text-muted-foreground">{t(($) => $.yixiezuo.source_status, { status: snapshot.status })}</p>
    <Markdown>{description}</Markdown>
    {snapshot.fields.length > 0 && <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-caption">
      {snapshot.fields.map((field, i) => <div className="contents" key={i}><dt className="text-muted-foreground">{field.name}</dt><dd>{field.value}</dd></div>)}
    </dl>}
    {snapshot.attachments.length > 0 && <div className="space-y-1">
      <p className="text-caption font-medium">{t(($) => $.yixiezuo.attachments)}</p>
      {snapshot.attachments.map((attachment) => <a key={attachment.id} href={attachment.local_url || attachment.url} target="_blank" rel="noreferrer" className="block break-all text-caption underline">{attachment.name}</a>)}
    </div>}
    {snapshot.comments.length > 0 && <details>
      <summary className="cursor-pointer text-caption font-medium">{t(($) => $.yixiezuo.comments, { count: snapshot.comments.length })}</summary>
      <div className="mt-3 space-y-4">{snapshot.comments.map((comment) => <div key={comment.id}>
        <p className="text-caption text-muted-foreground">{comment.author} · {comment.created_at}</p>
        <Markdown>{comment.content}</Markdown>
      </div>)}</div>
    </details>}
    {snapshot.warnings.map((warning) => <p role="status" className="text-caption text-muted-foreground" key={warning}>{warning}</p>)}
  </div>;
}

export function YixiezuoImportForm({ onImported }: { onImported?: () => void }) {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const qc = useQueryClient();
  const inputId = useId();
  const [url, setUrl] = useState("");
  const [projectId, setProjectId] = useState("");
  const [operationId, setOperationId] = useState("");
  const projects = useQuery(projectListOptions(wsId));
  const operation = useQuery(yixiezuoOperationOptions(wsId, operationId));
  const preview = useMutation({ mutationFn: () => api.previewYixiezuoIssue(wsId, url.trim()), onSuccess: (data) => setOperationId(data.id) });
  const imported = useMutation({
    mutationFn: () => api.importYixiezuoIssue(wsId, operationId, projectId || null),
    onSuccess: async (data) => {
      await qc.invalidateQueries({ queryKey: issueKeys.all(wsId) });
      onImported?.();
      navigation.push(paths.issueDetail(data.issue.id));
    },
  });
  const pending = preview.isPending || imported.isPending || (!!operationId && (!operation.data || ["pending", "running"].includes(operation.data.state)) && !operation.isError);
  const snapshot = operationId && operation.data?.state === "succeeded" ? operation.data.snapshot : null;
  const error = preview.error || operation.error || imported.error;
  return <div className="space-y-5">
    <div className="space-y-2">
      <p className="text-caption text-muted-foreground">{t(($) => $.yixiezuo.bridge_help)}</p>
      <code className="block overflow-x-auto rounded-md bg-muted px-3 py-2 text-caption">{`multica yixiezuo bridge --workspace-id ${wsId}`}</code>
    </div>
    <form className="space-y-3" onSubmit={(event) => { event.preventDefault(); setOperationId(""); preview.mutate(); }}>
      <Label htmlFor={inputId}>{t(($) => $.yixiezuo.url_label)}</Label>
      <Input id={inputId} type="url" required value={url} onChange={(event) => { setUrl(event.target.value); setOperationId(""); preview.reset(); imported.reset(); }} disabled={pending} placeholder="https://your-instance.pm.netease.com/issues/12345" />
      <Button type="submit" variant="outline" disabled={pending || !url.trim()} aria-busy={pending}>{t(($) => $.yixiezuo.preview)}</Button>
    </form>
    {pending && <p role="status" className="text-caption">{t(($) => $.yixiezuo.waiting)}</p>}
    {error && <p role="alert" className="text-caption text-destructive">{error.message}</p>}
    {operation.data?.error && <p role="alert" className="text-caption text-destructive">{operation.data.error}</p>}
    {snapshot && <>
      <div className="max-h-[45vh] overflow-y-auto rounded-lg border border-border p-4"><YixiezuoSourcePreview snapshot={snapshot} /></div>
      <div className="space-y-2">
        <Label htmlFor={`${inputId}-project`}>{t(($) => $.yixiezuo.project_label)}</Label>
        <select id={`${inputId}-project`} value={projectId} onChange={(event) => setProjectId(event.target.value)} disabled={imported.isPending} className="h-9 w-full rounded-md border border-input bg-background px-3 text-body">
          <option value="">{t(($) => $.yixiezuo.project_none)}</option>
          {projects.data?.map((project) => <option value={project.id} key={project.id}>{project.title}</option>)}
        </select>
      </div>
      <p className="text-caption text-muted-foreground">{t(($) => $.yixiezuo.import_consequence)}</p>
      <Button disabled={imported.isPending} aria-busy={imported.isPending} onClick={() => imported.mutate()}>{t(($) => $.yixiezuo.confirm_import)}</Button>
    </>}
  </div>;
}

export function YixiezuoImportDialog() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const [open, setOpen] = useState(false);
  return <Dialog open={open} onOpenChange={setOpen}>
    <DialogTrigger render={<Button variant="outline" size="sm" />}><Download className="size-3.5" />{t(($) => $.yixiezuo.import_action)}</DialogTrigger>
    <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-2xl">
      <DialogHeader><DialogTitle>{t(($) => $.yixiezuo.import_action)}</DialogTitle></DialogHeader>
      <YixiezuoImportForm key={wsId} onImported={() => setOpen(false)} />
    </DialogContent>
  </Dialog>;
}

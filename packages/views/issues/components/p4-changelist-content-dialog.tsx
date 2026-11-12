"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { p4ChangelistContentOptions } from "@multica/core/p4";
import { useWorkspaceId } from "@multica/core/hooks";
import type { P4Changelist, P4ChangelistContent } from "@multica/core/types";
import { FileCode, Loader2, RefreshCw } from "@multica/ui/icons";
import { Button } from "@multica/ui/components/ui/button";
import { DialogContent, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

export function P4ChangelistContentDialog({ issueId, changelist }: { issueId: string; changelist: P4Changelist }) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const [selectedPath, setSelectedPath] = useState<string | null>(null);
  const listing = useQuery(p4ChangelistContentOptions(wsId, issueId, changelist.id));
  const files = listing.data?.files ?? [];
  const selected = files.find((file) => file.depot_path === selectedPath) ?? files[0];
  const diff = useQuery({
    ...p4ChangelistContentOptions(wsId, issueId, changelist.id, selected?.depot_path),
    enabled: !!selected,
  });

  return (
    <DialogContent className="flex h-[min(48rem,calc(100dvh-2rem))] w-[calc(100vw_-_2rem)] max-w-none flex-col gap-0 overflow-hidden p-0 sm:max-w-6xl" aria-describedby={undefined}>
      <DialogHeader className="shrink-0 gap-1 border-b border-border px-4 py-3 pr-12">
        <DialogTitle>{t(($) => $.p4.changelist, { number: changelist.number })}</DialogTitle>
        {listing.data ? (
          <span className="text-caption text-muted-foreground">
            {listing.data.source === "shelf" ? t(($) => $.p4.content_source_shelf)
              : listing.data.source === "submitted" ? t(($) => $.p4.content_source_submitted)
                : listing.data.source === "workspace" ? t(($) => $.p4.content_source_workspace)
                  : t(($) => $.p4.content_files)}
          </span>
        ) : null}
      </DialogHeader>
      {listing.isPending ? <LoadingContent /> : listing.isError ? (
        <ContentError onRetry={() => void listing.refetch()} />
      ) : files.length === 0 ? (
        <p className="p-6 text-caption text-muted-foreground">{t(($) => $.p4.content_empty)}</p>
      ) : (
        <div className="flex min-h-0 flex-1 flex-col sm:flex-row">
          <nav aria-label={t(($) => $.p4.content_files)} className="max-h-44 shrink-0 overflow-y-auto border-b border-border p-2 sm:max-h-none sm:w-72 sm:border-r sm:border-b-0">
            {files.map((file) => {
              const name = file.depot_path.split("/").pop() ?? file.depot_path;
              const directory = file.depot_path.slice(0, -name.length);
              return (
                <button
                  key={file.depot_path}
                  type="button"
                  aria-pressed={selected?.depot_path === file.depot_path}
                  title={file.depot_path}
                  onClick={() => setSelectedPath(file.depot_path)}
                  className={cn("mb-0.5 flex w-full items-start gap-2 rounded-md px-2 py-2 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring",
                    selected?.depot_path === file.depot_path ? "bg-accent text-accent-foreground hover:bg-accent" : "hover:bg-muted")}
                >
                  <FileCode className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                  <span className="min-w-0 flex-1">
                    <span className="block break-all text-caption font-medium leading-snug">{name}</span>
                    <span className="block truncate text-micro text-muted-foreground" dir="ltr">{directory}</span>
                  </span>
                  <span className="shrink-0 text-micro text-muted-foreground">{fileActionLabel(file.action, t)}</span>
                </button>
              );
            })}
          </nav>
          <section aria-label={t(($) => $.p4.content_diff)} className="flex min-h-0 min-w-0 flex-1 flex-col">
            <div className="flex shrink-0 items-center gap-2 border-b border-border px-3 py-2">
              <span className="min-w-0 flex-1 truncate font-mono text-micro" title={selected?.depot_path}>{selected?.depot_path}</span>
              <Button variant="ghost" size="icon-xs" aria-label={t(($) => $.p4.content_refresh)} disabled={listing.isFetching || diff.isFetching} onClick={() => { void listing.refetch(); void diff.refetch(); }}>
                <RefreshCw />
              </Button>
            </div>
            <div className="min-h-0 flex-1 overflow-auto" tabIndex={0}>
              {diff.isPending ? <LoadingContent /> : diff.isError ? <ContentError onRetry={() => void diff.refetch()} />
                : diff.data ? <ChangelistDiff content={diff.data} /> : null}
            </div>
          </section>
        </div>
      )}
    </DialogContent>
  );
}

function LoadingContent() {
  const { t } = useT("issues");
  return <div role="status" className="flex items-center gap-2 p-6 text-caption text-muted-foreground"><Loader2 className="size-4 animate-spin" />{t(($) => $.p4.content_loading)}</div>;
}

function ContentError({ onRetry }: { onRetry: () => void }) {
  const { t } = useT("issues");
  return <div className="space-y-3 p-6"><p role="alert" className="text-caption text-muted-foreground">{t(($) => $.p4.content_failed)}</p><Button variant="outline" size="sm" onClick={onRetry}>{t(($) => $.p4.content_retry)}</Button></div>;
}

function fileActionLabel(action: string, t: ReturnType<typeof useT<"issues">>["t"]): string {
  switch (action) {
    case "add": return t(($) => $.p4.content_action_add);
    case "edit": return t(($) => $.p4.content_action_edit);
    case "delete": return t(($) => $.p4.content_action_delete);
    case "branch": return t(($) => $.p4.content_action_branch);
    case "integrate": return t(($) => $.p4.content_action_integrate);
    case "move/add": return t(($) => $.p4.content_action_move_add);
    case "move/delete": return t(($) => $.p4.content_action_move_delete);
    default: return action;
  }
}

function ChangelistDiff({ content }: { content: P4ChangelistContent }) {
  const { t } = useT("issues");
  const lines = content.diff.replaceAll("\r\n", "\n").split("\n").filter((line) => !line.startsWith("==== "));
  if (content.binary || content.unavailable || lines.every((line) => !line.trim())) {
    return <p className="p-6 text-caption text-muted-foreground">{content.binary ? t(($) => $.p4.content_binary)
      : content.unavailable === "workspace_unavailable" ? t(($) => $.p4.content_workspace_unavailable)
        : content.unavailable ? t(($) => $.p4.content_unavailable) : t(($) => $.p4.content_no_changes)}</p>;
  }
  return <>
    <pre className="w-max min-w-full py-3 font-mono text-caption leading-relaxed" dir="ltr"><code>
      {lines.map((line, index) => <span key={index} className={cn("block px-4",
        line.startsWith("+") ? "bg-success/10 text-success" : line.startsWith("-") ? "bg-destructive/10 text-destructive"
          : line.startsWith("@@") ? "bg-muted text-muted-foreground" : "text-foreground")}>{line || "\u00a0"}</span>)}
    </code></pre>
    {content.truncated ? <p className="sticky bottom-0 border-t border-border bg-background px-4 py-2 text-caption text-muted-foreground">{t(($) => $.p4.content_truncated)}</p> : null}
  </>;
}

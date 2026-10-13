"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  CheckCircle2,
  ChevronRight,
  CircleDashed,
  CircleSlash,
  Copy,
  Inbox,
  MoreHorizontal,
  Plus,
  TriangleAlert,
  Unlink,
} from "lucide-react";
import {
  issueP4ChangelistsOptions,
  p4ChangelistSummaryEntries,
  useLinkIssueP4Changelist,
  useUnlinkIssueP4Changelist,
} from "@multica/core/p4";
import { projectResourcesOptions } from "@multica/core/projects";
import { useWorkspaceId } from "@multica/core/hooks";
import type {
  P4Changelist,
  P4ChangelistStatus,
  PerforceDepotResourceRef,
  ProjectResource,
} from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@multica/ui/components/ui/popover";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { copyText } from "@multica/ui/lib/clipboard";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import { p4DepotLabel } from "../../common/p4-depot";

type IssuesT = ReturnType<typeof useT<"issues">>["t"];

function isPerforceResource(
  resource: ProjectResource,
): resource is ProjectResource & { resource_ref: PerforceDepotResourceRef } {
  return resource.resource_type === "perforce_depot";
}

const STATUS_ICON: Record<
  P4ChangelistStatus,
  { icon: React.ComponentType<{ className?: string }>; className: string }
> = {
  pending: { icon: CircleDashed, className: "text-muted-foreground" },
  shelved: { icon: Inbox, className: "text-amber-600 dark:text-amber-400" },
  submitted: { icon: CheckCircle2, className: "text-emerald-600 dark:text-emerald-400" },
  deleted: { icon: CircleSlash, className: "text-muted-foreground" },
};

function statusLabel(status: P4ChangelistStatus, t: IssuesT): string {
  switch (status) {
    case "shelved":
      return t(($) => $.p4.status_shelved);
    case "submitted":
      return t(($) => $.p4.status_submitted);
    case "deleted":
      return t(($) => $.p4.status_deleted);
    default:
      return t(($) => $.p4.status_pending);
  }
}

/** Helix Swarm's own state string; an unknown one is shown as the server sent it. */
function swarmStateLabel(state: string, t: IssuesT): string {
  switch (state.trim().toLowerCase().replace(/[^a-z0-9]/g, "")) {
    case "needsreview":
    case "pending":
      return t(($) => $.detail.pull_request_verdict_needs_review);
    case "needsrevision":
      return t(($) => $.detail.pull_request_verdict_needs_revision);
    case "approved":
      return t(($) => $.detail.pull_request_verdict_approved);
    case "committed":
      return t(($) => $.detail.pull_request_state_submitted);
    case "rejected":
    case "archived":
      return t(($) => $.detail.pull_request_state_closed);
    default:
      return state;
  }
}

interface ChangelistWarning {
  key: string;
  label: string;
  /** Depot paths behind the warning, for the hover title. */
  files: string[];
}

/**
 * What a person has to act on: the files this changelist keeps other people
 * out of, files it holds at an older revision, and a description without the
 * 易协作 ticket the submit rule requires.
 */
function changelistWarnings(changelist: P4Changelist, t: IssuesT): ChangelistWarning[] {
  const warnings: ChangelistWarning[] = [];
  if (changelist.exclusive_files.length > 0) {
    warnings.push({
      key: "exclusive_files",
      label: t(($) => $.p4.warning_exclusive_held, { count: changelist.exclusive_files.length }),
      files: changelist.exclusive_files,
    });
  }
  for (const warning of changelist.warnings) {
    // exclusive_held restates the exclusive_files column above it.
    if (warning.kind === "exclusive_held" && changelist.exclusive_files.length > 0) continue;
    warnings.push({
      key: `kind:${warning.kind}`,
      label: warningKindLabel(warning, t),
      files: warning.files,
    });
  }
  if (!changelist.ticket && changelist.status !== "deleted") {
    warnings.push({ key: "missing_ticket", label: t(($) => $.p4.warning_missing_ticket), files: [] });
  }
  return warnings;
}

function warningKindLabel(warning: { kind: string; files: string[] }, t: IssuesT): string {
  switch (warning.kind) {
    case "not_head":
      return t(($) => $.p4.warning_not_head);
    case "opened_elsewhere":
      return t(($) => $.p4.warning_opened_elsewhere);
    case "exclusive_held":
      return t(($) => $.p4.warning_exclusive_held, { count: warning.files.length });
    case "default_changelist_files":
      return t(($) => $.p4.warning_default_changelist_files);
    case "unexpected_opens":
      return t(($) => $.p4.warning_unexpected_opens);
    default:
      return warning.kind;
  }
}

/**
 * Whether this issue should surface Perforce at all: its project has a depot,
 * or it already carries changelists from one that was later detached. The
 * workspace depot list is not a signal — a depot the project never attached
 * says nothing about this issue.
 */
export function useIssueP4Surface(issueId: string, projectId: string | null) {
  const wsId = useWorkspaceId();
  const { data } = useQuery(issueP4ChangelistsOptions(wsId, issueId));
  const { data: resources } = useQuery({
    ...projectResourcesOptions(wsId, projectId ?? ""),
    enabled: !!wsId && !!projectId,
  });
  const changelists = data?.changelists ?? [];
  const depots = useMemo(
    () => (resources ?? []).filter(isPerforceResource).map((resource) => resource.resource_ref),
    [resources],
  );
  return { changelists, depots, visible: depots.length > 0 || changelists.length > 0 };
}

/**
 * The issue sidebar's Perforce changelists: one card per changelist with its
 * number, state, size, Swarm review and anything a person must unblock, plus
 * linking a changelist by number by hand.
 */
export function P4ChangelistsSection({
  issueId,
  projectId,
}: {
  issueId: string;
  projectId: string | null;
}) {
  const { t } = useT("issues");
  const [open, setOpen] = useState(true);
  const { changelists, depots, visible } = useIssueP4Surface(issueId, projectId);

  if (!visible) return null;

  return (
    <div>
      <div className="mb-2 flex w-full items-center gap-0.5">
        <button
          type="button"
          aria-expanded={open}
          className={cn(
            "flex min-w-0 flex-1 items-center gap-1 rounded-md px-2 py-1 text-caption font-medium transition-colors hover:bg-accent/70",
            open ? null : "text-muted-foreground hover:text-foreground",
          )}
          onClick={() => setOpen(!open)}
        >
          <span className="truncate">{t(($) => $.p4.section_title)}</span>
          {changelists.length > 0 ? (
            <span className="shrink-0 rounded-xs bg-muted px-1 text-micro font-medium tabular-nums text-muted-foreground">
              {changelists.length}
            </span>
          ) : null}
          <ChevronRight
            className={cn(
              "!size-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform",
              open ? "rotate-90" : null,
            )}
          />
        </button>
        {depots.length > 0 ? (
          <LinkChangelistPopover
            issueId={issueId}
            depots={depots}
            onLinked={() => setOpen(true)}
          />
        ) : null}
      </div>
      {open ? (
        <div className="space-y-1.5 pl-2">
          {changelists.length === 0 ? (
            <p className="px-2 text-caption text-muted-foreground">{t(($) => $.p4.empty)}</p>
          ) : (
            changelists.map((changelist) => (
              <ChangelistCard key={changelist.id} issueId={issueId} changelist={changelist} />
            ))
          )}
        </div>
      ) : null}
    </div>
  );
}

function ChangelistCard({ issueId, changelist }: { issueId: string; changelist: P4Changelist }) {
  const { t } = useT("issues");
  const cfg = STATUS_ICON[changelist.status];
  const StatusIcon = cfg.icon;
  const renumbered =
    changelist.original_number > 0 && changelist.original_number !== changelist.number
      ? t(($) => $.p4.renumbered, {
          number: changelist.number,
          original: changelist.original_number,
        })
      : undefined;
  const warnings = changelistWarnings(changelist, t);

  return (
    <div data-testid="p4-changelist-card" className="rounded-md border border-border px-2 py-1.5">
      <div className="flex items-center gap-1.5">
        <StatusIcon className={cn("size-3.5 shrink-0", cfg.className)} />
        <span className="truncate text-caption font-medium tabular-nums" title={renumbered}>
          {t(($) => $.p4.changelist, { number: changelist.number })}
        </span>
        <span className="shrink-0 text-micro text-muted-foreground">
          {statusLabel(changelist.status, t)}
        </span>
        <CopyNumberButton number={changelist.number} />
        <ChangelistMenu issueId={issueId} changelist={changelist} />
      </div>
      <div className="mt-1 flex flex-wrap items-center gap-x-1.5 text-micro text-muted-foreground tabular-nums">
        <span>{t(($) => $.p4.files_count, { count: changelist.files_count })}</span>
        {changelist.swarm_review_number ? (
          <>
            <span aria-hidden="true" className="text-faint-foreground">
              ·
            </span>
            {changelist.swarm_review_url ? (
              <a
                href={changelist.swarm_review_url}
                target="_blank"
                rel="noreferrer noopener"
                className="underline decoration-border underline-offset-2 hover:text-foreground hover:decoration-foreground"
              >
                {t(($) => $.p4.swarm_review, { number: changelist.swarm_review_number })}
              </a>
            ) : (
              <span>{t(($) => $.p4.swarm_review, { number: changelist.swarm_review_number })}</span>
            )}
            {changelist.swarm_review_state ? (
              <span>{swarmStateLabel(changelist.swarm_review_state, t)}</span>
            ) : null}
          </>
        ) : null}
      </div>
      {warnings.length > 0 ? (
        <ul className="mt-1.5 rounded-md bg-muted/70 px-2 py-1 text-micro">
          {warnings.map((warning) => (
            <li key={warning.key} className="flex items-center gap-1.5 py-px">
              <TriangleAlert className="size-3 shrink-0 text-amber-600 dark:text-amber-400" />
              <span className="truncate" title={warning.files.join("\n") || undefined}>
                {warning.label}
              </span>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

function CopyNumberButton({ number }: { number: number }) {
  const { t } = useT("issues");
  return (
    <Button
      variant="ghost"
      size="icon-xs"
      className="ml-auto shrink-0"
      aria-label={t(($) => $.p4.copy_number)}
      onClick={() => {
        void copyText(String(number)).then((ok) => {
          if (ok) toast.success(t(($) => $.p4.copied, { number }));
          else toast.error(t(($) => $.p4.copy_failed));
        });
      }}
    >
      <Copy />
    </Button>
  );
}

/**
 * Unlinking is confirmed: the card is the only place the number, its Swarm
 * review and its warnings are shown together, and the server stops reporting
 * this changelist's status to the issue once it is gone.
 */
function ChangelistMenu({ issueId, changelist }: { issueId: string; changelist: P4Changelist }) {
  const { t } = useT("issues");
  const { t: tCommon } = useT("common");
  const wsId = useWorkspaceId();
  const [confirming, setConfirming] = useState(false);
  const unlink = useUnlinkIssueP4Changelist(wsId, issueId);

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <Button
              variant="ghost"
              size="icon-xs"
              className="shrink-0"
              aria-label={t(($) => $.p4.row_menu)}
            >
              <MoreHorizontal />
            </Button>
          }
        />
        <DropdownMenuContent align="end" className="w-56">
          <DropdownMenuItem onClick={() => setConfirming(true)}>
            <Unlink />
            {t(($) => $.p4.unlink)}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <AlertDialog open={confirming} onOpenChange={setConfirming}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.p4.unlink_title, { number: changelist.number })}
            </AlertDialogTitle>
            <AlertDialogDescription>{t(($) => $.p4.unlink_body)}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tCommon(($) => $.cancel)}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={unlink.isPending}
              aria-busy={unlink.isPending || undefined}
              onClick={() =>
                unlink.mutate(changelist.id, {
                  onSuccess: () => setConfirming(false),
                  onError: () => toast.error(t(($) => $.p4.unlink_failed)),
                })
              }
            >
              {t(($) => $.p4.unlink_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

function LinkChangelistPopover({
  issueId,
  depots,
  onLinked,
}: {
  issueId: string;
  depots: PerforceDepotResourceRef[];
  onLinked: () => void;
}) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const [open, setOpen] = useState(false);
  const [number, setNumber] = useState("");
  const [port, setPort] = useState(depots[0]?.port ?? "");
  const link = useLinkIssueP4Changelist(wsId, issueId);
  // One depot needs no port: the server resolves it from the project. Several
  // depots make a missing port a 422, even when they share a server. The
  // select only appears when those depots are on different ports.
  const ports = useMemo(() => [...new Set(depots.map((depot) => depot.port))], [depots]);
  const includePort = depots.length > 1;
  const needsPortChoice = ports.length > 1;
  const parsed = Number.parseInt(number.trim(), 10);
  const valid = Number.isInteger(parsed) && parsed > 0;

  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) {
          setNumber("");
          link.reset();
        }
      }}
    >
      <PopoverTrigger
        render={
          <Button variant="ghost" size="icon-xs" aria-label={t(($) => $.p4.link_action)}>
            <Plus />
          </Button>
        }
      />
      <PopoverContent align="end" className="w-72">
        <form
          className="flex flex-col gap-2"
          onSubmit={(event) => {
            event.preventDefault();
            if (!valid || link.isPending) return;
            link.mutate(
              { number: parsed, ...(includePort ? { port } : {}) },
              {
                onSuccess: () => {
                  setOpen(false);
                  setNumber("");
                  onLinked();
                },
              },
            );
          }}
        >
          <Label htmlFor={`link-cl-${issueId}`}>{t(($) => $.p4.link_number_label)}</Label>
          <Input
            id={`link-cl-${issueId}`}
            type="text"
            inputMode="numeric"
            autoComplete="off"
            spellCheck={false}
            autoFocus
            value={number}
            placeholder={t(($) => $.p4.link_number_placeholder)}
            aria-invalid={link.error ? true : undefined}
            onChange={(event) => {
              setNumber(event.target.value);
              if (link.error) link.reset();
            }}
          />
          {needsPortChoice ? (
            <>
              <Label htmlFor={`link-cl-port-${issueId}`}>{t(($) => $.p4.link_port_label)}</Label>
              <Select
                items={ports.map((value) => ({
                  value,
                  label: depotLabelForPort(depots, value),
                }))}
                value={port}
                onValueChange={(next) => next && setPort(next)}
              >
                <SelectTrigger
                  id={`link-cl-port-${issueId}`}
                  size="sm"
                  className="w-full"
                  aria-label={t(($) => $.p4.link_port_label)}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent align="end">
                  {ports.map((value) => (
                    <SelectItem key={value} value={value}>
                      {depotLabelForPort(depots, value)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </>
          ) : null}
          {link.error ? (
            <p role="alert" className="text-caption text-destructive">
              {t(($) => $.p4.link_failed)}
            </p>
          ) : null}
          <div className="flex justify-end">
            <Button
              type="submit"
              size="sm"
              disabled={!valid || link.isPending}
              aria-busy={link.isPending || undefined}
            >
              {t(($) => $.p4.link_submit)}
            </Button>
          </div>
        </form>
      </PopoverContent>
    </Popover>
  );
}

function depotLabelForPort(depots: PerforceDepotResourceRef[], port: string): string {
  const depot = depots.find((candidate) => candidate.port === port);
  return depot ? p4DepotLabel(depot) : port;
}

/**
 * The issue's changelists as one result line — "CL 795115（已提交，原 795095）·
 * Swarm #123" — for a published 易协作 result. Empty when the issue has none.
 */
export function useP4ChangelistSummary(issueId: string): string {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const { data } = useQuery(issueP4ChangelistsOptions(wsId, issueId));
  const changelists = data?.changelists;
  return useMemo(() => {
    return p4ChangelistSummaryEntries(changelists ?? [])
      .map((entry) => {
        const status = statusLabel(entry.status, t);
        const detail =
          entry.originalNumber === null
            ? status
            : t(($) => $.p4.summary_detail_renumbered, {
                status,
                original: entry.originalNumber,
              });
        const line = t(($) => $.p4.summary_line, { number: entry.number, detail });
        return entry.swarmReviewNumber === null
          ? line
          : t(($) => $.p4.summary_review, { line, review: entry.swarmReviewNumber });
      })
      .join("\n");
  }, [changelists, t]);
}

"use client";

import { useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { PenLine } from "lucide-react";
import { api, ApiError, clientErrorMessage, errorCode } from "@multica/core/api";
import { isAgentRuntimeBound } from "@multica/core/agents";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { createSafeId } from "@multica/core/utils";
import { agentListOptions, memberListOptions } from "@multica/core/workspace/queries";
import { projectListOptions } from "@multica/core/projects";
import { yixiezuoKeys, yixiezuoOperationOptions } from "@multica/core/yixiezuo";
import { useCreateRequirement, type RequirementDetail } from "@multica/core/requirements";
import type { Agent } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Textarea } from "@multica/ui/components/ui/textarea";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@multica/ui/components/ui/dialog";
import { canAssignAgent } from "../../issues/components/pickers/assignee-picker";
import { YixiezuoSourcePreview } from "../../issues/components/yixiezuo-import";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";

const IDEA_TITLE_RUNES = 120;
const IDEA_BODY_BYTES = 20_000;

type IdeaAttempt = {
  idea: string;
  agentId: string;
  key: string;
  operationId: string;
};

const EMPTY_IDEA_ATTEMPT: IdeaAttempt = { idea: "", agentId: "", key: "", operationId: "" };

function existingRequirementId(error: unknown): string | null {
  if (!(error instanceof ApiError) || errorCode(error) !== "requirement_source_already_exists") return null;
  if (!error.body || typeof error.body !== "object") return null;
  const value = (error.body as { existing_requirement_id?: unknown }).existing_requirement_id;
  return typeof value === "string" && value.trim().length > 0 ? value : null;
}

/** First line, capped the same way `/requirement <idea>` titles a draft. */
export function requirementIdeaTitle(idea: string): string {
  const line = idea.trim().split("\n")[0] ?? "";
  return Array.from(line).slice(0, IDEA_TITLE_RUNES).join("");
}

function ideaBytes(idea: string): number {
  return new TextEncoder().encode(idea).length;
}

function requirementIdOf(operation: { requirement_id?: string } | undefined): string {
  return operation?.requirement_id?.trim() ?? "";
}

function useRequirementAgents(runtimeBound: boolean) {
  const wsId = useWorkspaceId();
  const user = useAuthStore((state) => state.user);
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const memberRole = members.find((member) => member.user_id === user?.id)?.role;
  return useMemo(
    () => agents.filter((agent) =>
      !agent.archived_at
      && canAssignAgent(agent, user?.id, memberRole)
      && (!runtimeBound || isAgentRuntimeBound(agent))),
    [agents, memberRole, runtimeBound, user?.id],
  );
}

function useSelectedAgent(agents: Agent[]) {
  const [agentId, setAgentId] = useState("");
  useEffect(() => {
    if (agents.some((agent) => agent.id === agentId)) return;
    setAgentId(agents[0]?.id ?? "");
  }, [agents, agentId]);
  return [agentId, setAgentId] as const;
}

function visibleClientError(error: unknown, fallback: string): string | null {
  if (!error) return null;
  if (typeof error === "string") return error;
  return clientErrorMessage(error) ?? fallback;
}

function RequirementAgentField({
  id,
  agents,
  agentId,
  onChange,
  disabled,
}: {
  id: string;
  agents: Agent[];
  agentId: string;
  onChange: (agentId: string) => void;
  disabled: boolean;
}) {
  const { t } = useT("requirements");
  return (
    <div className="space-y-2">
      <Label htmlFor={id}>{t(($) => $.intake.agent)}</Label>
      {agents.length === 0 ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.intake.no_agent)}</p>
      ) : (
        <select
          id={id}
          className="h-9 w-full rounded-md border border-input bg-background px-3 text-body"
          value={agentId}
          onChange={(event) => onChange(event.target.value)}
          disabled={disabled}
        >
          {agents.map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}
        </select>
      )}
    </div>
  );
}

function RequirementIdeaForm({
  attempt: controlledAttempt,
  onAttemptChange,
  onCreated,
  onUseExistingSource,
}: {
  /** Owned by the dialog so a thrown submit survives close and reopen. */
  attempt?: IdeaAttempt;
  onAttemptChange?: (attempt: IdeaAttempt) => void;
  onCreated?: (detail?: RequirementDetail) => void;
  onUseExistingSource?: () => void;
}) {
  const { t } = useT("requirements");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const queryClient = useQueryClient();
  const agents = useRequirementAgents(true);
  const [localAttempt, setLocalAttempt] = useState(EMPTY_IDEA_ATTEMPT);
  const attempt = controlledAttempt ?? localAttempt;
  const attemptRef = useRef(attempt);
  attemptRef.current = attempt;
  const updateAttempt = (next: IdeaAttempt) => {
    attemptRef.current = next;
    if (!controlledAttempt) setLocalAttempt(next);
    onAttemptChange?.(next);
  };
  const { idea, agentId, key: attemptKey, operationId } = attempt;
  const [reconcileUrl, setReconcileUrl] = useState("");
  const [reconcileOperationId, setReconcileOperationId] = useState("");
  const submitting = useRef(false);
  const reconcileKey = useRef<string | null>(null);
  const navigated = useRef(false);
  const settledReconcile = useRef("");
  const operation = useQuery(yixiezuoOperationOptions(wsId, operationId));
  const reconcileOperation = useQuery(yixiezuoOperationOptions(wsId, reconcileOperationId));
  const createDraft = useMutation({
    mutationFn: (input: { title: string; body: string; agentId: string; idempotencyKey: string }) =>
      api.createRequirementDraft(wsId, input),
  });
  const reconcile = useMutation({
    mutationFn: (input: { url: string; idempotencyKey: string }) =>
      api.reconcileRequirementDraft(wsId, attempt.operationId, input),
    onSuccess: (row) => setReconcileOperationId(row.id),
  });
  // Do not replace a payload that may already have been queued.
  useEffect(() => {
    if (attemptRef.current.key) return;
    if (!agents.length) return;
    if (agents.some((agent) => agent.id === attemptRef.current.agentId)) return;
    updateAttempt({ ...attemptRef.current, agentId: agents[0]?.id ?? "" });
    // updateAttempt closes over the latest setter; agents is the only input.
    // eslint-disable-next-line react-hooks/exhaustive-deps -- agent default must not rewrite an outstanding attempt
  }, [agents]);

  const trimmed = idea.trim();
  const tooLong = ideaBytes(trimmed) > IDEA_BODY_BYTES;
  const operationState = operation.data?.state ?? "";
  const requirementId = requirementIdOf(operation.data);
  // A draft whose read failed is as unconfirmed as `unknown`: offering create
  // again could duplicate the source issue.
  const loadFailed = !!operationId && !operation.data && operation.isError;
  const inFlight = createDraft.isPending
    || ["pending", "running"].includes(operationState)
    || (!!operationId && !operation.data && !operation.isError);
  const unconfirmed = operationState === "unknown" || operationState === "needs_reconcile" || loadFailed;
  const handoffPending = operationState === "succeeded" && !requirementId;
  const flowDisabled = errorCode(createDraft.error) === "requirement_flow_disabled";
  // A thrown request has a key but no operation yet. Keep that payload until retry or an explicit discard.
  const sendUnconfirmed = !!attemptKey && !operationId && !createDraft.isPending && !inFlight;
  const locked = inFlight || unconfirmed || handoffPending || flowDisabled || sendUnconfirmed;
  const canSubmit = !!trimmed && !tooLong && !!agentId && !inFlight && !unconfirmed && !handoffPending && !flowDisabled;
  const requestError = visibleClientError(createDraft.error, t(($) => $.intake.failed));
  const loadError = loadFailed ? visibleClientError(operation.error, "") : "";
  const reconcileError = visibleClientError(reconcile.error, t(($) => $.intake.failed))
    || reconcileOperation.data?.error
    || null;

  useEffect(() => {
    if (!requirementId || navigated.current) return;
    navigated.current = true;
    navigation.push(paths.requirementChat(requirementId));
    onCreated?.();
  }, [navigation, onCreated, paths, requirementId]);

  // A reconcile preview does not carry the requirement id. Refetch the draft so
  // its read path can finish the handoff.
  useEffect(() => {
    if (reconcileOperation.data?.state !== "succeeded" || !operationId || !reconcileOperationId) return;
    if (settledReconcile.current === reconcileOperationId) return;
    settledReconcile.current = reconcileOperationId;
    void queryClient.invalidateQueries({ queryKey: yixiezuoKeys.operation(wsId, operationId) });
  }, [operationId, queryClient, reconcileOperation.data?.state, reconcileOperationId, wsId]);

  const submitReconcile = () => {
    if (!operationId || !reconcileUrl.trim() || reconcile.isPending) return;
    const failed = reconcileOperation.data?.state === "failed";
    const key = failed ? createSafeId() : (reconcileKey.current ?? createSafeId());
    reconcileKey.current = key;
    reconcile.mutate({ url: reconcileUrl.trim(), idempotencyKey: key });
  };

  const submitIdea = async (event: FormEvent) => {
    event.preventDefault();
    if (unconfirmed) {
      submitReconcile();
      return;
    }
    if (!canSubmit || submitting.current) return;
    submitting.current = true;
    // A recorded failure will not create again under the same key. A thrown
    // request may already have queued the draft, so that key stays on the dialog.
    const key = operation.data?.state === "failed" && !attemptKey
      ? createSafeId()
      : (attemptKey || createSafeId());
    const sent = { idea: trimmed, agentId, key, operationId: "" };
    updateAttempt(sent);
    try {
      const row = await createDraft.mutateAsync({
        title: requirementIdeaTitle(trimmed),
        body: trimmed,
        agentId,
        idempotencyKey: key,
      });
      updateAttempt({ ...sent, key: "", operationId: row.id });
    } catch {
      updateAttempt(sent);
    } finally {
      submitting.current = false;
    }
  };

  return (
    <form className="space-y-4" onSubmit={submitIdea}>
      <div className="space-y-2">
        <Label htmlFor="requirement-idea">{t(($) => $.intake.idea)}</Label>
        <Textarea
          id="requirement-idea"
          value={idea}
          onChange={(event) => {
            if (attemptKey && !operationId) return;
            const next = { ...attempt, idea: event.target.value };
            if (!inFlight && !unconfirmed && !handoffPending && operationState === "failed") {
              next.key = "";
              next.operationId = "";
              createDraft.reset();
            }
            updateAttempt(next);
          }}
          disabled={locked}
          placeholder={t(($) => $.intake.idea_placeholder)}
          aria-describedby="requirement-idea-hint"
          className="min-h-28"
          autoFocus
        />
        <p id="requirement-idea-hint" className="text-caption text-muted-foreground">
          {t(($) => $.intake.idea_hint)}
        </p>
        {tooLong && <p role="alert" className="text-caption text-destructive">{t(($) => $.intake.too_long)}</p>}
      </div>
      <RequirementAgentField
        id="requirement-idea-agent"
        agents={agents}
        agentId={agentId}
        onChange={(nextAgentId) => {
          if (attemptKey && !operationId) return;
          updateAttempt({ ...attempt, agentId: nextAgentId });
        }}
        disabled={locked || createDraft.isPending}
      />
      {inFlight && <p role="status" className="text-caption">{t(($) => $.intake.waiting)}</p>}
      {unconfirmed && (
        <div className="space-y-3">
          <p role="alert" className="text-caption text-destructive">{t(($) => $.intake.unknown)}</p>
          {loadError && <p className="text-caption text-muted-foreground">{loadError}</p>}
          {loadFailed && (
            <Button type="button" variant="outline" onClick={() => void operation.refetch()} disabled={operation.isFetching}>
              {t(($) => $.intake.check_again)}
            </Button>
          )}
          <div className="space-y-1">
            <p className="text-caption text-muted-foreground">{t(($) => $.intake.marker)}</p>
            {/* eslint-disable-next-line i18next/no-literal-string -- matches the server's yixiezuo.DraftMarker token */}
            <p className="break-all font-mono text-caption">[Multica draft {operationId}]</p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="requirement-reconcile-url">{t(($) => $.intake.reconcile_url)}</Label>
            <Input
              id="requirement-reconcile-url"
              type="url"
              value={reconcileUrl}
              onChange={(event) => {
                setReconcileUrl(event.target.value);
                reconcileKey.current = null;
                reconcile.reset();
              }}
              disabled={reconcile.isPending || ["pending", "running"].includes(reconcileOperation.data?.state ?? "")}
              placeholder="https://your-instance.pm.netease.com/issues/12345"
            />
            <Button
              type="button"
              variant="outline"
              disabled={!reconcileUrl.trim() || reconcile.isPending}
              aria-busy={reconcile.isPending}
              onClick={submitReconcile}
            >
              {reconcile.isPending ? t(($) => $.intake.reconciling) : t(($) => $.intake.reconcile)}
            </Button>
          </div>
          {reconcileError && <p role="alert" className="text-caption text-destructive">{reconcileError}</p>}
        </div>
      )}
      {handoffPending && (
        <div className="space-y-2">
          <p role="status" className="text-caption">{t(($) => $.intake.handoff_pending)}</p>
          <Button type="button" variant="outline" onClick={() => void operation.refetch()}>
            {t(($) => $.intake.check_again)}
          </Button>
        </div>
      )}
      {(operationState === "failed" || requestError) && (
        <p role="alert" className="text-caption text-destructive">
          {operation.data?.error || requestError || t(($) => $.intake.failed)}
        </p>
      )}
      {sendUnconfirmed && (
        <div className="space-y-2">
          <p className="text-caption text-muted-foreground">{t(($) => $.intake.send_unconfirmed)}</p>
          <Button
            type="button"
            variant="link"
            className="h-auto px-0 text-muted-foreground"
            onClick={() => updateAttempt({ ...EMPTY_IDEA_ATTEMPT, agentId })}
          >
            {t(($) => $.intake.abandon)}
          </Button>
        </div>
      )}
      <div className="flex flex-wrap items-center gap-3">
        <Button type="submit" disabled={!canSubmit} aria-busy={inFlight}>
          {inFlight ? t(($) => $.intake.creating_draft) : sendUnconfirmed ? t(($) => $.intake.retry) : t(($) => $.intake.create)}
        </Button>
        {!locked && (
          <Button type="button" variant="link" className="h-auto px-0 text-muted-foreground" onClick={onUseExistingSource}>
            {t(($) => $.intake.existing_source)}
          </Button>
        )}
      </div>
    </form>
  );
}

function RequirementSourceForm({
  onCreated,
  onExistingRequirement,
  onStartFromIdea,
}: {
  onCreated?: (detail: RequirementDetail) => void;
  onExistingRequirement?: (requirementId: string) => void;
  onStartFromIdea?: () => void;
}) {
  const { t } = useT("requirements");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const agents = useRequirementAgents(false);
  const [agentId, setAgentId] = useSelectedAgent(agents);
  const { data: projects = [] } = useQuery(projectListOptions(wsId));
  const [url, setUrl] = useState("");
  const [operationId, setOperationId] = useState("");
  const [projectId, setProjectId] = useState("");
  const createKey = useRef<string | null>(null);
  const preview = useMutation({
    mutationFn: () => api.previewYixiezuoIssue(wsId, url.trim()),
    onSuccess: (operation) => setOperationId(operation.id),
  });
  const operation = useQuery(yixiezuoOperationOptions(wsId, operationId));
  const create = useCreateRequirement(wsId);

  const snapshot = operation.data?.state === "succeeded" ? operation.data.snapshot : null;
  const sourceReady = !!snapshot && !!operation.data?.id && !!snapshot.source.host.trim() &&
    !!snapshot.source.id && !!snapshot.source.url && !!snapshot.digest;
  const pendingPreview = preview.isPending || (!!operationId && !operation.data && !operation.isError) ||
    ["pending", "running"].includes(operation.data?.state ?? "");
  const error = preview.error || operation.error || create.error || operation.data?.error;
  const flowDisabled = errorCode(create.error) === "requirement_flow_disabled";

  const previewSource = (event: FormEvent) => {
    event.preventDefault();
    setOperationId("");
    createKey.current = null;
    preview.mutate();
  };
  const createRequirement = async () => {
    if (flowDisabled || !snapshot || !operation.data || !sourceReady || !agentId) return;
    const key = createKey.current ?? createSafeId();
    createKey.current = key;
    try {
      const detail = await create.mutateAsync({
        sourceHost: snapshot.source.host,
        externalId: snapshot.source.id,
        sourceUrl: snapshot.source.url,
        sourceSnapshot: { ...snapshot },
        sourceDigest: snapshot.digest,
        sourceLockVersion: snapshot.lock_version,
        agentId,
        projectId: projectId || null,
        intakeOperationId: operation.data.id,
        idempotencyKey: key,
      });
      onCreated?.(detail);
      navigation.push(paths.requirementChat(detail.id));
    } catch (caught) {
      const existing = existingRequirementId(caught);
      if (existing) {
        onExistingRequirement?.(existing);
        navigation.push(paths.requirementChat(existing));
      }
    }
  };

  return (
    <div className="space-y-4">
      <Button type="button" variant="link" className="h-auto px-0 text-muted-foreground" onClick={onStartFromIdea}>
        {t(($) => $.intake.from_idea)}
      </Button>
      <form className="space-y-2" onSubmit={previewSource}>
        <Label htmlFor="requirement-source-url">{t(($) => $.intake.source_url)}</Label>
        <Input
          id="requirement-source-url"
          type="url"
          required
          value={url}
          onChange={(event) => {
            setUrl(event.target.value);
            setOperationId("");
            createKey.current = null;
            preview.reset();
            create.reset();
          }}
          disabled={pendingPreview || create.isPending || flowDisabled}
          placeholder="https://your-instance.pm.netease.com/issues/12345"
        />
        <Button type="submit" variant="outline" disabled={pendingPreview || create.isPending || flowDisabled || !url.trim()} aria-busy={pendingPreview}>
          {t(($) => $.intake.preview)}
        </Button>
      </form>
      {pendingPreview && <p role="status" className="text-caption">{t(($) => $.intake.previewing)}</p>}
      {error && <p role="alert" className="text-caption text-destructive">{typeof error === "string" ? error : error.message}</p>}
      {snapshot && <>
        <div className="max-h-[45vh] overflow-y-auto rounded-lg border border-border p-3">
          <YixiezuoSourcePreview snapshot={snapshot} />
        </div>
        <div className="grid gap-3 sm:grid-cols-2">
          <RequirementAgentField
            id="requirement-agent"
            agents={agents}
            agentId={agentId}
            onChange={setAgentId}
            disabled={create.isPending}
          />
          <div className="space-y-2">
            <Label htmlFor="requirement-project">{t(($) => $.intake.project)}</Label>
            <select id="requirement-project" className="h-9 w-full rounded-md border border-input bg-background px-3 text-body" value={projectId} onChange={(event) => setProjectId(event.target.value)} disabled={create.isPending}>
              <option value="">{t(($) => $.intake.no_project)}</option>
              {projects.map((project) => <option key={project.id} value={project.id}>{project.title}</option>)}
            </select>
          </div>
        </div>
        <Button type="button" disabled={!sourceReady || !agentId || create.isPending || flowDisabled} aria-busy={create.isPending} onClick={() => void createRequirement()}>
          {create.isPending ? t(($) => $.intake.creating) : t(($) => $.intake.create)}
        </Button>
      </>}
    </div>
  );
}

export function RequirementIntakeForm({
  active = true,
  attempt,
  onAttemptChange,
  sourceMode: controlledSourceMode,
  onSourceModeChange,
  onCreated,
  onExistingRequirement,
}: {
  active?: boolean;
  attempt?: IdeaAttempt;
  onAttemptChange?: (attempt: IdeaAttempt) => void;
  sourceMode?: boolean;
  onSourceModeChange?: (sourceMode: boolean) => void;
  onCreated?: (detail?: RequirementDetail) => void;
  onExistingRequirement?: (requirementId: string) => void;
}) {
  const [uncontrolledSourceMode, setUncontrolledSourceMode] = useState(false);
  const sourceMode = controlledSourceMode ?? uncontrolledSourceMode;
  const setSourceMode = (next: boolean) => {
    setUncontrolledSourceMode(next);
    onSourceModeChange?.(next);
  };
  useEffect(() => {
    if (active) return;
    setUncontrolledSourceMode(false);
    onSourceModeChange?.(false);
  }, [active, onSourceModeChange]);

  return sourceMode ? (
    <RequirementSourceForm
      onCreated={onCreated}
      onExistingRequirement={onExistingRequirement}
      onStartFromIdea={() => setSourceMode(false)}
    />
  ) : (
    <RequirementIdeaForm
      attempt={attempt}
      onAttemptChange={onAttemptChange}
      onCreated={onCreated}
      onUseExistingSource={() => setSourceMode(true)}
    />
  );
}

/** Full-width list action, aligned with the conversation rows below it. */
export function RequirementIntakeTrigger() {
  const { t } = useT("requirements");
  return (
    <DialogTrigger
      render={
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="w-full justify-start font-normal text-muted-foreground hover:bg-accent/50 hover:text-foreground"
        />
      }
    >
      <PenLine aria-hidden />
      {t(($) => $.intake.title)}
    </DialogTrigger>
  );
}

export function RequirementIntakeDialog({
  children,
  open: controlledOpen,
  onOpenChange,
  showTrigger = true,
}: {
  children?: ReactNode;
  /** Controlled by a host that opens the dialog from its own menu. */
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  showTrigger?: boolean;
}) {
  const { t } = useT("requirements");
  const [uncontrolledOpen, setUncontrolledOpen] = useState(false);
  const [attempt, setAttempt] = useState(EMPTY_IDEA_ATTEMPT);
  const [sourceMode, setSourceMode] = useState(false);
  const [intakeGeneration, setIntakeGeneration] = useState(0);
  const open = controlledOpen ?? uncontrolledOpen;
  const setOpen = (next: boolean) => {
    if (!next) setSourceMode(false);
    if (onOpenChange) onOpenChange(next);
    else setUncontrolledOpen(next);
  };
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      {children ?? (showTrigger ? <RequirementIntakeTrigger /> : null)}
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t(($) => $.intake.title)}</DialogTitle>
          {sourceMode && <DialogDescription>{t(($) => $.intake.source_hint)}</DialogDescription>}
        </DialogHeader>
        <RequirementIntakeForm
          key={intakeGeneration}
          active={open}
          attempt={attempt}
          onAttemptChange={setAttempt}
          sourceMode={sourceMode}
          onSourceModeChange={setSourceMode}
          onCreated={() => {
            setAttempt(EMPTY_IDEA_ATTEMPT);
            setIntakeGeneration((generation) => generation + 1);
            setOpen(false);
          }}
          onExistingRequirement={() => setOpen(false)}
        />
      </DialogContent>
    </Dialog>
  );
}

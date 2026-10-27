"use client";

import { useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { api, ApiError, errorCode } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { createSafeId } from "@multica/core/utils";
import { agentListOptions, memberListOptions } from "@multica/core/workspace/queries";
import { projectListOptions } from "@multica/core/projects";
import { yixiezuoOperationOptions } from "@multica/core/yixiezuo";
import { useCreateRequirement, type RequirementDetail } from "@multica/core/requirements";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@multica/ui/components/ui/dialog";
import { canAssignAgent } from "../../issues/components/pickers/assignee-picker";
import { YixiezuoSourcePreview } from "../../issues/components/yixiezuo-import";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";

function existingRequirementId(error: unknown): string | null {
  if (!(error instanceof ApiError) || errorCode(error) !== "requirement_source_already_exists") return null;
  if (!error.body || typeof error.body !== "object") return null;
  const value = (error.body as { existing_requirement_id?: unknown }).existing_requirement_id;
  return typeof value === "string" && value.trim().length > 0 ? value : null;
}

export function RequirementIntakeForm({
  onCreated,
  onExistingRequirement,
}: {
  onCreated?: (detail: RequirementDetail) => void;
  onExistingRequirement?: (requirementId: string) => void;
}) {
  const { t } = useT("requirements");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const user = useAuthStore((state) => state.user);
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: projects = [] } = useQuery(projectListOptions(wsId));
  const [url, setUrl] = useState("");
  const [operationId, setOperationId] = useState("");
  const [agentId, setAgentId] = useState("");
  const [projectId, setProjectId] = useState("");
  const createKey = useRef<string | null>(null);
  const preview = useMutation({
    mutationFn: () => api.previewYixiezuoIssue(wsId, url.trim()),
    onSuccess: (operation) => setOperationId(operation.id),
  });
  const operation = useQuery(yixiezuoOperationOptions(wsId, operationId));
  const create = useCreateRequirement(wsId);
  const memberRole = members.find((member) => member.user_id === user?.id)?.role;
  const availableAgents = useMemo(
    () => agents.filter((agent) => !agent.archived_at && canAssignAgent(agent, user?.id, memberRole)),
    [agents, user?.id, memberRole],
  );
  useEffect(() => {
    if (availableAgents.some((agent) => agent.id === agentId)) return;
    setAgentId(availableAgents[0]?.id ?? "");
  }, [availableAgents, agentId]);

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
    } catch (error) {
      const existing = existingRequirementId(error);
      if (existing) {
        onExistingRequirement?.(existing);
        navigation.push(paths.requirementChat(existing));
      }
      // Otherwise keep the preview and key so an explicit retry cannot duplicate creation.
    }
  };

  return (
    <div className="space-y-4">
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
          <div className="space-y-2">
            <Label htmlFor="requirement-agent">{t(($) => $.intake.agent)}</Label>
            <select id="requirement-agent" className="h-9 w-full rounded-md border border-input bg-background px-3 text-body" value={agentId} onChange={(event) => setAgentId(event.target.value)} disabled={create.isPending}>
              {availableAgents.map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}
            </select>
          </div>
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

export function RequirementIntakeTrigger() {
  const { t } = useT("requirements");
  return (
    <DialogTrigger render={<Button type="button" size="sm" variant="outline" />}>
      {t(($) => $.intake.title)}
    </DialogTrigger>
  );
}

export function RequirementIntakeDialog({ children }: { children?: ReactNode }) {
  const { t } = useT("requirements");
  const [open, setOpen] = useState(false);
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      {children ?? <RequirementIntakeTrigger />}
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader><DialogTitle>{t(($) => $.intake.title)}</DialogTitle></DialogHeader>
        <RequirementIntakeForm onCreated={() => setOpen(false)} onExistingRequirement={() => setOpen(false)} />
      </DialogContent>
    </Dialog>
  );
}

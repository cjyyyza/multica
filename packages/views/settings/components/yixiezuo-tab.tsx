"use client";

import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Textarea } from "@multica/ui/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
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
import { useWorkspaceId } from "@multica/core/hooks";
import { projectListOptions } from "@multica/core/projects";
import { yixiezuoConnectionOptions, yixiezuoKeys } from "@multica/core/yixiezuo";
import { api } from "@multica/core/api";
import { useT } from "../../i18n";

const NONE_PROJECT = "__none__";

export function statusMapToText(map: Record<string, string>): string {
  return Object.entries(map)
    .filter(([key, value]) => key.trim() && value.trim())
    .map(([key, value]) => `${key}=${value}`)
    .join("\n");
}

export function parseStatusMapText(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const eq = trimmed.indexOf("=");
    if (eq <= 0) continue;
    const key = trimmed.slice(0, eq).trim();
    const value = trimmed.slice(eq + 1).trim();
    if (key && value) out[key] = value;
  }
  return out;
}

export function YixiezuoTab() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const { data, isLoading } = useQuery({
    ...yixiezuoConnectionOptions(wsId),
    enabled: !!wsId,
  });
  const { data: projects = [] } = useQuery({
    ...projectListOptions(wsId),
    enabled: !!wsId,
  });
  const connection = data?.connection ?? null;
  const canManage = data?.can_manage === true;

  const [cliBin, setCliBin] = useState("popo-cli");
  const [gcpHost, setGcpHost] = useState("");
  const [listQueryId, setListQueryId] = useState("");
  const [externalProjectId, setExternalProjectId] = useState("");
  const [trackerId, setTrackerId] = useState("");
  const [projectId, setProjectId] = useState(NONE_PROJECT);
  const [statusMapText, setStatusMapText] = useState("");
  const [saving, setSaving] = useState(false);
  const [disconnectOpen, setDisconnectOpen] = useState(false);
  const [disconnecting, setDisconnecting] = useState(false);

  useEffect(() => {
    setCliBin(connection?.cli_bin || "popo-cli");
    setGcpHost(connection?.gcp_host ?? "");
    setListQueryId(connection?.list_query_id ?? "");
    setExternalProjectId(connection?.external_project_id ?? "");
    setTrackerId(connection?.tracker_id ?? "");
    setProjectId(connection?.project_id || NONE_PROJECT);
    setStatusMapText(statusMapToText(connection?.status_map ?? {}));
  }, [connection]);

  async function handleSave() {
    if (!canManage || saving) return;
    if (!listQueryId.trim()) {
      toast.error(t(($) => $.yixiezuo.toast_query_required));
      return;
    }
    setSaving(true);
    try {
      await api.upsertYixiezuoConnection(wsId, {
        cli_bin: cliBin.trim() || "popo-cli",
        gcp_host: gcpHost.trim(),
        list_query_id: listQueryId.trim(),
        external_project_id: externalProjectId.trim(),
        tracker_id: trackerId.trim(),
        project_id: projectId === NONE_PROJECT ? null : projectId,
        status_map: parseStatusMapText(statusMapText),
      });
      await qc.invalidateQueries({ queryKey: yixiezuoKeys.connection(wsId) });
      toast.success(t(($) => $.yixiezuo.toast_saved));
    } catch (error) {
      toast.error(
        error instanceof Error ? error.message : t(($) => $.yixiezuo.toast_save_failed),
      );
    } finally {
      setSaving(false);
    }
  }

  async function handleDisconnect() {
    if (!canManage || disconnecting) return;
    setDisconnecting(true);
    try {
      await api.deleteYixiezuoConnection(wsId);
      await qc.invalidateQueries({ queryKey: yixiezuoKeys.connection(wsId) });
      toast.success(t(($) => $.yixiezuo.toast_disconnected));
      setDisconnectOpen(false);
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.yixiezuo.toast_disconnect_failed),
      );
    } finally {
      setDisconnecting(false);
    }
  }

  if (isLoading) {
    return (
      <p className="text-body text-muted-foreground">{t(($) => $.integrations.status_loading)}</p>
    );
  }

  if (!canManage && !connection) {
    return (
      <p className="text-caption text-muted-foreground">{t(($) => $.yixiezuo.contact_admin)}</p>
    );
  }

  const projectItems = [
    { value: NONE_PROJECT, label: t(($) => $.yixiezuo.project_none) },
    ...projects.map((project) => ({ value: project.id, label: project.title })),
  ];

  return (
    <div className="space-y-8">
      <Card>
        <CardContent className="space-y-2">
          <p className="text-body text-muted-foreground">{t(($) => $.yixiezuo.local_note)}</p>
          <code className="block rounded-md bg-muted px-2 py-1.5 text-caption">
            {t(($) => $.yixiezuo.sync_command)}
          </code>
        </CardContent>
      </Card>

      <Card>
        <CardContent className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="yixiezuo-cli">{t(($) => $.yixiezuo.cli_bin_label)}</Label>
            <Input
              id="yixiezuo-cli"
              value={cliBin}
              onChange={(event) => setCliBin(event.target.value)}
              disabled={!canManage || saving}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="yixiezuo-host">{t(($) => $.yixiezuo.gcp_host_label)}</Label>
            <Input
              id="yixiezuo-host"
              value={gcpHost}
              onChange={(event) => setGcpHost(event.target.value)}
              disabled={!canManage || saving}
            />
            <p className="text-caption text-muted-foreground">
              {t(($) => $.yixiezuo.gcp_host_help)}
            </p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="yixiezuo-external-project">{t(($) => $.yixiezuo.external_project_label)}</Label>
            <Input
              id="yixiezuo-external-project"
              value={externalProjectId}
              onChange={(event) => setExternalProjectId(event.target.value)}
              disabled={!canManage || saving}
            />
            <p className="text-caption text-muted-foreground">
              {t(($) => $.yixiezuo.external_project_help)}
            </p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="yixiezuo-query">{t(($) => $.yixiezuo.list_query_label)}</Label>
            <Input
              id="yixiezuo-query"
              value={listQueryId}
              onChange={(event) => setListQueryId(event.target.value)}
              disabled={!canManage || saving}
              required
            />
            <p className="text-caption text-muted-foreground">
              {t(($) => $.yixiezuo.list_query_help)}
            </p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="yixiezuo-tracker">{t(($) => $.yixiezuo.tracker_id_label)}</Label>
            <Input
              id="yixiezuo-tracker"
              value={trackerId}
              onChange={(event) => setTrackerId(event.target.value)}
              disabled={!canManage || saving}
            />
            <p className="text-caption text-muted-foreground">
              {t(($) => $.yixiezuo.tracker_id_help)}
            </p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="yixiezuo-project">{t(($) => $.yixiezuo.project_label)}</Label>
            <Select
              items={projectItems}
              value={projectId}
              onValueChange={(value) => {
                if (value) setProjectId(value);
              }}
            >
              <SelectTrigger id="yixiezuo-project" disabled={!canManage || saving}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {projectItems.map((item) => (
                  <SelectItem key={item.value} value={item.value}>
                    {item.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-caption text-muted-foreground">
              {t(($) => $.yixiezuo.project_help)}
            </p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="yixiezuo-status-map">{t(($) => $.yixiezuo.status_map_label)}</Label>
            <Textarea
              id="yixiezuo-status-map"
              value={statusMapText}
              onChange={(event) => setStatusMapText(event.target.value)}
              placeholder={t(($) => $.yixiezuo.status_map_placeholder)}
              disabled={!canManage || saving}
            />
            <p className="text-caption text-muted-foreground">
              {t(($) => $.yixiezuo.status_map_help)}
            </p>
          </div>
          {connection?.last_pulled_at ? (
            <p className="text-caption text-muted-foreground">
              {t(($) => $.yixiezuo.last_pulled, { time: connection.last_pulled_at })}
            </p>
          ) : null}
          {connection?.last_pushed_at ? (
            <p className="text-caption text-muted-foreground">
              {t(($) => $.yixiezuo.last_pushed, { time: connection.last_pushed_at })}
            </p>
          ) : null}
          {canManage ? (
            <div className="flex justify-end gap-2">
              {connection ? (
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => setDisconnectOpen(true)}
                  disabled={saving || disconnecting}
                >
                  {t(($) => $.yixiezuo.disconnect)}
                </Button>
              ) : null}
              <Button
                type="button"
                size="sm"
                onClick={handleSave}
                disabled={saving || !listQueryId.trim()}
              >
                {saving ? t(($) => $.yixiezuo.saving) : t(($) => $.yixiezuo.save)}
              </Button>
            </div>
          ) : null}
        </CardContent>
      </Card>

      <AlertDialog
        open={disconnectOpen}
        onOpenChange={(open) => {
          if (!open && !disconnecting) setDisconnectOpen(false);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.yixiezuo.disconnect_confirm_title)}</AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.yixiezuo.disconnect_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnecting}>
              {t(($) => $.yixiezuo.disconnect_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleDisconnect} disabled={disconnecting}>
              {disconnecting
                ? t(($) => $.yixiezuo.disconnecting)
                : t(($) => $.yixiezuo.disconnect_confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

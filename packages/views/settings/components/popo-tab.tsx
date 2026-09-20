"use client";

import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Check, ChevronRight, Copy, RefreshCw, Trash2 } from "lucide-react";
// Named import, NOT default: react-qr-code is CJS; electron-vite's default
// import interop hands back the module namespace (see lark-tab.tsx).
import { QRCode } from "react-qr-code";
import { PopoMark } from "./popo-mark";
import { cn } from "@multica/ui/lib/utils";
import { copyText } from "@multica/ui/lib/clipboard";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
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
import { ApiError, api, errorCode } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions } from "@multica/core/workspace/queries";
import { useActorName } from "@multica/core/workspace/hooks";
import {
  popoBridgesOptions,
  popoInstallationsOptions,
  popoKeys,
  popoStatusOptions,
} from "@multica/core/popo";
import {
  isIdlePopoRobot,
  type PopoBridge,
  type PopoBridgePairing,
  type PopoBridgeRobot,
  type PopoInstallation,
  type PopoRegistration,
  type PopoStatusBridge,
} from "@multica/core/types";
import { ActorAvatar } from "../../common/actor-avatar";
import { useLocale, useT, useTimeAgo } from "../../i18n";

const SELECT_CLASS =
  "h-8 w-full min-w-0 rounded-lg border border-input bg-transparent px-2.5 py-1 text-title-sm outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 md:text-body dark:bg-input/30";

function isPopoNotConfiguredError(error: unknown): boolean {
  if (!(error instanceof ApiError)) return false;
  return error.status === 503 || errorCode(error) === "popo_not_configured";
}

function popoServerOrigin(): string {
  const base = typeof api.getBaseUrl === "function" ? api.getBaseUrl() : "";
  const trimmed = base.replace(/\/$/, "");
  if (trimmed) {
    try {
      return new URL(trimmed).origin;
    } catch {
      return trimmed;
    }
  }
  if (typeof window !== "undefined" && window.location?.origin) {
    return window.location.origin;
  }
  return "";
}

export function popoPairCommand(origin: string, pairingCode: string): string {
  return `python -m nanobot.multica_bridge pair --server ${origin} --pairing-code ${pairingCode}`;
}

function onlineActiveBridges(bridges: PopoBridge[]): PopoBridge[] {
  return bridges.filter((b) => b.online && b.status === "active");
}

function idleRobotsOn(
  bridge: PopoBridge | undefined,
  agentId?: string,
  installations: PopoInstallation[] = [],
): PopoBridgeRobot[] {
  const taken = new Set(
    installations
      .filter((inst) => inst.status === "active" && inst.agent_id !== agentId)
      .map((inst) => inst.robot_id),
  );
  return (bridge?.robots ?? []).filter(
    (robot) => isIdlePopoRobot(robot) && !taken.has(robot.robot_id),
  );
}

const POPO_ENABLE_SETTING = "MULTICA_POPO_ENABLED=true";

export function PopoTab() {
  const { t } = useT("settings");
  const locale = useLocale();
  const timeAgo = useTimeAgo();
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const user = useAuthStore((s) => s.user);

  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  const canManage =
    currentMember?.role === "owner" || currentMember?.role === "admin";

  const bridgesQuery = useQuery({
    ...popoBridgesOptions(wsId),
    enabled: !!wsId,
  });
  const installsQuery = useQuery({
    ...popoInstallationsOptions(wsId),
    enabled: !!wsId,
  });
  const statusQuery = useQuery({
    ...popoStatusOptions(wsId),
    enabled: !!wsId,
  });

  const bridges = bridgesQuery.data?.bridges ?? [];
  const installations = installsQuery.data?.installations ?? [];
  const statusById = new Map(
    (statusQuery.data?.bridges ?? []).map((row) => [row.id, row]),
  );
  const notConfigured =
    isPopoNotConfiguredError(bridgesQuery.error) ||
    isPopoNotConfiguredError(installsQuery.error) ||
    isPopoNotConfiguredError(statusQuery.error) ||
    bridgesQuery.data?.configured === false ||
    statusQuery.data?.configured === false ||
    (bridgesQuery.data == null && installsQuery.data?.configured === false);
  const isLoading =
    (bridgesQuery.isLoading && bridgesQuery.data == null) ||
    (installsQuery.isLoading && installsQuery.data == null);
  const loadFailed =
    (bridgesQuery.isError && !isPopoNotConfiguredError(bridgesQuery.error)) ||
    (installsQuery.isError && !isPopoNotConfiguredError(installsQuery.error));

  const [pairing, setPairing] = useState<PopoBridgePairing | null>(null);
  const [creatingPairing, setCreatingPairing] = useState(false);
  const [pairingCopied, setPairingCopied] = useState(false);
  const [revokeTarget, setRevokeTarget] = useState<string | null>(null);
  const [revoking, setRevoking] = useState(false);
  const [disconnectTarget, setDisconnectTarget] = useState<string | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);

  function closePairingDialog() {
    setPairing(null);
    setPairingCopied(false);
  }

  async function handleCreatePairing() {
    if (creatingPairing) return;
    setCreatingPairing(true);
    try {
      const created = await api.createPopoBridgePairing(wsId);
      if (!created.pairing_code) {
        throw new Error(t(($) => $.popo.pairing_create_failed));
      }
      setPairingCopied(false);
      setPairing(created);
      await qc.invalidateQueries({ queryKey: popoKeys.all(wsId) });
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t(($) => $.popo.pairing_create_failed),
      );
    } finally {
      setCreatingPairing(false);
    }
  }

  async function handleCopyPairCommand() {
    if (!pairing?.pairing_code) return;
    const command = popoPairCommand(popoServerOrigin(), pairing.pairing_code);
    if (await copyText(command)) {
      setPairingCopied(true);
    }
  }

  async function handleRevoke() {
    if (!revokeTarget || revoking) return;
    setRevoking(true);
    try {
      await api.revokePopoBridge(wsId, revokeTarget);
      await qc.invalidateQueries({ queryKey: popoKeys.all(wsId) });
      toast.success(t(($) => $.popo.toast_bridge_revoked));
      setRevokeTarget(null);
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t(($) => $.popo.toast_bridge_revoke_failed),
      );
    } finally {
      setRevoking(false);
    }
  }

  async function handleDisconnect() {
    if (!disconnectTarget || disconnecting) return;
    setDisconnecting(true);
    try {
      await api.deletePopoInstallation(wsId, disconnectTarget);
      await qc.invalidateQueries({ queryKey: popoKeys.all(wsId) });
      toast.success(t(($) => $.popo.toast_disconnected));
      setDisconnectTarget(null);
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t(($) => $.popo.toast_disconnect_failed),
      );
    } finally {
      setDisconnecting(false);
    }
  }

  const pairingCommand = pairing
    ? popoPairCommand(popoServerOrigin(), pairing.pairing_code)
    : "";
  const pairingExpires =
    pairing?.expires_at && !Number.isNaN(Date.parse(pairing.expires_at))
      ? new Date(pairing.expires_at).toLocaleString(locale)
      : "";

  return (
    <div className="space-y-8">
      {loadFailed && !notConfigured ? (
        <Card>
          <CardContent>
            <p className="text-body text-muted-foreground">
              {t(($) => $.popo.load_failed)}
            </p>
          </CardContent>
        </Card>
      ) : isLoading ? (
        <Card>
          <CardContent>
            <p className="text-body text-muted-foreground">{t(($) => $.popo.loading)}</p>
          </CardContent>
        </Card>
      ) : notConfigured ? (
        <Card>
          <CardContent className="space-y-2">
            <p className="text-body font-medium">{t(($) => $.popo.not_enabled_title)}</p>
            <p className="text-caption text-muted-foreground">
              {t(($) => $.popo.not_enabled_description_prefix)}{" "}
              <code className="rounded-xs bg-muted px-1 py-0.5 text-micro">
                {POPO_ENABLE_SETTING}
              </code>{" "}
              {t(($) => $.popo.not_enabled_description_suffix)}{" "}
              {t(($) => $.popo.not_enabled_self_host_hint)}
            </p>
          </CardContent>
        </Card>
      ) : (
        <>
          <section className="space-y-3">
            <div className="flex items-center justify-between gap-3">
              <div className="min-w-0 space-y-1">
                <h2 className="text-body font-semibold">{t(($) => $.popo.bridges_title)}</h2>
                {statusQuery.data ? (
                  <p
                    className="text-caption text-muted-foreground"
                    data-testid="popo-diag-runtime"
                  >
                    {statusQuery.data.runtime_online
                      ? t(($) => $.popo.runtime_online)
                      : t(($) => $.popo.runtime_offline)}
                  </p>
                ) : null}
              </div>
              {canManage && (
                <Button
                  size="sm"
                  onClick={() => void handleCreatePairing()}
                  disabled={creatingPairing}
                  data-testid="popo-create-pairing"
                >
                  {creatingPairing
                    ? t(($) => $.popo.pairing_creating)
                    : t(($) => $.popo.create_pairing)}
                </Button>
              )}
            </div>
            {bridges.length === 0 ? (
              <Card>
                <CardContent>
                  <p className="text-body font-medium">{t(($) => $.popo.bridges_empty)}</p>
                </CardContent>
              </Card>
            ) : (
              <Card>
                <CardContent className="divide-y">
                  {bridges.map((bridge) => (
                    <BridgeRow
                      key={bridge.id}
                      bridge={bridge}
                      diagnostics={statusById.get(bridge.id)}
                      canManage={canManage}
                      lastSeen={
                        bridge.last_heartbeat_at
                          ? t(($) => $.popo.bridge_last_seen, {
                              when: timeAgo(bridge.last_heartbeat_at),
                            })
                          : t(($) => $.popo.bridge_last_seen_never)
                      }
                      onRevoke={() => setRevokeTarget(bridge.id)}
                    />
                  ))}
                </CardContent>
              </Card>
            )}
          </section>

          <section className="space-y-3">
            <h2 className="text-body font-semibold">{t(($) => $.popo.connected_bots)}</h2>
            {installations.length === 0 ? (
              <Card>
                <CardContent className="space-y-2">
                  <p className="text-body font-medium">{t(($) => $.popo.empty_title)}</p>
                  <p className="text-caption text-muted-foreground">
                    {t(($) => $.popo.empty_description_prefix)}{" "}
                    <strong>{t(($) => $.popo.empty_description_cta)}</strong>{" "}
                    {t(($) => $.popo.empty_description_suffix)}
                  </p>
                </CardContent>
              </Card>
            ) : (
              <Card>
                <CardContent className="divide-y">
                  {installations.map((inst) => (
                    <InstallationRow
                      key={inst.id}
                      installation={inst}
                      canManage={canManage}
                      onDisconnect={() => setDisconnectTarget(inst.id)}
                    />
                  ))}
                </CardContent>
              </Card>
            )}
          </section>
        </>
      )}

      <Dialog
        open={!!pairing}
        onOpenChange={(open) => {
          if (!open) closePairingDialog();
        }}
      >
        <DialogContent className="sm:max-w-lg" data-testid="popo-pairing-dialog">
          <DialogHeader>
            <DialogTitle>{t(($) => $.popo.pairing_dialog_title)}</DialogTitle>
          </DialogHeader>
          {pairing ? (
            <div className="space-y-3">
              <code
                className="block break-all rounded-md border bg-muted/50 px-3 py-2 text-body select-all"
                data-testid="popo-pairing-code"
              >
                {pairing.pairing_code}
              </code>
              {pairingExpires ? (
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.popo.pairing_expires, { when: pairingExpires })}
                </p>
              ) : null}
              <p className="text-caption text-muted-foreground">
                {t(($) => $.popo.pairing_command_hint)}
              </p>
              <code
                className="block whitespace-pre-wrap break-all rounded-md border bg-muted/50 px-3 py-2 text-caption select-all"
                data-testid="popo-pairing-command"
              >
                {pairingCommand}
              </code>
            </div>
          ) : null}
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => void handleCopyPairCommand()}
              data-testid="popo-pairing-copy"
            >
              {pairingCopied ? <Check className="h-3 w-3" /> : <Copy className="h-3 w-3" />}
              {pairingCopied
                ? t(($) => $.popo.pairing_copied)
                : t(($) => $.popo.pairing_copy)}
            </Button>
            <Button
              type="button"
              size="sm"
              onClick={closePairingDialog}
              data-testid="popo-pairing-close"
            >
              {t(($) => $.popo.pairing_done)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <AlertDialog
        open={!!revokeTarget}
        onOpenChange={(v) => {
          if (!v && !revoking) setRevokeTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.popo.revoke_bridge_title)}</AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.popo.revoke_bridge_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={revoking}>
              {t(($) => $.popo.revoke_bridge_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={handleRevoke}
              disabled={revoking}
            >
              {revoking
                ? t(($) => $.popo.revoking_bridge)
                : t(($) => $.popo.revoke_bridge)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog
        open={!!disconnectTarget}
        onOpenChange={(v) => {
          if (!v && !disconnecting) setDisconnectTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.popo.disconnect_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.popo.disconnect_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnecting}>
              {t(($) => $.popo.disconnect_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleDisconnect} disabled={disconnecting}>
              {disconnecting
                ? t(($) => $.popo.disconnecting)
                : t(($) => $.popo.disconnect)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function BridgeRow({
  bridge,
  diagnostics,
  canManage,
  lastSeen,
  onRevoke,
}: {
  bridge: PopoBridge;
  diagnostics?: PopoStatusBridge;
  canManage: boolean;
  lastSeen: string;
  onRevoke: () => void;
}) {
  const { t } = useT("settings");
  const isRevoked = bridge.status === "revoked";
  const host = bridge.hostname || bridge.id;
  const hostOnline = diagnostics?.online ?? bridge.online;
  const popoConnected = diagnostics?.popo_connected === true;
  return (
    <div
      className="flex items-start justify-between gap-4 py-3 first:pt-0 last:pb-0"
      data-testid="popo-bridge-row"
    >
      <div className="min-w-0 space-y-1">
        <p className="text-body font-medium">
          {host}
          <span
            className={cn(
              "ml-2 rounded-xs px-1.5 py-0.5 text-micro",
              hostOnline && !isRevoked
                ? "bg-emerald-500/15 text-emerald-700 dark:text-emerald-400"
                : "bg-muted text-muted-foreground",
            )}
            data-testid="popo-diag-host"
          >
            {isRevoked
              ? t(($) => $.popo.bridge_revoked)
              : hostOnline
                ? t(($) => $.popo.bridge_online)
                : t(($) => $.popo.bridge_offline)}
          </span>
        </p>
        <p className="text-micro text-muted-foreground">{lastSeen}</p>
        {diagnostics ? (
          <ul className="flex flex-wrap gap-x-3 gap-y-0.5 text-caption text-muted-foreground">
            <li data-testid="popo-diag-popo">
              {popoConnected
                ? t(($) => $.popo.popo_connected)
                : t(($) => $.popo.popo_disconnected)}
            </li>
            <li
              data-testid="popo-diag-inbound"
              title={t(($) => $.popo.inbound_backlog_hint)}
            >
              {t(($) => $.popo.inbound_backlog, {
                count: diagnostics.inbound_backlog,
              })}
            </li>
            <li data-testid="popo-diag-outbound">
              {t(($) => $.popo.outbound_backlog, {
                count: diagnostics.outbound_backlog,
              })}
            </li>
            <li data-testid="popo-diag-unknown">
              {t(($) => $.popo.unknown_deliveries, {
                count: diagnostics.unknown_deliveries,
              })}
            </li>
          </ul>
        ) : null}
        {bridge.robots.length === 0 ? (
          <p className="text-caption text-muted-foreground">
            {t(($) => $.popo.bridge_robots_none)}
          </p>
        ) : (
          <ul className="space-y-0.5 text-caption text-muted-foreground">
            {bridge.robots.map((robot) => (
              <li key={robot.robot_id || robot.display_name}>
                {robot.display_name || robot.robot_id} ·{" "}
                {!robot.connected
                  ? t(($) => $.popo.bridge_robot_disconnected)
                  : isIdlePopoRobot(robot)
                    ? t(($) => $.popo.bridge_robot_idle)
                    : t(($) => $.popo.bridge_robot_occupied, {
                        occupant: robot.occupied_by,
                      })}
              </li>
            ))}
          </ul>
        )}
      </div>
      {canManage && !isRevoked && (
        <Button
          variant="outline"
          size="sm"
          onClick={onRevoke}
          data-testid="popo-revoke-bridge"
        >
          <Trash2 className="h-3 w-3" />
          {t(($) => $.popo.revoke_bridge)}
        </Button>
      )}
    </div>
  );
}

function InstallationRow({
  installation,
  canManage,
  onDisconnect,
}: {
  installation: PopoInstallation;
  canManage: boolean;
  onDisconnect: () => void;
}) {
  const { t } = useT("settings");
  const locale = useLocale();
  const { getAgentName } = useActorName();
  const isActive = installation.status === "active";
  const agentName = getAgentName(installation.agent_id);
  return (
    <div className="flex items-start justify-between gap-4 py-3 first:pt-0 last:pb-0">
      <div className="flex items-start gap-3">
        <ActorAvatar
          actorType="agent"
          actorId={installation.agent_id}
          size="lg"
          enableHoverCard
          profileLink
        />
        <div className="space-y-1">
          <p className="text-body font-medium">
            {agentName}
            {installation.robot_id ? (
              <span className="ml-2 text-caption text-muted-foreground">
                {installation.robot_name
                  ? `${installation.robot_name} · ${installation.robot_id}`
                  : installation.robot_id}
              </span>
            ) : null}
            {!isActive && (
              <span className="ml-2 rounded-xs bg-muted px-1.5 py-0.5 text-micro text-muted-foreground">
                {t(($) => $.popo.revoked_badge)}
              </span>
            )}
          </p>
          <p className="text-micro text-muted-foreground">
            {t(($) => $.popo.installed_at_label, {
              when: new Date(installation.installed_at).toLocaleString(locale),
            })}
          </p>
        </div>
      </div>
      {canManage && isActive && (
        <Button variant="outline" size="sm" onClick={onDisconnect}>
          <Trash2 className="h-3 w-3" />
          {t(($) => $.popo.disconnect)}
        </Button>
      )}
    </div>
  );
}

export function PopoAgentBindButton({
  agentId,
  agentName,
  agentOwnerId,
  className,
  onShowConnectedDetails,
}: {
  agentId: string;
  agentName?: string;
  /**
   * The bound agent's owner (`agent.owner_id`). When it matches the
   * current user, they can bind/disconnect even if they are not a
   * workspace owner/admin — mirroring canManageAgent on the server.
   */
  agentOwnerId?: string | null;
  className?: string;
  onShowConnectedDetails?: () => void;
}) {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const user = useAuthStore((s) => s.user);

  const [dialogOpen, setDialogOpen] = useState(false);
  const [bridgeId, setBridgeId] = useState("");
  const [robotId, setRobotId] = useState("");
  const [robotName, setRobotName] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [scanning, setScanning] = useState(false);
  const [scan, setScan] = useState<PopoRegistration | null>(null);
  const [scanStatus, setScanStatus] = useState<PopoRegistration["status"]>("pending");
  const [scanErrorReason, setScanErrorReason] = useState<string | null>(null);
  const [scanErrorMessage, setScanErrorMessage] = useState<string | null>(null);
  const closedRef = useRef(false);

  const { data: listing } = useQuery({
    ...popoInstallationsOptions(wsId),
    enabled: !!wsId,
  });
  const { data: bridgeListing } = useQuery({
    ...popoBridgesOptions(wsId),
    enabled: !!wsId,
  });
  const installSupported = listing?.install_supported === true;

  const { data: members = [] } = useQuery({
    ...memberListOptions(wsId),
    enabled: !!wsId,
  });
  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  const isWorkspaceAdmin =
    currentMember?.role === "owner" || currentMember?.role === "admin";
  const isAgentOwner =
    !!user?.id && agentOwnerId != null && agentOwnerId === user.id;
  const canManage = isWorkspaceAdmin || isAgentOwner;

  const onlineBridges = onlineActiveBridges(bridgeListing?.bridges ?? []);
  const selectedBridge = onlineBridges.find((b) => b.id === bridgeId);
  const idleRobots = idleRobotsOn(selectedBridge, agentId, listing?.installations ?? []);

  useEffect(() => {
    if (!dialogOpen) return;
    if (!bridgeId || !onlineBridges.some((b) => b.id === bridgeId)) {
      setBridgeId(onlineBridges[0]?.id ?? "");
    }
  }, [dialogOpen, onlineBridges, bridgeId]);

  useEffect(() => {
    if (!dialogOpen) return;
    if (!robotId || !idleRobots.some((r) => r.robot_id === robotId)) {
      setRobotId(idleRobots[0]?.robot_id ?? "");
    }
  }, [dialogOpen, idleRobots, robotId]);

  useEffect(() => {
    if (!dialogOpen) {
      closedRef.current = true;
      return;
    }
    closedRef.current = false;
  }, [dialogOpen]);

  useEffect(() => {
    if (!scan || (scanStatus !== "pending" && scanStatus !== "awaiting_scan")) return;
    const intervalMs = Math.max(50, (scan.poll_interval_seconds || 2) * 1000);
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | null = null;

    const poll = async () => {
      if (cancelled) return;
      try {
        const res = await api.getPopoRegistration(wsId, scan.id);
        if (cancelled) return;
        setScan(res);
        setScanStatus(res.status);
        if (res.status === "success") {
          await qc.invalidateQueries({ queryKey: popoKeys.all(wsId) });
          toast.success(t(($) => $.popo.connect_success_toast));
          setTimeout(() => {
            if (!cancelled) {
              setDialogOpen(false);
              setBridgeId("");
              setRobotId("");
              setRobotName("");
              setScanning(false);
              setScan(null);
              setScanStatus("pending");
              setScanErrorReason(null);
              setScanErrorMessage(null);
            }
          }, 800);
          return;
        }
        if (res.status === "error" || res.status === "expired") {
          setScanErrorReason(res.error_reason || res.status);
          return;
        }
        timer = setTimeout(poll, intervalMs);
      } catch (e) {
        if (cancelled) return;
        if (e instanceof ApiError) {
          if (e.status === 404) {
            setScanStatus("error");
            setScanErrorReason("session_lost");
            setScanErrorMessage(e.message);
            return;
          }
          if (e.status === 403 || e.status === 401) {
            setScanStatus("error");
            setScanErrorReason("forbidden");
            setScanErrorMessage(e.message);
            return;
          }
        }
        timer = setTimeout(poll, intervalMs);
      }
    };

    timer = setTimeout(poll, intervalMs);
    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
    // Poll from the registration id only. Including scanStatus would
    // re-run this effect on success and cancel the close timeout.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scan?.id]);

  if (!canManage) return null;

  const existing = listing?.installations.find(
    (inst) => inst.agent_id === agentId && inst.status === "active",
  );
  if (existing) {
    return onShowConnectedDetails ? (
      <PopoAgentBotStatusRow
        onClick={onShowConnectedDetails}
        className={className}
      />
    ) : (
      <PopoAgentBotConnectedBadge installation={existing} className={className} />
    );
  }

  if (!installSupported) return null;

  const scanInFlight = scanStatus === "pending" || scanStatus === "awaiting_scan";

  function resetBindFields() {
    setBridgeId("");
    setRobotId("");
    setRobotName("");
  }

  function resetScan() {
    setScanning(false);
    setScan(null);
    setScanStatus("pending");
    setScanErrorReason(null);
    setScanErrorMessage(null);
  }

  function closeDialog() {
    if (submitting) return;
    const registrationId = scan?.id;
    const shouldCancel = !!registrationId && scanInFlight;
    closedRef.current = true;
    setDialogOpen(false);
    resetBindFields();
    resetScan();
    if (shouldCancel) {
      void api.cancelPopoRegistration(wsId, registrationId).catch(() => undefined);
    }
  }

  async function handleSubmit() {
    if (submitting || scanning || !agentId || !bridgeId || !robotId) return;
    setSubmitting(true);
    try {
      const installation = await api.registerPopoBot(wsId, agentId, {
        bridge_id: bridgeId,
        robot_id: robotId,
        robot_name: robotName.trim(),
      });
      if (!installation.id || installation.status !== "active") {
        throw new Error("POPO connection returned an invalid installation");
      }
      await qc.invalidateQueries({ queryKey: popoKeys.all(wsId) });
      toast.success(t(($) => $.popo.connect_success_toast));
      setDialogOpen(false);
      resetBindFields();
      resetScan();
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t(($) => $.popo.connect_failed_toast),
      );
    } finally {
      setSubmitting(false);
    }
  }

  async function beginScan() {
    if (submitting || !agentId || onlineBridges.length === 0) return;
    closedRef.current = false;
    const previousId = scan?.id;
    if (previousId && (scanStatus === "pending" || scanStatus === "awaiting_scan")) {
      void api.cancelPopoRegistration(wsId, previousId).catch(() => undefined);
    }
    setScanning(true);
    setScan(null);
    setScanStatus("pending");
    setScanErrorReason(null);
    setScanErrorMessage(null);
    try {
      const created = await api.createPopoRegistration(
        wsId,
        agentId,
        bridgeId ? { bridge_id: bridgeId } : undefined,
      );
      if (closedRef.current) return;
      if (!created.id) {
        throw new Error(t(($) => $.popo.scan_error_generic));
      }
      setScan(created);
      setScanStatus(created.status);
      if (created.status === "error" || created.status === "expired") {
        setScanErrorReason(created.error_reason || created.status);
      }
    } catch (e) {
      if (closedRef.current) return;
      setScanStatus("error");
      setScanErrorReason(
        e instanceof ApiError && e.status === 409 ? "no_bridge" : "internal_error",
      );
      setScanErrorMessage(e instanceof Error ? e.message : String(e));
    } finally {
      setScanning(false);
    }
  }

  const canSubmit = !!bridgeId && !!robotId && !submitting && !scanning && !scan;
  const scanActive = !!scan && (scanStatus === "pending" || scanStatus === "awaiting_scan");
  const canScan =
    onlineBridges.length > 0 && !submitting && !scanning && !scanActive && scanStatus !== "success";
  const showScan = !!scan || scanning || scanStatus === "error" || scanStatus === "expired";

  return (
    <div
      className={cn("flex flex-wrap items-center gap-2", className)}
      data-testid="popo-agent-bind-buttons"
    >
      <Button
        variant="outline"
        size="sm"
        onClick={() => setDialogOpen(true)}
        disabled={!agentId}
        title={
          agentName
            ? t(($) => $.popo.bind_button_title, { agent: agentName })
            : undefined
        }
        data-testid="popo-agent-connect"
      >
        <PopoMark className="h-3 w-3" />
        {t(($) => $.popo.bind_button)}
      </Button>

      <Dialog
        open={dialogOpen}
        onOpenChange={(v) => (v ? setDialogOpen(true) : closeDialog())}
      >
        <DialogContent className="sm:max-w-lg" data-testid="popo-connect-dialog">
          <DialogHeader>
            <DialogTitle>{t(($) => $.popo.connect_dialog_title)}</DialogTitle>
          </DialogHeader>

          {showScan ? (
            <PopoScanPanel
              scanning={scanning}
              scan={scan}
              status={scanStatus}
              errorReason={scanErrorReason}
              errorMessage={scanErrorMessage}
            />
          ) : onlineBridges.length === 0 ? (
            <p className="text-caption text-muted-foreground">
              {t(($) => $.popo.no_online_bridge)}
            </p>
          ) : (
            <div className="space-y-3">
              <div className="space-y-1.5">
                <Label htmlFor="popo-bridge">{t(($) => $.popo.bridge_label)}</Label>
                <select
                  id="popo-bridge"
                  data-testid="popo-bridge-select"
                  className={SELECT_CLASS}
                  value={bridgeId}
                  onChange={(e) => setBridgeId(e.target.value)}
                  disabled={submitting}
                >
                  {onlineBridges.map((bridge) => (
                    <option key={bridge.id} value={bridge.id}>
                      {bridge.hostname || bridge.id}
                    </option>
                  ))}
                </select>
              </div>

              {idleRobots.length === 0 ? (
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.popo.no_idle_robots)}
                </p>
              ) : (
                <div className="space-y-1.5">
                  <Label htmlFor="popo-robot">{t(($) => $.popo.robot_label)}</Label>
                  <select
                    id="popo-robot"
                    data-testid="popo-robot-select"
                    className={SELECT_CLASS}
                    value={robotId}
                    onChange={(e) => setRobotId(e.target.value)}
                    disabled={submitting}
                  >
                    {idleRobots.map((robot) => (
                      <option key={robot.robot_id} value={robot.robot_id}>
                        {robot.display_name
                          ? `${robot.display_name} · ${robot.robot_id}`
                          : robot.robot_id}
                      </option>
                    ))}
                  </select>
                </div>
              )}

              <div className="space-y-1.5">
                <Label htmlFor="popo-robot-name">
                  {t(($) => $.popo.robot_name_label)}
                </Label>
                <Input
                  id="popo-robot-name"
                  data-testid="popo-robot-name"
                  value={robotName}
                  onChange={(e) => setRobotName(e.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                  disabled={submitting}
                />
              </div>
            </div>
          )}

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={closeDialog}
              disabled={submitting}
            >
              {t(($) => $.popo.connect_cancel)}
            </Button>
            {scanStatus === "error" || scanStatus === "expired" ? (
              <Button
                type="button"
                size="sm"
                onClick={() => void beginScan()}
                disabled={scanning || onlineBridges.length === 0}
                data-testid="popo-scan-retry"
              >
                <RefreshCw className="h-3 w-3" />
                {t(($) => $.popo.scan_retry)}
              </Button>
            ) : (
              <>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => void beginScan()}
                  disabled={!canScan}
                  data-testid="popo-scan-to-create"
                >
                  {scanning
                    ? t(($) => $.popo.scan_starting)
                    : t(($) => $.popo.scan_to_create)}
                </Button>
                {!showScan && (
                  <Button
                    type="button"
                    size="sm"
                    onClick={handleSubmit}
                    disabled={!canSubmit}
                    data-testid="popo-connect-submit"
                  >
                    {submitting
                      ? t(($) => $.popo.connect_submitting)
                      : t(($) => $.popo.connect_submit)}
                  </Button>
                )}
              </>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function PopoScanPanel({
  scanning,
  scan,
  status,
  errorReason,
  errorMessage,
}: {
  scanning: boolean;
  scan: PopoRegistration | null;
  status: PopoRegistration["status"];
  errorReason: string | null;
  errorMessage: string | null;
}) {
  const { t } = useT("settings");
  const qrURL = scan?.qr_url ?? "";
  return (
    <div className="flex flex-col items-center gap-4 py-2" data-testid="popo-scan-panel">
      {scanning && !scan && (
        <p className="text-body text-muted-foreground">{t(($) => $.popo.scan_starting)}</p>
      )}

      {scan && (status === "pending" || status === "awaiting_scan") && !qrURL && (
        <p className="text-caption text-muted-foreground">{t(($) => $.popo.scan_waiting_qr)}</p>
      )}

      {scan && (status === "pending" || status === "awaiting_scan") && qrURL ? (
        <>
          <div className="rounded-md border bg-white p-3" data-testid="popo-scan-qr">
            <QRCode value={qrURL} size={192} />
          </div>
          <p className="text-center text-caption text-muted-foreground">
            {t(($) => $.popo.scan_hint)}
          </p>
          <p className="text-caption text-muted-foreground">{t(($) => $.popo.scan_expires)}</p>
          <a
            href={qrURL}
            target="_blank"
            rel="noopener noreferrer"
            className="text-caption text-muted-foreground underline"
          >
            {t(($) => $.popo.scan_open_link)}
          </a>
        </>
      ) : null}

      {status === "success" && (
        <p className="text-body font-medium">{t(($) => $.popo.scan_success)}</p>
      )}

      {(status === "error" || status === "expired") && (
        <div className="space-y-2 text-center">
          <p className="text-body font-medium text-destructive">
            {(() => {
              switch (errorReason ?? status) {
                case "expired":
                  return t(($) => $.popo.scan_error_expired);
                case "denied":
                  return t(($) => $.popo.scan_error_denied);
                case "protocol":
                  return t(($) => $.popo.scan_error_protocol);
                case "installation_conflict":
                  return t(($) => $.popo.scan_error_conflict);
                case "session_lost":
                  return t(($) => $.popo.scan_error_session_lost);
                case "forbidden":
                  return t(($) => $.popo.scan_error_forbidden);
                case "no_bridge":
                  return t(($) => $.popo.no_online_bridge);
                default:
                  return t(($) => $.popo.scan_error_generic);
              }
            })()}
          </p>
          {errorMessage ? (
            <p className="break-all text-micro text-muted-foreground">{errorMessage}</p>
          ) : null}
        </div>
      )}
    </div>
  );
}

function PopoAgentBotStatusRow({
  onClick,
  className,
}: {
  onClick: () => void;
  className?: string;
}) {
  const { t } = useT("settings");
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-caption text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
        className,
      )}
      data-testid="popo-agent-bot-status"
    >
      <span className="inline-block h-1.5 w-1.5 shrink-0 rounded-full bg-emerald-500" />
      <span className="truncate">{t(($) => $.popo.agent_bot_connected_label)}</span>
      <ChevronRight className="ml-auto h-3.5 w-3.5 shrink-0" />
    </button>
  );
}

function PopoAgentBotConnectedBadge({
  installation,
  className,
}: {
  installation: PopoInstallation;
  className?: string;
}) {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();

  const [confirmOpen, setConfirmOpen] = useState(false);
  const [disconnecting, setDisconnecting] = useState(false);

  async function handleDisconnect() {
    if (disconnecting) return;
    setDisconnecting(true);
    try {
      await api.deletePopoInstallation(wsId, installation.id);
      await qc.invalidateQueries({ queryKey: popoKeys.all(wsId) });
      toast.success(t(($) => $.popo.toast_disconnected));
      setConfirmOpen(false);
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t(($) => $.popo.toast_disconnect_failed),
      );
    } finally {
      setDisconnecting(false);
    }
  }

  return (
    <div
      className={cn("space-y-2", className)}
      data-testid="popo-agent-bot-connected"
    >
      <div className="flex items-center justify-between gap-3">
        <span className="inline-flex min-w-0 items-center gap-2 text-caption text-muted-foreground">
          <span className="inline-block h-1.5 w-1.5 shrink-0 rounded-full bg-emerald-500" />
          <span className="truncate">
            {t(($) => $.popo.agent_bot_connected_label)}
            {installation.robot_id ? ` · ${installation.robot_id}` : ""}
          </span>
        </span>
        <Button
          variant="destructive"
          size="sm"
          onClick={() => setConfirmOpen(true)}
          disabled={disconnecting}
          title={t(($) => $.popo.agent_bot_disconnect_tooltip)}
          aria-label={t(($) => $.popo.disconnect)}
          data-testid="popo-agent-bot-disconnect"
        >
          <Trash2 className="h-3 w-3" />
          {disconnecting
            ? t(($) => $.popo.disconnecting)
            : t(($) => $.popo.disconnect)}
        </Button>
      </div>

      <AlertDialog
        open={confirmOpen}
        onOpenChange={(v) => {
          if (!v && !disconnecting) setConfirmOpen(false);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.popo.disconnect_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.popo.disconnect_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnecting}>
              {t(($) => $.popo.disconnect_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleDisconnect} disabled={disconnecting}>
              {disconnecting
                ? t(($) => $.popo.disconnecting)
                : t(($) => $.popo.disconnect)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

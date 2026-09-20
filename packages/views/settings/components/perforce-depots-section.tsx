"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { Plus, Trash2 } from "lucide-react";
import { Input } from "@multica/ui/components/ui/input";
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
import { toast } from "sonner";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentWorkspace } from "@multica/core/paths";
import { memberListOptions, workspaceKeys } from "@multica/core/workspace/queries";
import { api } from "@multica/core/api";
import type { Workspace, WorkspaceP4Depot } from "@multica/core/types";
import { useT } from "../../i18n";
import { p4DepotIdentity, toP4DepotPayload } from "../../common/p4-depot";
import { SettingsCard, SettingsSaveState, SettingsSection } from "./settings-layout";
import { useAutoSave } from "./use-auto-save";

const EMPTY_DEPOTS: WorkspaceP4Depot[] = [];

function depotsEqual(left: WorkspaceP4Depot[], right: WorkspaceP4Depot[]) {
  if (left.length !== right.length) return false;
  return left.every((depot, index) => {
    const other = right[index];
    if (!other) return false;
    return (
      p4DepotIdentity(depot) === p4DepotIdentity(other) &&
      (depot.user ?? "") === (other.user ?? "") &&
      (depot.charset ?? "") === (other.charset ?? "") &&
      (depot.changelist ?? "") === (other.changelist ?? "") &&
      (depot.description ?? "") === (other.description ?? "")
    );
  });
}

export function PerforceDepotsSection() {
  const { t } = useT("settings");
  const user = useAuthStore((state) => state.user);
  const workspace = useCurrentWorkspace();
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const [depots, setDepots] = useState<WorkspaceP4Depot[]>(
    workspace?.p4_depots ?? EMPTY_DEPOTS,
  );
  const [pendingRemovalIndex, setPendingRemovalIndex] = useState<number | null>(
    null,
  );

  const currentMember = members.find((member) => member.user_id === user?.id) ?? null;
  const canManageWorkspace =
    currentMember?.role === "owner" || currentMember?.role === "admin";

  useEffect(() => {
    setDepots(workspace?.p4_depots ?? EMPTY_DEPOTS);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- keyed on workspace identity
  }, [workspace?.id]);

  const savedDepots = workspace?.p4_depots ?? EMPTY_DEPOTS;
  const draft = useMemo(() => depots, [depots]);
  const saveDepots = useCallback(
    async (next: WorkspaceP4Depot[]) => {
      if (!workspace) return;
      const updated = await api.updateWorkspace(workspace.id, {
        p4_depots: next.map(toP4DepotPayload),
      });
      queryClient.setQueryData(
        workspaceKeys.list(),
        (old: Workspace[] | undefined) =>
          old?.map((item) => (item.id === updated.id ? updated : item)),
      );
    },
    [queryClient, workspace],
  );
  const allRequiredFilled = depots.every(
    (depot) => depot.port.trim().length > 0 && depot.depot.trim().length > 0,
  );
  const autoSave = useAutoSave({
    value: draft,
    savedValue: savedDepots,
    onSave: saveDepots,
    onSuccess: () =>
      toast.success(t(($) => $.perforce.toast_saved), {
        id: "settings-p4-auto-save",
      }),
    onError: (error) =>
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.perforce.toast_save_failed),
      ),
    enabled: !!workspace && canManageWorkspace && allRequiredFilled,
    isEqual: depotsEqual,
  });

  const updateDepot = (
    index: number,
    field: keyof WorkspaceP4Depot,
    value: string,
  ) => {
    setDepots((current) =>
      current.map((depot, depotIndex) =>
        depotIndex === index ? { ...depot, [field]: value } : depot,
      ),
    );
  };

  const addDepot = () => {
    setDepots((current) => [...current, { port: "", depot: "" }]);
  };

  const removeDepot = (index: number) => {
    const next = depots.filter((_, depotIndex) => depotIndex !== index);
    setDepots(next);
    autoSave.saveNow(next);
  };

  if (!workspace) return null;

  return (
    <>
      <SettingsSection
        title={t(($) => $.perforce.section_title)}
        description={t(($) => $.perforce.description)}
        action={
          <SettingsSaveState
            status={autoSave.status}
            savingLabel={t(($) => $.auto_save.saving)}
            savedLabel={t(($) => $.auto_save.saved)}
            errorLabel={t(($) => $.auto_save.failed)}
          />
        }
      >
        <SettingsCard>
          {depots.length === 0 ? (
            <div className="px-4 py-8 text-center text-caption text-muted-foreground">
              {t(($) => $.perforce.empty)}
            </div>
          ) : null}

          {depots.map((depot, index) => (
            <div key={index} className="space-y-2 px-4 py-3.5">
              <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] sm:items-center">
                <Input
                  type="text"
                  name={`p4-depot-${index}-port`}
                  autoComplete="off"
                  spellCheck={false}
                  aria-label={t(($) => $.perforce.port_placeholder)}
                  value={depot.port}
                  onChange={(event) =>
                    updateDepot(index, "port", event.target.value)
                  }
                  onBlur={autoSave.flush}
                  disabled={!canManageWorkspace}
                  aria-invalid={!depot.port.trim()}
                  placeholder={t(($) => $.perforce.port_placeholder)}
                  className="font-mono text-caption"
                />
                <Input
                  type="text"
                  name={`p4-depot-${index}-depot`}
                  autoComplete="off"
                  spellCheck={false}
                  aria-label={t(($) => $.perforce.depot_placeholder)}
                  value={depot.depot}
                  onChange={(event) =>
                    updateDepot(index, "depot", event.target.value)
                  }
                  onBlur={autoSave.flush}
                  disabled={!canManageWorkspace}
                  aria-invalid={!depot.depot.trim()}
                  placeholder={t(($) => $.perforce.depot_placeholder)}
                  className="font-mono text-caption"
                />
                {canManageWorkspace ? (
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={t(($) => $.perforce.delete_aria)}
                    className="justify-self-end text-muted-foreground hover:text-destructive"
                    onClick={() => setPendingRemovalIndex(index)}
                  >
                    <Trash2 className="size-3.5" />
                  </Button>
                ) : null}
              </div>
              <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,0.8fr)]">
                <Input
                  type="text"
                  name={`p4-depot-${index}-stream`}
                  autoComplete="off"
                  spellCheck={false}
                  aria-label={t(($) => $.perforce.stream_placeholder)}
                  value={depot.stream ?? ""}
                  onChange={(event) =>
                    updateDepot(index, "stream", event.target.value)
                  }
                  onBlur={autoSave.flush}
                  disabled={!canManageWorkspace}
                  placeholder={t(($) => $.perforce.stream_placeholder)}
                  className="font-mono text-caption"
                />
                <Input
                  type="text"
                  name={`p4-depot-${index}-description`}
                  autoComplete="off"
                  aria-label={t(($) => $.perforce.description_placeholder)}
                  value={depot.description ?? ""}
                  onChange={(event) =>
                    updateDepot(index, "description", event.target.value)
                  }
                  onBlur={autoSave.flush}
                  disabled={!canManageWorkspace}
                  placeholder={t(($) => $.perforce.description_placeholder)}
                />
              </div>
            </div>
          ))}

          {canManageWorkspace ? (
            <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-3.5">
              <Button variant="outline" size="sm" onClick={addDepot}>
                <Plus className="size-3.5" />
                {t(($) => $.perforce.add)}
              </Button>
              {!allRequiredFilled ? (
                <span className="text-caption text-muted-foreground">
                  {t(($) => $.perforce.incomplete)}
                </span>
              ) : null}
            </div>
          ) : (
            <div className="px-4 py-3 text-caption text-muted-foreground">
              {t(($) => $.perforce.manage_hint)}
            </div>
          )}
        </SettingsCard>
      </SettingsSection>

      <AlertDialog
        open={pendingRemovalIndex !== null}
        onOpenChange={(open) => {
          if (!open) setPendingRemovalIndex(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.perforce.delete_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.perforce.delete_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>
              {t(($) => $.perforce.delete_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                if (pendingRemovalIndex !== null) {
                  removeDepot(pendingRemovalIndex);
                }
                setPendingRemovalIndex(null);
              }}
            >
              {t(($) => $.perforce.delete_confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

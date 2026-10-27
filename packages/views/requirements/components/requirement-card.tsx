"use client";

import { useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { createSafeId } from "@multica/core/utils";
import { memberListOptions } from "@multica/core/workspace/queries";
import {
  parseYixiezuoRequirementSnapshot,
  useUpdateRequirementParticipants,
  type RequirementDetail,
  type RequirementParticipantInput,
} from "@multica/core/requirements";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { YixiezuoSourcePreview } from "../../issues/components/yixiezuo-import";
import { useT } from "../../i18n";

const editableRoles = ["requester", "approver", "viewer"] as const;
type EditableRole = (typeof editableRoles)[number];

export function RequirementCard({ wsId, detail }: { wsId: string; detail: RequirementDetail }) {
  const { t } = useT("requirements");
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const updateParticipants = useUpdateRequirementParticipants(wsId, detail.id);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState<RequirementParticipantInput[]>([]);
  const [editRevision, setEditRevision] = useState<number>();
  const idempotencyKey = useRef<string | null>(null);
  const memberName = (userId: string) => {
    const member = members.find((item) => item.user_id === userId);
    return member?.name || member?.email || userId;
  };
  const source = parseYixiezuoRequirementSnapshot(detail.sourceSnapshot);
  const stale = editing && editRevision !== detail.revision;
  const hasApprover = draft.some((participant) => participant.roles.includes("approver"));

  const startEditing = () => {
    setDraft(detail.participants.map((participant) => ({
      userId: participant.userId,
      roles: participant.roles.filter((role): role is EditableRole =>
        editableRoles.includes(role as EditableRole),
      ),
    })));
    setEditRevision(detail.revision);
    idempotencyKey.current = createSafeId();
    updateParticipants.reset();
    setEditing(true);
  };

  const save = async () => {
    if (editRevision === undefined || stale || !hasApprover) return;
    const key = idempotencyKey.current ?? createSafeId();
    idempotencyKey.current = key;
    try {
      await updateParticipants.mutateAsync({
        expectedRevision: editRevision,
        participants: draft,
        idempotencyKey: key,
      });
      idempotencyKey.current = null;
      setEditing(false);
    } catch {
      // The mutation keeps the draft and its idempotency key for an explicit retry.
    }
  };

  return (
    <section aria-label={t(($) => $.card.title)} className="space-y-3 rounded-lg border border-border p-3">
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-body font-medium">{t(($) => $.card.title)}</h3>
        <span className="text-caption text-muted-foreground">{t(($) => $.states[detail.state])}</span>
      </div>
      {detail.run && <div className="flex items-center gap-2 text-caption">
        <span className="text-muted-foreground">{t(($) => $.card.run)}</span>
        <span>{detail.run.status}</span>
      </div>}
      {detail.run?.status === "completed" && detail.state !== "done" && (
        <p className="text-caption text-muted-foreground">{t(($) => $.card.run_completed_not_done)}</p>
      )}
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-caption">
        <dt className="text-muted-foreground">{t(($) => $.card.source)}</dt>
        <dd className="min-w-0 break-all">{detail.sourceHost}/{detail.externalId}</dd>
        {detail.sourceUrl && <dd className="col-start-2"><a className="underline" href={detail.sourceUrl} target="_blank" rel="noreferrer">{detail.sourceUrl}</a></dd>}
      </dl>
      {source && <details>
        <summary className="cursor-pointer text-caption font-medium">{t(($) => $.card.source_preview)}</summary>
        <div className="mt-2 max-h-80 overflow-y-auto rounded-md border border-border p-3">
          <YixiezuoSourcePreview snapshot={source} />
        </div>
      </details>}
      <div className="space-y-2">
        <div className="flex items-center justify-between gap-2">
          <h4 className="text-caption font-medium">{t(($) => $.card.participants)}</h4>
          {detail.permissions.canManageParticipants && !editing && (
            <Button type="button" size="sm" variant="outline" onClick={startEditing} disabled={detail.revision === undefined}>
              {t(($) => $.participants.edit)}
            </Button>
          )}
        </div>
        {!editing ? (
          <ul className="space-y-1 text-caption">
            {detail.participants.map((participant) => (
              <li key={participant.id} className="flex flex-wrap items-center gap-2">
                <span>{memberName(participant.userId)}</span>
                <span className="text-muted-foreground">{participant.roles.join(", ")}</span>
              </li>
            ))}
          </ul>
        ) : (
          <div className="space-y-3">
            {stale && <p role="alert" className="text-caption text-destructive">{t(($) => $.participants.stale)}</p>}
            {draft.map((participant, index) => {
              const requester = participant.roles.includes("requester");
              return (
                <div key={participant.userId} className="space-y-1 rounded-md border border-border p-2">
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-caption">{memberName(participant.userId)}</span>
                    {!requester && <Button type="button" size="sm" variant="ghost" onClick={() => setDraft((current) => current.filter((_, i) => i !== index))}>
                      {t(($) => $.participants.remove)}
                    </Button>}
                  </div>
                  <div className="flex flex-wrap gap-3">
                    {editableRoles.map((role) => {
                      const checked = participant.roles.includes(role);
                      return (
                        <label key={role} className="flex items-center gap-1.5 text-caption">
                          <Checkbox
                            checked={checked}
                            disabled={role === "requester"}
                            onCheckedChange={(next) => setDraft((current) => current.map((item, i) => {
                              if (i !== index || role === "requester") return item;
                              const roles = next === true
                                ? [...item.roles, role]
                                : item.roles.filter((value) => value !== role);
                              return { ...item, roles: [...new Set(roles)] };
                            }))}
                          />
                          {t(($) => $.participants.roles[role])}
                        </label>
                      );
                    })}
                  </div>
                </div>
              );
            })}
            <select
              aria-label={t(($) => $.participants.add_member)}
              className="h-9 w-full rounded-md border border-input bg-background px-3 text-body"
              value=""
              onChange={(event) => {
                const userId = event.target.value;
                if (!userId) return;
                setDraft((current) => current.some((item) => item.userId === userId)
                  ? current
                  : [...current, { userId, roles: ["viewer"] }]);
              }}
            >
              <option value="">{t(($) => $.participants.add_member)}</option>
              {members.filter((member) => !draft.some((participant) => participant.userId === member.user_id)).map((member) => (
                <option key={member.user_id} value={member.user_id}>{memberName(member.user_id)}</option>
              ))}
            </select>
            {!hasApprover && <p role="alert" className="text-caption text-destructive">{t(($) => $.participants.approver_required)}</p>}
            {updateParticipants.error && <p role="alert" className="text-caption text-destructive">{updateParticipants.error.message}</p>}
            <div className="flex gap-2">
              <Button type="button" variant="outline" disabled={updateParticipants.isPending} onClick={() => setEditing(false)}>
                {t(($) => $.participants.cancel)}
              </Button>
              <Button type="button" disabled={updateParticipants.isPending || stale || !hasApprover || editRevision === undefined} aria-busy={updateParticipants.isPending} onClick={() => void save()}>
                {t(($) => $.participants.save)}
              </Button>
            </div>
          </div>
        )}
      </div>
    </section>
  );
}

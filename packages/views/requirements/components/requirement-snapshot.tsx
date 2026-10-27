import { useQuery } from "@tanstack/react-query";
import { agentListOptions } from "@multica/core/workspace/queries";
import { projectListOptions } from "@multica/core/projects";
import {
  resolveRequirementCandidateAttachments,
  type RequirementCandidateSnapshot,
} from "@multica/core/requirements";
import type { YixiezuoSnapshot } from "@multica/core/yixiezuo";
import { useT } from "../../i18n";

function shortId(id: string): string {
  return id.length > 12 ? `${id.slice(0, 8)}...` : id;
}

export function RequirementSnapshotView({
  wsId,
  snapshot,
  sourceSnapshot,
}: {
  wsId: string;
  snapshot: RequirementCandidateSnapshot;
  sourceSnapshot?: YixiezuoSnapshot | null;
}) {
  const { t } = useT("requirements");
  const { data: projects = [] } = useQuery(projectListOptions(wsId));
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const selectedAttachments = resolveRequirementCandidateAttachments(snapshot, sourceSnapshot ?? null);
  const attachmentsUnavailable = selectedAttachments.some(({ attachment }) => !attachment);
  const project = snapshot.projectId
    ? projects.find((item) => item.id === snapshot.projectId)
    : null;
  const agent = agents.find((item) => item.id === snapshot.agentId);
  const empty = <span className="text-muted-foreground">{t(($) => $.candidate.empty)}</span>;
  const list = (items: string[]) => items.length > 0
    ? <ul className="list-disc space-y-1 pl-5">{items.map((item, index) => <li key={index} className="whitespace-pre-wrap break-words [overflow-wrap:anywhere]">{item}</li>)}</ul>
    : empty;

  return (
    <dl className="space-y-4 text-caption">
      <div className="grid gap-3 sm:grid-cols-2">
        <div>
          <dt className="font-medium text-muted-foreground">{t(($) => $.candidate.project)}</dt>
          <dd className="break-words [overflow-wrap:anywhere]">
            {snapshot.projectId === null
              ? t(($) => $.candidate.no_project)
              : project?.title ?? t(($) => $.candidate.unknown_project, { id: shortId(snapshot.projectId) })}
          </dd>
        </div>
        <div>
          <dt className="font-medium text-muted-foreground">{t(($) => $.candidate.agent)}</dt>
          <dd className="break-words [overflow-wrap:anywhere]">
            {agent?.name ?? t(($) => $.candidate.unknown_agent, { id: shortId(snapshot.agentId) })}
          </dd>
        </div>
      </div>
      <div>
        <dt className="font-medium text-muted-foreground">{t(($) => $.candidate.goal)}</dt>
        <dd className="whitespace-pre-wrap break-words [overflow-wrap:anywhere]">{snapshot.goal || empty}</dd>
      </div>
      <div>
        <dt className="font-medium text-muted-foreground">{t(($) => $.candidate.non_goals)}</dt>
        <dd>{list(snapshot.nonGoals)}</dd>
      </div>
      <div>
        <dt className="font-medium text-muted-foreground">{t(($) => $.candidate.scope)}</dt>
        <dd>{list(snapshot.scope)}</dd>
      </div>
      <div>
        <dt className="font-medium text-muted-foreground">{t(($) => $.candidate.constraints)}</dt>
        <dd>{list(snapshot.constraints)}</dd>
      </div>
      <div>
        <dt className="font-medium text-muted-foreground">{t(($) => $.candidate.acceptance_criteria)}</dt>
        <dd>{list(snapshot.acceptanceCriteria)}</dd>
      </div>
      {selectedAttachments.length > 0 && (
        <div>
          <dt className="font-medium text-muted-foreground">{t(($) => $.candidate.attachments)}</dt>
          <dd>
            <ul className="space-y-1">
              {selectedAttachments.map(({ externalId, attachment }, index) => (
                <li key={`${externalId}-${index}`} className="flex flex-wrap gap-x-2">
                  {attachment ? (
                    <>
                      <span className="break-words [overflow-wrap:anywhere]">{attachment.name}</span>
                      <span className="text-muted-foreground">{attachment.content_type || t(($) => $.candidate.file)}</span>
                    </>
                  ) : (
                    <span className="text-destructive">{t(($) => $.candidate.unknown_attachment, { id: externalId })}</span>
                  )}
                </li>
              ))}
            </ul>
            {attachmentsUnavailable && (
              <p role="alert" className="mt-2 text-destructive">{t(($) => $.candidate.attachments_unavailable)}</p>
            )}
          </dd>
        </div>
      )}
    </dl>
  );
}

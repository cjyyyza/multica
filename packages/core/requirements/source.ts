import {
  YixiezuoSnapshotSchema,
  type YixiezuoSnapshot,
} from "../yixiezuo/schemas";
import type { RequirementCandidateSnapshot, RequirementDetail } from "./schemas";

export function isNativeRequirement(detail: Pick<RequirementDetail, "sourceHost" | "sourceSnapshot">): boolean {
  return detail.sourceHost === "multica" || detail.sourceSnapshot?.kind === "squad_delegation";
}

export function parseYixiezuoRequirementSnapshot(
  value: unknown,
): YixiezuoSnapshot | null {
  const parsed = YixiezuoSnapshotSchema.safeParse(value);
  return parsed.success ? parsed.data : null;
}

export function resolveRequirementCandidateAttachments(
  candidate: RequirementCandidateSnapshot,
  source: YixiezuoSnapshot | null,
) {
  const byExternalID = new Map(
    (source?.attachments ?? []).map((attachment) => [attachment.id, attachment]),
  );
  return (candidate.attachmentIds ?? []).map((externalId) => ({
    externalId,
    attachment: byExternalID.get(externalId),
  }));
}

// @vitest-environment node
import { describe, expect, it } from "vitest";
import { YixiezuoSnapshotSchema } from "../yixiezuo/schemas";
import { resolveRequirementCandidateAttachments } from "./source";
import { RequirementCandidateSnapshotSchema } from "./schemas";

describe("resolveRequirementCandidateAttachments", () => {
  it("maps only selected external IDs and preserves their source content type", () => {
    const candidate = RequirementCandidateSnapshotSchema.parse({
      goal: "Ship attachments",
      nonGoals: [],
      scope: [],
      constraints: [],
      acceptanceCriteria: [],
      projectId: null,
      agentId: "agent-1",
      attachmentIds: ["source-image", "missing-source-id"],
    });
    const source = YixiezuoSnapshotSchema.parse({
      source: { host: "dj01.pm.netease.com", id: "100", url: "https://dj01.pm.netease.com/issues/100" },
      title: "Source requirement",
      digest: "source-digest",
      attachments: [
        { id: "source-image", name: "design.png", url: "https://media.invalid/design.png", content_type: "image/png" },
        { id: "source-other", name: "notes.txt", url: "https://media.invalid/notes.txt", content_type: "text/plain" },
      ],
    });

    const resolved = resolveRequirementCandidateAttachments(candidate, source);
    expect(resolved).toHaveLength(2);
    expect(resolved[0]).toMatchObject({ externalId: "source-image" });
    expect(resolved[0]?.attachment).toMatchObject({ name: "design.png", content_type: "image/png" });
    expect(resolved[1]).toEqual({ externalId: "missing-source-id", attachment: undefined });
    expect(resolved.some(({ attachment }) => attachment?.name === "notes.txt")).toBe(false);
  });

  it("fails closed when the source preview is unavailable", () => {
    const candidate = RequirementCandidateSnapshotSchema.parse({
      goal: "Ship attachments",
      nonGoals: [],
      scope: [],
      constraints: [],
      acceptanceCriteria: [],
      projectId: null,
      agentId: "agent-1",
      attachmentIds: ["source-image"],
    });
    expect(resolveRequirementCandidateAttachments(candidate, null)).toEqual([
      { externalId: "source-image", attachment: undefined },
    ]);
  });
});

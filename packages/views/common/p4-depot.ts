import type { PerforceDepotResourceRef, WorkspaceP4Depot } from "@multica/core/types";

/** Allowlist key: lowercase port + depot + stream. Matches server p4depot.Identity. */
export function p4DepotIdentity(
  depot: Pick<WorkspaceP4Depot, "port" | "depot" | "stream">,
): string {
  return `${depot.port.trim().toLowerCase()}\n${depot.depot.trim()}\n${(depot.stream ?? "").trim()}`;
}

export function p4DepotLabel(
  depot: Pick<WorkspaceP4Depot, "port" | "depot" | "stream">,
): string {
  const stream = depot.stream?.trim();
  return stream
    ? `${depot.port.trim()} ${depot.depot.trim()} (${stream})`
    : `${depot.port.trim()} ${depot.depot.trim()}`;
}

export function toP4DepotPayload(depot: WorkspaceP4Depot): WorkspaceP4Depot {
  const payload: WorkspaceP4Depot = {
    port: depot.port.trim(),
    depot: depot.depot.trim(),
  };
  const stream = depot.stream?.trim();
  const user = depot.user?.trim();
  const charset = depot.charset?.trim();
  const changelist = depot.changelist?.trim();
  const description = depot.description?.trim();
  const swarmURL = depot.swarm_url?.trim();
  if (stream) payload.stream = stream;
  if (user) payload.user = user;
  if (charset) payload.charset = charset;
  if (changelist) payload.changelist = changelist;
  if (description) payload.description = description;
  if (swarmURL) payload.swarm_url = swarmURL;
  return payload;
}

export function p4RefIdentity(ref: PerforceDepotResourceRef): string {
  return p4DepotIdentity(ref);
}

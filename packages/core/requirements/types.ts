import type {
  RequirementCandidateSnapshot,
  RequirementParticipantRole,
  RequirementSnapshot,
} from "./schemas";

export interface RequirementParticipantInput {
  userId: string;
  roles: Exclude<RequirementParticipantRole, "unknown">[];
}

export interface CreateRequirementRequest {
  sourceHost: string;
  externalId: string;
  sourceUrl: string;
  sourceSnapshot: RequirementSnapshot;
  sourceDigest: string;
  sourceLockVersion?: number | null;
  agentId: string;
  projectId?: string | null;
  participants?: RequirementParticipantInput[];
  intakeOperationId?: string | null;
}

export interface UpdateRequirementParticipantsRequest {
  expectedRevision: number;
  participants: RequirementParticipantInput[];
}

export interface OpenRequirementRequest {
  expectedRevision: number;
}

export interface SubmitRequirementCandidateRequest {
  expectedRevision: number;
  snapshot: RequirementCandidateSnapshot;
}

export interface ApproveRequirementRequest {
  expectedRevision: number;
  candidateId: string;
  candidateDigest: string;
}

export interface AcceptRequirementRequest {
  expectedIssueRevision: number;
  summary?: string;
  statusName?: string;
}

export interface RejectRequirementRequest {
  expectedIssueRevision: number;
  reason: string;
}

export type CancelRequirementRequest = {
  reason?: string;
} & (
  | { expectedRevision: number; expectedIssueRevision?: number }
  | { expectedRevision?: number; expectedIssueRevision: number }
);

export type ReconcileRequirementRequest =
  | { operationId: string; deliveryId?: string }
  | { operationId?: string; deliveryId: string };

export type RequirementMutationInput<Request> = Request & {
  idempotencyKey: string;
};

import { z, type ZodType } from "zod";
import {
  AgentTaskSchema,
  ChatPendingTaskSchema,
  ChatSessionSchema,
  IssueSchema,
} from "../api/schemas";

export const REQUIREMENT_STATES = [
  "intake_pending",
  "clarifying",
  "awaiting_approval",
  "executing",
  "blocked",
  "in_review",
  "done",
  "cancelled",
] as const;

export const REQUIREMENT_INTAKE_STATES = [
  "intake_pending",
  "clarifying",
  "awaiting_approval",
] as const;

export const REQUIREMENT_OPERATION_STATES = [
  "pending",
  "running",
  "succeeded",
  "failed",
  "conflict",
  "unknown",
  "needs_reconcile",
  "superseded",
] as const;

export const REQUIREMENT_DELIVERY_STATES = [
  "pending",
  "queued",
  "backlog",
  "sending",
  "accepted",
  "delivered",
  "failed",
  "unknown",
  "superseded",
] as const;

function enumWithUnknown<const Values extends readonly string[]>(values: Values) {
  return z.unknown().transform((value): Values[number] | "unknown" =>
    typeof value === "string" && values.includes(value)
      ? value as Values[number]
      : "unknown",
  );
}

function looseArray<Item>(schema: ZodType<Item>) {
  return z.unknown().transform((value): Item[] => {
    if (!Array.isArray(value)) return [];
    return value.flatMap((item) => {
      const parsed = schema.safeParse(item);
      return parsed.success ? [parsed.data] : [];
    });
  });
}

const nullableString = z.string().nullable().optional().default(null).catch(null);
const optionalString = z.string().optional().catch(undefined);
const optionalRevision = z.number().int().positive().optional();

export const RequirementSnapshotSchema = z.record(z.string(), z.unknown());
export type RequirementSnapshot = z.infer<typeof RequirementSnapshotSchema>;

export const RequirementCandidateSnapshotSchema = z.object({
  goal: z.string(),
  nonGoals: z.array(z.string()),
  scope: z.array(z.string()),
  constraints: z.array(z.string()),
  acceptanceCriteria: z.array(z.string()),
  projectId: z.string().nullable(),
  agentId: z.string(),
  attachmentIds: z.array(z.string()).optional(),
}).strict();
export type RequirementCandidateSnapshot = z.infer<typeof RequirementCandidateSnapshotSchema>;

export const RequirementStateSchema = enumWithUnknown(REQUIREMENT_STATES);
export type RequirementState = z.infer<typeof RequirementStateSchema>;

export const RequirementIntakeStateSchema = enumWithUnknown(REQUIREMENT_INTAKE_STATES);
export type RequirementIntakeState = z.infer<typeof RequirementIntakeStateSchema>;

export const RequirementParticipantRoleSchema = enumWithUnknown([
  "requester",
  "approver",
  "viewer",
] as const);
export type RequirementParticipantRole = z.infer<typeof RequirementParticipantRoleSchema>;

export const RequirementParticipantSchema = z.object({
  id: z.string().min(1),
  userId: z.string().min(1),
  roles: looseArray(RequirementParticipantRoleSchema),
}).loose();
export type RequirementParticipant = z.infer<typeof RequirementParticipantSchema>;

export const RequirementCandidateSchema = z.object({
  id: z.string().min(1),
  requirementId: z.string().min(1),
  revision: optionalRevision,
  snapshot: RequirementCandidateSnapshotSchema,
  digest: z.string().min(1),
  createdByTaskId: z.string().min(1).nullable().optional().default(null).catch(null),
  createdAt: z.string().default("").catch(""),
}).loose();
export type RequirementCandidate = z.infer<typeof RequirementCandidateSchema>;

const permission = z.boolean().optional().default(false).catch(false);

export const RequirementPermissionsSchema = z.object({
  canRead: permission,
  canOpen: permission,
  canManageParticipants: permission,
  canSubmitCandidate: permission,
  canApprove: permission,
  canAccept: permission,
  canReject: permission,
  canCancel: permission,
  canReconcile: permission,
}).loose();
export type RequirementPermissions = z.infer<typeof RequirementPermissionsSchema>;

export const DEFAULT_REQUIREMENT_PERMISSIONS: RequirementPermissions = {
  canRead: false,
  canOpen: false,
  canManageParticipants: false,
  canSubmitCandidate: false,
  canApprove: false,
  canAccept: false,
  canReject: false,
  canCancel: false,
  canReconcile: false,
};

export const RequirementOperationSchema = z.object({
  id: z.string().min(1),
  requirementId: optionalString,
  kind: enumWithUnknown([
    "intake",
    "draft",
    "writeback",
    "preview",
    "refresh",
    "publish",
    "review",
    "reconcile",
  ] as const),
  state: enumWithUnknown(REQUIREMENT_OPERATION_STATES),
  requestHash: optionalString,
  targetOperationId: optionalString,
  error: nullableString,
  createdAt: z.string().default("").catch(""),
  updatedAt: z.string().default("").catch(""),
}).loose();
export type RequirementOperation = z.infer<typeof RequirementOperationSchema>;

export const RequirementDeliverySchema = z.object({
  id: z.string().min(1),
  requirementId: optionalString,
  kind: enumWithUnknown([
    "requirement_draft",
    "requirement_status",
    "requirement_blocked",
    "requirement_review",
  ] as const),
  state: enumWithUnknown(REQUIREMENT_DELIVERY_STATES),
  sourceKey: optionalString,
  occurrenceId: optionalString,
  recipientBindingId: optionalString,
  recipientUserId: optionalString,
  transportCommandId: optionalString,
  transportAttempt: z.number().int().nonnegative().optional().default(0).catch(0),
  acceptedAt: nullableString,
  requestHash: optionalString,
  attemptCount: z.number().int().nonnegative().optional().default(0).catch(0),
  lastError: nullableString,
  deliveredAt: nullableString,
  createdAt: z.string().default("").catch(""),
  updatedAt: z.string().default("").catch(""),
}).loose();
export type RequirementDelivery = z.infer<typeof RequirementDeliverySchema>;

export const RequirementBlockedQuestionSchema = z.object({
  commentId: z.string().min(1),
  wakeupId: z.string().min(1),
  targetUserId: z.string().min(1),
  agentId: z.string().min(1),
  content: z.string(),
  canReply: permission,
}).loose();
export type RequirementBlockedQuestion = z.infer<typeof RequirementBlockedQuestionSchema>;

export const RequirementDetailSchema = z.object({
  id: z.string().min(1),
  workspaceId: z.string().min(1),
  revision: optionalRevision,
  state: RequirementStateSchema,
  intakeState: RequirementIntakeStateSchema,
  sourceHost: z.string().default("").catch(""),
  externalId: z.string().default("").catch(""),
  sourceUrl: nullableString,
  sourceSnapshot: RequirementSnapshotSchema.nullable().optional().default(null).catch(null),
  sourceDigest: nullableString,
  sourceLockVersion: z.number().int().nonnegative().nullable().optional().default(null).catch(null),
  requesterId: z.string().min(1),
  agentId: nullableString,
  squadId: nullableString,
  projectId: nullableString,
  chatSessionId: nullableString,
  issueId: nullableString,
  intakeOperationId: nullableString,
  cancelledAt: nullableString,
  cancelReason: nullableString,
  issueDeleted: z.boolean().optional().default(false).catch(false),
  lastError: nullableString,
  createdAt: z.string().default("").catch(""),
  updatedAt: z.string().default("").catch(""),
  participants: looseArray(RequirementParticipantSchema),
  latestCandidate: RequirementCandidateSchema.nullable().optional().default(null).catch(null),
  blockedQuestion: RequirementBlockedQuestionSchema.nullable().optional().default(null).catch(null),
  permissions: RequirementPermissionsSchema.optional().catch(undefined)
    .transform((value) => value ?? { ...DEFAULT_REQUIREMENT_PERMISSIONS }),
  issue: IssueSchema.nullable().optional().default(null).catch(null),
  run: AgentTaskSchema.nullable().optional().default(null).catch(null),
  operations: looseArray(RequirementOperationSchema),
  deliveries: looseArray(RequirementDeliverySchema),
}).loose();
export type RequirementDetail = z.infer<typeof RequirementDetailSchema>;

export const RequirementReplyResponseSchema = z.object({
  requirement: RequirementDetailSchema,
  reply: z.object({
    id: z.string().min(1),
    issueId: z.string().min(1),
    questionCommentId: z.string().min(1),
    content: z.string().min(1),
    authorId: z.string().min(1),
    issueRevision: z.number().int().positive(),
  }).loose(),
}).loose();
export type RequirementReplyResponse = z.infer<typeof RequirementReplyResponseSchema>;

export const RequirementSummarySchema = RequirementDetailSchema.pick({
  id: true, workspaceId: true, revision: true, state: true, intakeState: true,
  sourceHost: true, externalId: true, requesterId: true, agentId: true, squadId: true,
  projectId: true, chatSessionId: true, issueId: true, issueDeleted: true, createdAt: true, updatedAt: true,
}).extend({
  title: z.string().optional().default("").catch(""),
  sourceKind: z.string().optional().default("").catch(""),
});
export type RequirementSummary = z.infer<typeof RequirementSummarySchema>;

export const RequirementListResponseSchema = z.object({
  requirements: looseArray(RequirementSummarySchema),
  hasMore: z.boolean().optional().default(false).catch(false),
  nextOffset: z.number().int().nonnegative().optional().catch(undefined),
}).loose();
export type RequirementListResponse = z.infer<typeof RequirementListResponseSchema>;

export const RequirementOpenResponseSchema = z.object({
  requirement: RequirementDetailSchema,
  session: ChatSessionSchema.refine((session) => session.id.length > 0),
  pendingTask: ChatPendingTaskSchema.optional(),
}).loose();
export type RequirementOpenResponse = z.infer<typeof RequirementOpenResponseSchema>;

export const RequirementApproveResponseSchema = z.object({
  requirement: RequirementDetailSchema,
  issue: IssueSchema.refine((issue) => issue.id.length > 0),
}).loose();
export type RequirementApproveResponse = z.infer<typeof RequirementApproveResponseSchema>;

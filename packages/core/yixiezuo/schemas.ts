import { z } from "zod";

const link = z.string().refine((value) => /^https?:\/\//.test(value) || value.startsWith("/api/"));
export const YixiezuoSnapshotSchema = z.object({
  source: z.object({ host: z.string(), id: z.string().min(1), url: link }),
  title: z.string().min(1),
  description: z.string().default(""),
  status: z.string().default(""),
  priority: z.string().default(""),
  updated_at: z.string().default(""),
  lock_version: z.number().nullable().optional().default(null),
  digest: z.string().min(1),
  attachments: z.array(z.object({ id: z.string(), name: z.string(), url: link, local_id: z.string().optional(), local_url: link.optional() })).default([]),
  comments: z.array(z.object({ id: z.string(), author: z.string().default(""), content: z.string(), created_at: z.string().default("") })).default([]),
  statuses: z.array(z.object({ id: z.number(), name: z.string() })).default([]),
  warnings: z.array(z.string()).default([]),
  fields: z.array(z.object({ name: z.string(), value: z.string() })).default([]),
});

export const YixiezuoOperationSchema = z.object({
  id: z.string().min(1),
  kind: z.enum(["preview", "refresh", "publish", "review"]).catch("preview"),
  state: z.enum(["pending", "running", "succeeded", "failed", "conflict", "unknown"]).catch("unknown"),
  snapshot: YixiezuoSnapshotSchema.nullable().optional().default(null),
  error: z.string().default(""),
});

export const YixiezuoImportSchema = z.object({
  issue_id: z.string(),
  snapshot: YixiezuoSnapshotSchema,
  state: z.enum(["imported", "published", "needs_review", "pending", "running", "failed", "conflict", "unknown"]).catch("unknown"),
  operation: YixiezuoOperationSchema.nullable().optional().default(null),
  revision: z.number(),
  published_revision: z.number().default(0),
  published_at: z.string().nullable().optional().default(null),
});
export const YixiezuoImportEnvelopeSchema = z.object({ import: YixiezuoImportSchema.nullable().default(null) });
export type YixiezuoSnapshot = z.infer<typeof YixiezuoSnapshotSchema>;
export type YixiezuoOperation = z.infer<typeof YixiezuoOperationSchema>;
export type YixiezuoImport = z.infer<typeof YixiezuoImportSchema>;
export const YixiezuoImportStatesSchema = z.array(z.object({
  issue_id: z.string(), external_id: z.string(), source_url: link, source_status: z.string().default(""),
  state: YixiezuoImportSchema.shape.state,
}));
export type YixiezuoImportState = z.infer<typeof YixiezuoImportStatesSchema>[number];

export const YixiezuoPMResultSchema = z.object({
  external_id: z.string().default(""),
  issue_id: z.string().default(""),
  identifier: z.string().default(""),
  url: z.string().refine((value) => value === "" || /^https?:\/\//.test(value)).default(""),
  assignee: z.string().default(""),
  source_assignee: z.string().default(""),
  state: z.enum(["created", "updated", "unchanged", "skipped", "failed"]).catch("failed"),
  changes: z.array(z.object({ field: z.string(), old: z.string(), new: z.string() })).nullish().transform((value) => value ?? []),
  warnings: z.array(z.string()).nullish().transform((value) => value ?? []),
  error: z.string().default(""),
}).superRefine((value, ctx) => {
  if (["created", "updated", "unchanged"].includes(value.state)
    && (!z.string().uuid().safeParse(value.issue_id).success || !/^[1-9][0-9]*$/.test(value.external_id))) {
    ctx.addIssue({ code: "custom", message: "A successful sync requires source and Multica identities" });
  }
});

export const YixiezuoPMTriageResultSchema = z.object({
  issue_id: z.string().uuid(),
  identifier: z.string().default(""),
  comment_id: z.string().default(""),
  state: z.enum(["notified", "already_notified", "skipped", "failed"]).catch("failed"),
  reason_code: z.string().default(""),
  error: z.string().default(""),
}).superRefine((value, ctx) => {
  if (["notified", "already_notified"].includes(value.state) && !z.string().uuid().safeParse(value.comment_id).success) {
    ctx.addIssue({ code: "custom", message: "A notified result requires a comment identity" });
  }
});

export const YixiezuoPMReconcileSchema = z.object({
  sources: z.array(YixiezuoSnapshotSchema.shape.source),
  errors: z.array(YixiezuoPMResultSchema).default([]),
  has_more: z.boolean().default(false),
  next_offset: z.number().int().nonnegative().default(0),
});
export const YixiezuoPMTriageCandidatesSchema = z.object({
  issues: z.array(z.object({ id: z.string().uuid(), identifier: z.string().default("") })),
  has_more: z.boolean().default(false),
  next_offset: z.number().int().nonnegative().default(0),
});

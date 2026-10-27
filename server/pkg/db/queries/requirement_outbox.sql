-- Requirement POPO deliveries use popo_bridge_command as their single ledger.
-- One row carries business intent, deferred routing/backlog, lease/attempt
-- fencing and receipt evidence. There is no shadow outbox row mirroring it.

-- name: CreateRequirementPopoCommand :one
INSERT INTO popo_bridge_command (
    workspace_id, bridge_id, installation_id, type, delivery_id, payload, status,
    requirement_id, requirement_kind, occurrence_id, operation_id, recipient_user_id,
    recipient_binding_id, requirement_revision, issue_revision, snapshot_payload,
    request_hash, attempt, next_attempt_at, not_sent
) VALUES (
    sqlc.arg('workspace_id'), NULL, NULL, 'send', sqlc.arg('delivery_id'), '{}'::jsonb, 'pending',
    sqlc.arg('requirement_id'), sqlc.arg('requirement_kind'), sqlc.arg('occurrence_id'),
    sqlc.narg('operation_id'), sqlc.arg('recipient_user_id'),
    NULL, sqlc.arg('requirement_revision'), sqlc.narg('issue_revision'),
    sqlc.arg('snapshot_payload')::jsonb, sqlc.narg('request_hash'), 0, now(), true
)
ON CONFLICT (delivery_id) DO NOTHING
RETURNING *;

-- name: GetRequirementPopoCommandByDeliveryID :one
SELECT * FROM popo_bridge_command
WHERE workspace_id = sqlc.arg('workspace_id')
  AND delivery_id = sqlc.arg('delivery_id')
  AND requirement_id IS NOT NULL;

-- name: LockRequirementPopoCommandByDeliveryID :one
SELECT * FROM popo_bridge_command
WHERE workspace_id = sqlc.arg('workspace_id')
  AND delivery_id = sqlc.arg('delivery_id')
  AND requirement_id IS NOT NULL
FOR UPDATE;

-- name: LockRequirementPopoCommandForReceiptByDelivery :one
SELECT * FROM popo_bridge_command
WHERE bridge_id = sqlc.arg('bridge_id')
  AND delivery_id = sqlc.arg('delivery_id')
  AND requirement_id IS NOT NULL
FOR UPDATE;

-- name: ListRequirementPopoCommands :many
SELECT * FROM popo_bridge_command
WHERE workspace_id = sqlc.arg('workspace_id')
  AND requirement_id = sqlc.arg('requirement_id')
ORDER BY created_at ASC, id ASC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: SupersedePendingRequirementPopoCommands :execrows
-- A delivery that reached a POPO bridge (leased and dispatched) may still be in
-- flight, so it keeps its lease and waits for a receipt. Unknown outcomes are
-- never rewritten by business supersession.
UPDATE popo_bridge_command AS cmd
SET status = CASE WHEN cmd.status = 'unknown' THEN cmd.status ELSE 'superseded' END,
    superseded_at = CASE
        WHEN cmd.status = 'unknown' THEN cmd.superseded_at
        ELSE COALESCE(cmd.superseded_at, now())
    END,
    lease_id = NULL,
    lease_expires_at = NULL,
    updated_at = now()
WHERE cmd.workspace_id = sqlc.arg('workspace_id')
  AND cmd.requirement_id = sqlc.arg('requirement_id')
  AND cmd.status IN ('pending', 'leased', 'failed', 'conflict')
  AND (cmd.status <> 'leased' OR cmd.dispatched_at IS NULL)
  AND (sqlc.narg('kind')::text IS NULL OR cmd.requirement_kind = sqlc.narg('kind')::text)
  AND (sqlc.narg('occurrence_id')::uuid IS NULL
       OR cmd.occurrence_id = sqlc.narg('occurrence_id')::uuid);

-- name: SupersedeRemovedRequirementPopoCommandRecipients :exec
UPDATE popo_bridge_command AS cmd
SET status = CASE WHEN cmd.status = 'unknown' THEN cmd.status ELSE 'superseded' END,
    superseded_at = CASE
        WHEN cmd.status = 'unknown' THEN cmd.superseded_at
        ELSE COALESCE(cmd.superseded_at, now())
    END,
    lease_id = NULL,
    lease_expires_at = NULL,
    updated_at = now()
WHERE cmd.workspace_id = sqlc.arg('workspace_id')
  AND cmd.requirement_id = sqlc.arg('requirement_id')
  AND cmd.recipient_user_id IS NOT NULL
  AND cmd.recipient_user_id <> ALL(sqlc.arg('keep_user_ids')::uuid[])
  AND cmd.status IN ('pending', 'leased', 'failed', 'conflict')
  AND (cmd.status <> 'leased' OR cmd.dispatched_at IS NULL);

-- name: ClaimRequirementPopoCommands :many
-- Preparation claims only rows that still need routing resolved. The claim
-- lease is separate from the bridge lease: it fences concurrent preparers and
-- expires back into the same pending state.
WITH candidates AS (
    SELECT cmd.id
    FROM popo_bridge_command AS cmd
    WHERE cmd.requirement_id IS NOT NULL
      AND cmd.status = 'pending'
      AND cmd.bridge_id IS NULL
      AND cmd.next_attempt_at <= now()
      AND (cmd.lease_expires_at IS NULL OR cmd.lease_expires_at <= now())
    ORDER BY cmd.next_attempt_at, cmd.created_at, cmd.id
    LIMIT sqlc.arg('page_limit')
    FOR UPDATE SKIP LOCKED
)
UPDATE popo_bridge_command AS cmd
SET lease_id = gen_random_uuid(),
    lease_expires_at = sqlc.arg('lease_expires_at'),
    updated_at = now()
FROM candidates
WHERE cmd.id = candidates.id
RETURNING cmd.*;

-- name: LockClaimedRequirementPopoCommand :one
SELECT * FROM popo_bridge_command
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND lease_id = sqlc.arg('lease_id')
  AND requirement_id IS NOT NULL
  AND status = 'pending'
FOR UPDATE;

-- name: DeferRequirementPopoCommand :one
UPDATE popo_bridge_command
SET next_attempt_at = sqlc.arg('next_attempt_at'),
    last_error = sqlc.narg('last_error'),
    lease_id = NULL,
    lease_expires_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND lease_id = sqlc.arg('lease_id')
  AND requirement_id IS NOT NULL
  AND status = 'pending'
RETURNING *;

-- name: SupersedeClaimedRequirementPopoCommand :one
UPDATE popo_bridge_command
SET status = 'superseded',
    superseded_at = COALESCE(superseded_at, now()),
    lease_id = NULL,
    lease_expires_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND lease_id = sqlc.arg('lease_id')
  AND requirement_id IS NOT NULL
  AND status = 'pending'
RETURNING *;

-- name: MarkClaimedRequirementPopoCommandConflict :one
UPDATE popo_bridge_command
SET status = 'conflict',
    needs_reconcile = true,
    last_error = sqlc.narg('last_error'),
    lease_id = NULL,
    lease_expires_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND lease_id = sqlc.arg('lease_id')
  AND requirement_id IS NOT NULL
  AND status = 'pending'
RETURNING *;

-- name: FailClaimedRequirementPopoCommand :one
UPDATE popo_bridge_command
SET status = 'failed',
    not_sent = true,
    retryable = false,
    evidence_finalized = true,
    needs_reconcile = true,
    next_attempt_at = 'infinity',
    last_error = sqlc.narg('last_error'),
    lease_id = NULL,
    lease_expires_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND lease_id = sqlc.arg('lease_id')
  AND requirement_id IS NOT NULL
  AND status = 'pending'
RETURNING *;

-- name: PrepareRequirementPopoCommand :one
UPDATE popo_bridge_command
SET bridge_id = sqlc.arg('bridge_id'),
    installation_id = sqlc.arg('installation_id'),
    recipient_binding_id = sqlc.arg('recipient_binding_id'),
    payload = sqlc.arg('payload')::jsonb,
    payload_hash = sqlc.arg('payload_hash'),
    attempt = GREATEST(attempt, 1),
    accepted_at = COALESCE(accepted_at, now()),
    expires_at = COALESCE(expires_at, now() + interval '24 hours'),
    status = 'pending',
    dispatched_at = NULL,
    not_sent = true,
    last_error = NULL,
    lease_id = NULL,
    lease_expires_at = NULL,
    next_attempt_at = now(),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND lease_id = sqlc.arg('lease_id')
  AND requirement_id IS NOT NULL
  AND status = 'pending'
RETURNING *;

-- name: RecordRequirementPopoCommandReceipt :one
-- Receipts fence on attempt and, when supplied, the lease and payload hash.
-- A late delivered receipt may still upgrade a terminal row because delivery is
-- the final truth; every other terminal transition stays fenced.
UPDATE popo_bridge_command AS cmd
SET status = sqlc.arg('status')::text,
    remote_message_id = sqlc.narg('remote_message_id'),
    last_error = sqlc.narg('last_error'),
    not_sent = sqlc.arg('not_sent')::boolean,
    retryable = sqlc.arg('retryable')::boolean,
    partial_delivery = sqlc.arg('partial_delivery')::boolean,
    evidence_finalized = sqlc.arg('evidence_finalized')::boolean,
    needs_reconcile = sqlc.arg('needs_reconcile')::boolean,
    delivered_at = CASE
        WHEN sqlc.arg('status')::text = 'delivered' THEN COALESCE(cmd.delivered_at, now())
        ELSE cmd.delivered_at
    END,
    unknown_at = CASE
        WHEN sqlc.arg('status')::text = 'unknown' THEN COALESCE(cmd.unknown_at, now())
        ELSE cmd.unknown_at
    END,
    superseded_at = CASE
        WHEN sqlc.arg('status')::text = 'superseded' THEN COALESCE(cmd.superseded_at, now())
        ELSE cmd.superseded_at
    END,
    lease_expires_at = NULL,
    updated_at = now()
WHERE cmd.id = sqlc.arg('id')
  AND cmd.bridge_id = sqlc.arg('bridge_id')
  AND cmd.attempt = sqlc.arg('attempt')
  AND (sqlc.narg('lease_id')::uuid IS NULL OR cmd.lease_id = sqlc.narg('lease_id')::uuid)
  AND (sqlc.narg('payload_hash')::text IS NULL OR cmd.payload_hash = sqlc.narg('payload_hash')::text)
  AND (cmd.status IN ('pending', 'leased') OR sqlc.arg('status')::text = 'delivered')
RETURNING *;

-- name: RequeueConfirmedFailedRequirementPopoCommand :one
UPDATE popo_bridge_command
SET status = 'pending',
    attempt = attempt + 1,
    dispatched_at = NULL,
    lease_id = NULL,
    lease_expires_at = NULL,
    not_sent = true,
    retryable = false,
    partial_delivery = false,
    evidence_finalized = false,
    needs_reconcile = false,
    remote_message_id = NULL,
    last_error = NULL,
    next_attempt_at = now(),
    expires_at = now() + interval '24 hours',
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND status = 'failed'
  AND attempt = sqlc.arg('attempt')
  AND requirement_id IS NOT NULL
  AND not_sent
  AND retryable
  AND evidence_finalized
  AND payload_hash IS NOT NULL
  AND payload_hash = sqlc.arg('payload_hash')
RETURNING *;

-- name: FenceRequirementPopoCommand :one
UPDATE popo_bridge_command
SET status = sqlc.arg('status')::text,
    superseded_at = CASE
        WHEN sqlc.arg('status')::text = 'superseded' THEN COALESCE(superseded_at, now())
        ELSE superseded_at
    END,
    not_sent = CASE WHEN sqlc.arg('status')::text = 'failed' THEN true ELSE not_sent END,
    evidence_finalized = CASE WHEN sqlc.arg('status')::text = 'failed' THEN true ELSE evidence_finalized END,
    needs_reconcile = CASE WHEN sqlc.arg('status')::text = 'failed' THEN true ELSE needs_reconcile END,
    last_error = sqlc.narg('last_error'),
    lease_id = NULL,
    lease_expires_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND requirement_id IS NOT NULL
  AND status IN ('pending', 'leased')
RETURNING *;

-- name: ResolveRequirementRecipientBinding :one
SELECT binding.*
FROM channel_user_binding AS binding
JOIN channel_installation AS installation ON installation.id = binding.installation_id
WHERE binding.workspace_id = @workspace_id
  AND binding.multica_user_id = @recipient_user_id
  AND binding.channel_type = 'popo'
  AND installation.channel_type = 'popo'
  AND installation.agent_id = @agent_id
  AND (sqlc.narg('installation_id')::uuid IS NULL
       OR binding.installation_id = sqlc.narg('installation_id')::uuid)
ORDER BY binding.bound_at DESC, binding.id DESC
LIMIT 1;

-- name: SetRequirementPopoCommandPayloadHash :one
-- Adopts the content identity of a prepared row created before the single
-- ledger carried payload hashes.
UPDATE popo_bridge_command
SET payload_hash = sqlc.arg('payload_hash'),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND requirement_id IS NOT NULL
  AND payload_hash IS NULL
RETURNING *;

-- name: LockRequirementPopoCommandForDelivery :one
SELECT * FROM popo_bridge_command
WHERE id = sqlc.arg('id')
  AND bridge_id = sqlc.arg('bridge_id')
  AND requirement_id IS NOT NULL
FOR UPDATE;

-- name: ClearRequirementPopoCommandReconcile :one
UPDATE popo_bridge_command
SET needs_reconcile = false,
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND requirement_id IS NOT NULL
RETURNING *;

-- name: MarkRequirementPopoCommandNeedsReconcile :one
UPDATE popo_bridge_command
SET needs_reconcile = true,
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND requirement_id IS NOT NULL
RETURNING *;

-- name: ExpireRequirementPopoCommandLeases :exec
-- A lease expiring after dispatch without a receipt is an unknown outcome: POPO
-- may have delivered it, so it must never return to pending automatically.
UPDATE popo_bridge_command
SET status = 'unknown',
    unknown_at = COALESCE(unknown_at, now()),
    not_sent = false,
    needs_reconcile = true,
    lease_expires_at = NULL,
    last_error = 'POPO command lease expired before receipt',
    updated_at = now()
WHERE requirement_id IS NOT NULL
  AND status = 'leased'
  AND lease_expires_at IS NOT NULL
  AND lease_expires_at < now();

-- name: ExpireUndispatchedRequirementPopoCommands :exec
-- A prepared command whose expiry elapsed before any bridge leased it is
-- provably not sent and can be reported as a final non-retryable failure.
UPDATE popo_bridge_command
SET status = 'failed',
    not_sent = true,
    retryable = false,
    evidence_finalized = true,
    last_error = 'POPO command expired before dispatch',
    lease_expires_at = NULL,
    updated_at = now()
WHERE requirement_id IS NOT NULL
  AND status = 'pending'
  AND dispatched_at IS NULL
  AND expires_at IS NOT NULL
  AND expires_at <= now();

-- name: CountRequirementPopoCommandDeliveryHealth :one
SELECT
    count(*) FILTER (WHERE status = 'pending')::bigint AS pending,
    count(*) FILTER (WHERE status = 'leased')::bigint AS leased,
    count(*) FILTER (WHERE status = 'unknown')::bigint AS unknown,
    count(*) FILTER (WHERE status = 'failed')::bigint AS failed,
    count(*) FILTER (WHERE needs_reconcile AND status <> 'unknown')::bigint AS needs_reconcile
FROM popo_bridge_command
WHERE workspace_id = sqlc.arg('workspace_id')
  AND requirement_id IS NOT NULL;

-- name: GetRequirementReconcileMetrics :one
WITH operation_metrics AS (
    SELECT
        count(*) FILTER (WHERE state IN ('pending', 'running'))::bigint AS pending,
        count(*) FILTER (WHERE state = 'unknown')::bigint AS unknown,
        count(*) FILTER (WHERE state = 'needs_reconcile')::bigint AS needs_reconcile,
        COALESCE(EXTRACT(EPOCH FROM now() - min(created_at) FILTER (WHERE state IN ('pending', 'running'))), 0)::double precision AS oldest_pending_seconds,
        COALESCE(EXTRACT(EPOCH FROM now() - min(created_at) FILTER (WHERE state = 'unknown')), 0)::double precision AS oldest_unknown_seconds,
        COALESCE(EXTRACT(EPOCH FROM now() - min(created_at) FILTER (WHERE state = 'needs_reconcile')), 0)::double precision AS oldest_needs_reconcile_seconds
    FROM yixiezuo_operation AS operation
    WHERE operation.workspace_id = sqlc.arg('workspace_id')
      AND (
          operation.requirement_id IS NOT NULL
          OR operation.kind = 'draft'
          OR EXISTS (
              SELECT 1 FROM requirement AS requirement
              WHERE requirement.workspace_id = operation.workspace_id
                AND (requirement.intake_operation_id = operation.id OR requirement.issue_id = operation.issue_id)
          )
      )
), delivery_metrics AS (
    SELECT
        count(*) FILTER (WHERE status = 'pending')::bigint AS pending,
        count(*) FILTER (WHERE status = 'leased')::bigint AS accepted,
        count(*) FILTER (WHERE status = 'unknown')::bigint AS unknown,
        count(*) FILTER (WHERE needs_reconcile AND status <> 'unknown')::bigint AS needs_reconcile,
        count(*) FILTER (
            WHERE status = 'pending'
              AND bridge_id IS NOT NULL
              AND (
                  (expires_at IS NOT NULL AND expires_at <= now())
                  OR (lease_expires_at IS NOT NULL AND lease_expires_at <= now())
              )
        )::bigint AS stale,
        COALESCE(EXTRACT(EPOCH FROM now() - min(created_at) FILTER (WHERE status = 'pending')), 0)::double precision AS oldest_pending_seconds,
        COALESCE(EXTRACT(EPOCH FROM now() - min(created_at) FILTER (WHERE status = 'leased')), 0)::double precision AS oldest_accepted_seconds,
        COALESCE(EXTRACT(EPOCH FROM now() - min(created_at) FILTER (WHERE status = 'unknown')), 0)::double precision AS oldest_unknown_seconds,
        COALESCE(EXTRACT(EPOCH FROM now() - min(created_at) FILTER (WHERE needs_reconcile AND status <> 'unknown')), 0)::double precision AS oldest_needs_reconcile_seconds
    FROM popo_bridge_command
    WHERE workspace_id = sqlc.arg('workspace_id')
      AND requirement_id IS NOT NULL
)
SELECT operation_metrics.pending AS operation_pending,
       operation_metrics.unknown AS operation_unknown,
       operation_metrics.needs_reconcile AS operation_needs_reconcile,
       operation_metrics.oldest_pending_seconds AS operation_oldest_pending_seconds,
       operation_metrics.oldest_unknown_seconds AS operation_oldest_unknown_seconds,
       operation_metrics.oldest_needs_reconcile_seconds AS operation_oldest_needs_reconcile_seconds,
       delivery_metrics.pending AS delivery_pending,
       delivery_metrics.accepted AS delivery_accepted,
       delivery_metrics.unknown AS delivery_unknown,
       delivery_metrics.needs_reconcile AS delivery_needs_reconcile,
       delivery_metrics.stale AS delivery_stale,
       delivery_metrics.oldest_pending_seconds AS delivery_oldest_pending_seconds,
       delivery_metrics.oldest_accepted_seconds AS delivery_oldest_accepted_seconds,
       delivery_metrics.oldest_unknown_seconds AS delivery_oldest_unknown_seconds,
       delivery_metrics.oldest_needs_reconcile_seconds AS delivery_oldest_needs_reconcile_seconds
FROM operation_metrics, delivery_metrics;

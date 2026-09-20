-- name: LockYixiezuoImportSource :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg('source_key')::text, 0));

-- name: GetYixiezuoImportBySource :one
SELECT * FROM yixiezuo_import WHERE workspace_id = $1 AND source_host = $2 AND external_id = $3;

-- name: GetYixiezuoImportByIssue :one
SELECT * FROM yixiezuo_import WHERE workspace_id = $1 AND issue_id = $2;

-- name: ListYixiezuoImportStates :many
SELECT link.issue_id, link.external_id, link.source_url,
       COALESCE(link.snapshot->>'status', '')::text AS source_status,
       CASE WHEN latest.state IN ('pending', 'running', 'failed', 'conflict', 'unknown') THEN latest.state
            WHEN link.published_revision = 0 THEN 'imported'
            WHEN link.published_revision = i.revision THEN 'published'
            ELSE 'needs_review' END::text AS state
FROM yixiezuo_import link
JOIN issue i ON i.id = link.issue_id AND i.workspace_id = link.workspace_id
LEFT JOIN LATERAL (
    SELECT op.state FROM yixiezuo_operation op
    WHERE op.workspace_id = link.workspace_id AND op.issue_id = link.issue_id AND op.kind <> 'review'
    ORDER BY op.created_at DESC, op.id DESC LIMIT 1
) latest ON true
WHERE link.workspace_id = $1;

-- name: CreateYixiezuoImport :one
INSERT INTO yixiezuo_import (workspace_id, issue_id, source_host, external_id, source_url, snapshot, imported_by)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING *;

-- name: UpdateYixiezuoImportSnapshot :exec
UPDATE yixiezuo_import SET snapshot = $3 WHERE workspace_id = $1 AND issue_id = $2;

-- name: RecordYixiezuoPublication :exec
UPDATE yixiezuo_import SET snapshot = $3, published_revision = $4, published_at = now()
WHERE workspace_id = $1 AND issue_id = $2;

-- name: CreateYixiezuoOperation :one
INSERT INTO yixiezuo_operation (workspace_id, requested_by, kind, issue_id, payload, request_key)
VALUES ($1, $2, $3, sqlc.narg('issue_id'), $4, sqlc.narg('request_key'))
ON CONFLICT (workspace_id, requested_by, request_key) WHERE request_key IS NOT NULL
DO UPDATE SET request_key = EXCLUDED.request_key RETURNING *;

-- name: CreateYixiezuoReview :one
INSERT INTO yixiezuo_operation (workspace_id, requested_by, kind, issue_id, payload, result, state, request_key)
VALUES ($1, $2, 'review', $3, $4, $5, 'succeeded', $6)
ON CONFLICT (workspace_id, requested_by, request_key) WHERE request_key IS NOT NULL
DO UPDATE SET request_key = EXCLUDED.request_key RETURNING *;

-- name: GetYixiezuoOperationByRequestKey :one
SELECT * FROM yixiezuo_operation WHERE workspace_id = $1 AND requested_by = $2 AND request_key = $3;

-- name: GetYixiezuoOperation :one
SELECT * FROM yixiezuo_operation WHERE workspace_id = $1 AND requested_by = $2 AND id = $3;

-- name: LatestYixiezuoOperation :one
SELECT * FROM yixiezuo_operation WHERE workspace_id = $1 AND issue_id = $2 AND kind <> 'review'
ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: ExpireYixiezuoOperations :exec
UPDATE yixiezuo_operation SET
    state = CASE WHEN kind = 'publish' AND state = 'running' THEN 'unknown' ELSE 'failed' END,
    error = CASE WHEN state = 'running' THEN 'The local bridge stopped before reporting a result. Refresh the source before retrying.' ELSE 'The local bridge did not pick up this request. Start multica yixiezuo bridge, then retry.' END,
    completed_at = now()
WHERE workspace_id = $1 AND
    ((state = 'pending' AND created_at < now() - interval '5 minutes') OR
     (state = 'running' AND started_at < now() - interval '3 minutes'));

-- name: ClaimYixiezuoOperation :one
WITH next AS (
    SELECT candidate.id FROM yixiezuo_operation candidate
    WHERE candidate.workspace_id = $1 AND candidate.requested_by = $2 AND candidate.state = 'pending'
    ORDER BY candidate.created_at, candidate.id FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE yixiezuo_operation op SET state = 'running', lease_token = gen_random_uuid(), started_at = now()
FROM next WHERE op.id = next.id RETURNING op.*;

-- name: CompleteYixiezuoOperation :one
UPDATE yixiezuo_operation SET state = $5, result = $6, error = $7, completed_at = now()
WHERE workspace_id = $1 AND requested_by = $2 AND id = $3 AND lease_token = $4
  AND state = 'running' AND started_at >= now() - interval '3 minutes'
RETURNING *;

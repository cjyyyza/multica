-- =====================
-- 易协作 connection + card links
-- =====================

-- name: GetYixiezuoConnectionByWorkspace :one
SELECT * FROM yixiezuo_connection
WHERE workspace_id = $1;

-- name: UpsertYixiezuoConnection :one
INSERT INTO yixiezuo_connection (
    workspace_id, project_id, cli_bin, gcp_host, list_query_id,
    external_project_id, tracker_id, status_map, created_by_id
) VALUES (
    $1, sqlc.narg('project_id'), $2, $3, $4, $5, $6, $7, sqlc.narg('created_by_id')
)
ON CONFLICT (workspace_id) DO UPDATE SET
    project_id           = EXCLUDED.project_id,
    cli_bin              = EXCLUDED.cli_bin,
    gcp_host             = EXCLUDED.gcp_host,
    list_query_id        = EXCLUDED.list_query_id,
    external_project_id  = EXCLUDED.external_project_id,
    tracker_id           = EXCLUDED.tracker_id,
    status_map           = EXCLUDED.status_map,
    updated_at           = now()
RETURNING *;

-- name: DeleteYixiezuoConnection :exec
WITH target AS (
    SELECT yixiezuo_connection.id
    FROM yixiezuo_connection
    WHERE yixiezuo_connection.id = $1 AND yixiezuo_connection.workspace_id = $2
),
cleared_links AS (
    DELETE FROM yixiezuo_card_link
    WHERE connection_id IN (SELECT target.id FROM target)
)
DELETE FROM yixiezuo_connection
WHERE yixiezuo_connection.id = $1 AND yixiezuo_connection.workspace_id = $2;

-- name: TouchYixiezuoConnectionPull :one
UPDATE yixiezuo_connection
SET last_pulled_at = now(), updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: TouchYixiezuoConnectionPush :one
UPDATE yixiezuo_connection
SET last_pushed_at = now(), updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: GetYixiezuoCardLinkByExternal :one
SELECT * FROM yixiezuo_card_link
WHERE connection_id = $1 AND external_issue_id = $2;

-- name: GetYixiezuoCardLinkByIssue :one
SELECT * FROM yixiezuo_card_link
WHERE connection_id = $1 AND issue_id = $2;

-- name: ListDirtyYixiezuoCardLinks :many
SELECT * FROM yixiezuo_card_link
WHERE connection_id = $1 AND dirty
ORDER BY updated_at ASC;

-- name: ListYixiezuoCardLinksByConnection :many
SELECT * FROM yixiezuo_card_link
WHERE connection_id = $1
ORDER BY created_at ASC;

-- name: ListUnlinkedIssuesForYixiezuoExport :many
SELECT i.id, i.workspace_id, i.title, i.description, i.status, i.priority,
       i.start_date, i.due_date, i.updated_at, i.revision, i.project_id
FROM issue i
WHERE i.workspace_id = $1
  AND i.project_id = sqlc.arg('project_id')
  AND NOT EXISTS (
      SELECT 1 FROM yixiezuo_card_link l
      WHERE l.connection_id = $2 AND l.issue_id = i.id
  )
ORDER BY i.updated_at ASC;

-- name: UpsertYixiezuoCardLink :one
INSERT INTO yixiezuo_card_link (
    workspace_id, connection_id, issue_id, external_issue_id,
    external_updated_at, issue_revision, dirty, last_direction,
    last_error, last_sync_at
) VALUES (
    $1, $2, $3, $4,
    sqlc.narg('external_updated_at'), $5, $6, $7,
    $8, sqlc.narg('last_sync_at')
)
ON CONFLICT (connection_id, issue_id) DO UPDATE SET
    external_issue_id   = EXCLUDED.external_issue_id,
    external_updated_at = EXCLUDED.external_updated_at,
    issue_revision      = EXCLUDED.issue_revision,
    dirty               = EXCLUDED.dirty,
    last_direction      = EXCLUDED.last_direction,
    last_error          = EXCLUDED.last_error,
    last_sync_at        = EXCLUDED.last_sync_at,
    updated_at          = now()
RETURNING *;

-- name: MarkYixiezuoCardLinkDirtyByIssue :exec
UPDATE yixiezuo_card_link
SET dirty = TRUE, updated_at = now()
WHERE workspace_id = $1 AND issue_id = $2;

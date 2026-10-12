-- name: UpsertP4Changelist :one
-- A task reporting its changelist. Identity is (workspace_id, server_key,
-- original_number): the number a submit assigns lands in `number`, so the same
-- changelist keeps one row across a renumber.
--
-- The three COALESCEd columns are facts a later report may not carry: a report
-- made before the Swarm review exists must not unset the review a previous one
-- attached, and the same holds for the submit timestamp and the owning task.
INSERT INTO p4_changelist (
    workspace_id, daemon_id, server_key, port, charset, number, original_number,
    client, p4_user, status, description, ticket, files_count, exclusive_files,
    warnings, swarm_review_id, task_id, submitted_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
    sqlc.narg('swarm_review_id'), sqlc.narg('task_id'), sqlc.narg('submitted_at')
)
ON CONFLICT (workspace_id, server_key, original_number) DO UPDATE SET
    daemon_id = EXCLUDED.daemon_id,
    port = EXCLUDED.port,
    charset = EXCLUDED.charset,
    number = EXCLUDED.number,
    client = EXCLUDED.client,
    p4_user = EXCLUDED.p4_user,
    status = EXCLUDED.status,
    description = EXCLUDED.description,
    ticket = EXCLUDED.ticket,
    files_count = EXCLUDED.files_count,
    exclusive_files = EXCLUDED.exclusive_files,
    warnings = EXCLUDED.warnings,
    swarm_review_id = COALESCE(EXCLUDED.swarm_review_id, p4_changelist.swarm_review_id),
    task_id = COALESCE(EXCLUDED.task_id, p4_changelist.task_id),
    submitted_at = COALESCE(EXCLUDED.submitted_at, p4_changelist.submitted_at),
    updated_at = now()
RETURNING *;

-- name: InsertManualP4Changelist :one
-- A person pasting a changelist number. Everything but the number is unknown
-- until a daemon that serves this port reports on the row, so the insert stays
-- minimal and an existing row is returned untouched rather than reset to
-- pending.
INSERT INTO p4_changelist (
    workspace_id, server_key, port, number, original_number, status
) VALUES (
    $1, $2, $3, sqlc.arg('number'), sqlc.arg('number'), 'pending'
)
ON CONFLICT (workspace_id, server_key, original_number) DO UPDATE SET
    updated_at = now()
RETURNING *;

-- name: LinkIssueP4Changelist :exec
-- link_source is not overwritten: a person's manual link stays credited to the
-- person even once the task reports the same changelist.
INSERT INTO issue_p4_changelist (issue_id, changelist_id, workspace_id, link_source)
VALUES ($1, $2, $3, $4)
ON CONFLICT (issue_id, changelist_id) DO NOTHING;

-- name: UnlinkIssueP4Changelist :execrows
DELETE FROM issue_p4_changelist
WHERE issue_id = $1 AND changelist_id = $2 AND workspace_id = $3;

-- name: DeleteOrphanP4Changelist :exec
-- Removes a changelist the last unlink left with no issue. Without it an
-- unlinked row would keep showing up in the daemon's poll list forever.
DELETE FROM p4_changelist
WHERE p4_changelist.id = $1
  AND p4_changelist.workspace_id = $2
  AND NOT EXISTS (
      SELECT 1 FROM issue_p4_changelist link WHERE link.changelist_id = p4_changelist.id
  );

-- name: GetP4ChangelistInWorkspace :one
SELECT * FROM p4_changelist WHERE id = $1 AND workspace_id = $2;

-- name: ListP4ChangelistsForIssue :many
-- Newest first, the order the issue page renders. The Swarm columns come from
-- the attached review so a card can show its state without a second request.
SELECT
    cl.id,
    cl.workspace_id,
    cl.daemon_id,
    cl.server_key,
    cl.port,
    cl.charset,
    cl.number,
    cl.original_number,
    cl.client,
    cl.p4_user,
    cl.status,
    cl.description,
    cl.ticket,
    cl.files_count,
    cl.exclusive_files,
    cl.warnings,
    cl.swarm_review_id,
    cl.task_id,
    cl.submitted_at,
    cl.created_at,
    cl.updated_at,
    link.link_source,
    sr.review_number AS swarm_review_number,
    sr.html_url AS swarm_review_url,
    sr.state AS swarm_review_state
FROM p4_changelist cl
JOIN issue_p4_changelist link ON link.changelist_id = cl.id
LEFT JOIN swarm_review sr ON sr.id = cl.swarm_review_id
WHERE link.issue_id = $1 AND link.workspace_id = $2
ORDER BY cl.created_at DESC, cl.original_number DESC;

-- name: ListOpenP4ChangelistsForDaemon :many
-- The daemon's poll list. Unfinished rows this daemon reported, plus rows no
-- daemon owns yet — a manual link is one of those, and the first daemon that
-- recognizes its port claims it through UpdateP4ChangelistStatus.
SELECT
    cl.id,
    cl.port,
    cl.number,
    cl.original_number,
    cl.status,
    sr.review_number AS swarm_review_number,
    sr.swarm_url AS swarm_url
FROM p4_changelist cl
LEFT JOIN swarm_review sr ON sr.id = cl.swarm_review_id
WHERE cl.workspace_id = $1
  AND cl.status IN ('pending', 'shelved')
  AND (cl.daemon_id = sqlc.arg('daemon_id') OR cl.daemon_id = '')
ORDER BY cl.original_number;

-- name: UpdateP4ChangelistStatus :one
-- One poll result. A zero number means the poll had nothing new to say about
-- numbering, so the stored number stands. An unowned row is claimed by the
-- daemon applying the update; an owned row only accepts its own daemon.
UPDATE p4_changelist SET
    status = sqlc.arg('status'),
    number = CASE WHEN sqlc.arg('number')::int > 0 THEN sqlc.arg('number')::int ELSE number END,
    submitted_at = COALESCE(sqlc.narg('submitted_at'), submitted_at),
    daemon_id = CASE WHEN daemon_id = '' THEN sqlc.arg('daemon_id') ELSE daemon_id END,
    updated_at = now()
WHERE id = $1
  AND workspace_id = $2
  AND (daemon_id = '' OR daemon_id = sqlc.arg('daemon_id'))
RETURNING *;

-- name: UpdateP4ChangelistSwarmReviewState :exec
-- The poll also carries the Swarm review's state. Only the state moves here;
-- the rest of the review is written when the review is attached.
UPDATE swarm_review SET state = sqlc.arg('state'), updated_at = now()
WHERE swarm_review.workspace_id = $2
  AND swarm_review.id = (
      SELECT cl.swarm_review_id FROM p4_changelist cl
      WHERE cl.id = $1 AND cl.workspace_id = $2
  );

-- name: ListIssueIDsForP4Changelist :many
SELECT issue_id FROM issue_p4_changelist
WHERE changelist_id = $1 AND workspace_id = $2
ORDER BY linked_at;

-- name: ListP4ChangelistStatesForIssue :many
-- The statuses the auto-advance decision reads, ordered so its reasons read
-- stably.
SELECT cl.id, cl.original_number, cl.status
FROM p4_changelist cl
JOIN issue_p4_changelist link ON link.changelist_id = cl.id
WHERE link.issue_id = $1
ORDER BY cl.original_number;

-- name: ListIssueP4ChangelistSwarmReviewIDs :many
-- Swarm reviews already shown as a changelist card, so the pull-request list
-- can leave them out instead of showing the same review twice.
SELECT cl.swarm_review_id
FROM p4_changelist cl
JOIN issue_p4_changelist link ON link.changelist_id = cl.id
WHERE link.issue_id = $1 AND link.workspace_id = $2 AND cl.swarm_review_id IS NOT NULL;

-- name: MoveIssueFromP4Changelists :one
-- Conditional status write for the changelist auto-advance. Same shape as
-- MoveIssueFromPullRequests: it lands only if the issue is still in the status
-- the decision saw, is not already in the target, and every linked changelist
-- is still submitted when the write runs. It also refuses once any pull request
-- is linked — a reviewed PR owns the issue's completion, and the changelist
-- rule exists for the Perforce-only case.
UPDATE issue AS i SET
    status = sqlc.arg('target_status')::text,
    duplicate_of_issue_id = NULL,
    position = (
        SELECT COALESCE(MIN(target.position), 0) - 1
        FROM issue AS target
        WHERE target.workspace_id = i.workspace_id
          AND target.status = sqlc.arg('target_status')::text
    ),
    revision = i.revision + 1,
    last_activity_at = GREATEST(COALESCE(i.last_activity_at, i.updated_at), now()),
    updated_at = now()
WHERE i.id = $1
  AND i.workspace_id = $2
  AND i.status = sqlc.arg('expected_status')::text
  AND i.status <> sqlc.arg('target_status')::text
  AND EXISTS (
      SELECT 1 FROM issue_p4_changelist link WHERE link.issue_id = i.id
  )
  AND NOT EXISTS (
      SELECT 1 FROM issue_p4_changelist link
      JOIN p4_changelist cl ON cl.id = link.changelist_id
      WHERE link.issue_id = i.id AND cl.status <> 'submitted'
  )
  AND NOT EXISTS (
      SELECT 1 FROM issue_pull_request ipr WHERE ipr.issue_id = i.id
  )
  AND NOT EXISTS (
      SELECT 1 FROM issue_vcs_pull_request ipr WHERE ipr.issue_id = i.id
  )
RETURNING i.*;

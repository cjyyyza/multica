-- name: UpsertP4Changelist :one
-- A task reporting its changelist. Identity is (workspace_id, server_key,
-- original_number): the number a submit assigns lands in `number`, so the same
-- changelist keeps one row across a renumber.
--
-- The three COALESCEd columns are facts a later report may not carry: a report
-- made before the Swarm review exists must not unset the review a previous one
-- attached, and the same holds for the submit timestamp and the owning task.
-- Lease-aware daemons fence in-flight polls when they report newer task work.
-- The opt-in flag preserves generation-zero polling for installed older daemons.
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
    number = CASE WHEN p4_changelist.status = 'submitted' THEN p4_changelist.number ELSE EXCLUDED.number END,
    client = EXCLUDED.client,
    p4_user = EXCLUDED.p4_user,
    status = CASE WHEN p4_changelist.status IN ('submitted', 'deleted') THEN p4_changelist.status ELSE EXCLUDED.status END,
    description = EXCLUDED.description,
    ticket = EXCLUDED.ticket,
    files_count = EXCLUDED.files_count,
    exclusive_files = EXCLUDED.exclusive_files,
    warnings = EXCLUDED.warnings,
    swarm_review_id = COALESCE(EXCLUDED.swarm_review_id, p4_changelist.swarm_review_id),
    task_id = COALESCE(EXCLUDED.task_id, p4_changelist.task_id),
    submitted_at = COALESCE(EXCLUDED.submitted_at, p4_changelist.submitted_at),
    poll_generation = p4_changelist.poll_generation + CASE WHEN sqlc.arg('fence_polls')::boolean THEN 1 ELSE 0 END,
    poll_lease_until = CASE WHEN sqlc.arg('fence_polls')::boolean THEN NULL ELSE p4_changelist.poll_lease_until END,
    updated_at = now()
WHERE p4_changelist.poll_generation = 0 OR sqlc.arg('fence_polls')::boolean
RETURNING *;

-- name: LockP4ChangelistWorkspace :one
-- Serialize identity discovery, placeholder adoption and linking. An absent
-- row cannot be protected with a changelist row lock alone.
SELECT id FROM workspace WHERE id = $1 FOR UPDATE;

-- name: FindP4ChangelistsByPortNumber :many
SELECT * FROM p4_changelist
WHERE workspace_id = $1 AND lower(port) = lower(sqlc.arg('port'))
  AND (number = sqlc.arg('number') OR original_number = sqlc.arg('number'))
ORDER BY (server_key <> lower(port)) DESC, (task_id IS NOT NULL) DESC, created_at
FOR UPDATE;

-- name: FindP4ChangelistIdentityRows :many
SELECT * FROM p4_changelist
WHERE workspace_id = $1 AND (
    (server_key = sqlc.arg('server_key') AND original_number = sqlc.arg('original_number'))
    OR id = sqlc.narg('source_id')
    OR (lower(port) = lower(sqlc.arg('port')) AND server_key = lower(port)
        AND (original_number IN (sqlc.arg('original_number'), sqlc.arg('number'))
             OR number IN (sqlc.arg('original_number'), sqlc.arg('number'))))
)
ORDER BY (server_key = sqlc.arg('server_key') AND original_number = sqlc.arg('original_number')) DESC,
         (task_id IS NOT NULL) DESC, created_at
FOR UPDATE;

-- name: SetP4ChangelistIdentity :one
UPDATE p4_changelist SET server_key = $3, original_number = $4, updated_at = now()
WHERE id = $1 AND workspace_id = $2 RETURNING *;

-- name: MergeP4Changelist :exec
-- Keep all issue links and any metadata already delivered for a placeholder.
-- The caller holds the workspace identity lock and keeps the canonical row.
WITH metadata AS (
    UPDATE p4_changelist target SET
        swarm_review_id = COALESCE(target.swarm_review_id, source.swarm_review_id),
        daemon_id = CASE WHEN target.daemon_id = '' THEN source.daemon_id ELSE target.daemon_id END,
        client = CASE WHEN target.client = '' THEN source.client ELSE target.client END,
        p4_user = CASE WHEN target.p4_user = '' THEN source.p4_user ELSE target.p4_user END,
        charset = CASE WHEN target.charset = '' THEN source.charset ELSE target.charset END,
        description = CASE WHEN target.description = '' THEN source.description ELSE target.description END,
        ticket = CASE WHEN target.ticket = '' THEN source.ticket ELSE target.ticket END,
        files_count = CASE WHEN target.task_id IS NULL AND source.task_id IS NOT NULL THEN source.files_count ELSE target.files_count END,
        warnings = CASE WHEN target.task_id IS NULL AND source.task_id IS NOT NULL THEN source.warnings ELSE target.warnings END,
        exclusive_files = CASE WHEN target.task_id IS NULL AND source.task_id IS NOT NULL THEN source.exclusive_files ELSE target.exclusive_files END,
        task_id = COALESCE(target.task_id, source.task_id)
    FROM p4_changelist source
    WHERE target.id = sqlc.arg('target_id') AND source.id = sqlc.arg('source_id')
      AND target.workspace_id = sqlc.arg('workspace_id') AND source.workspace_id = sqlc.arg('workspace_id')
), copied AS (
    INSERT INTO issue_p4_changelist (issue_id, changelist_id, workspace_id, link_source)
    SELECT issue_id, sqlc.arg('target_id'), workspace_id, link_source
    FROM issue_p4_changelist
    WHERE changelist_id = sqlc.arg('source_id') AND workspace_id = sqlc.arg('workspace_id')
    ON CONFLICT (issue_id, changelist_id) DO UPDATE SET link_source =
        CASE WHEN issue_p4_changelist.link_source = 'manual' OR EXCLUDED.link_source = 'manual'
             THEN 'manual' ELSE 'task' END
), removed AS (
    DELETE FROM issue_p4_changelist
    WHERE changelist_id = sqlc.arg('source_id') AND workspace_id = sqlc.arg('workspace_id')
)
DELETE FROM p4_changelist WHERE p4_changelist.id = sqlc.arg('source_id') AND p4_changelist.workspace_id = sqlc.arg('workspace_id');

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
    cl.server_key,
    cl.daemon_id,
    cl.client,
    cl.poll_generation,
    cl.p4_user,
    cl.charset,
    cl.number,
    cl.original_number,
    cl.status,
    sr.review_number AS swarm_review_number,
    sr.swarm_url AS swarm_url
FROM p4_changelist cl
LEFT JOIN swarm_review sr ON sr.id = cl.swarm_review_id
WHERE cl.workspace_id = $1
  AND cl.status IN ('pending', 'shelved')
  AND (cl.daemon_id = sqlc.arg('daemon_id') OR cl.daemon_id = '' OR (
      COALESCE(cl.poll_lease_until, cl.updated_at + interval '2 minutes') <= now()
      AND NOT EXISTS (
          SELECT 1 FROM agent_runtime ar
          WHERE ar.workspace_id = cl.workspace_id AND ar.daemon_id = cl.daemon_id
            AND ar.status = 'online' AND ar.last_seen_at > now() - interval '90 seconds'
      )
  ))
ORDER BY cl.original_number;

-- name: ClaimP4ChangelistPoll :one
-- The generation fences observations made before another daemon takes over.
UPDATE p4_changelist cl SET
    daemon_id = sqlc.arg('daemon_id'),
    poll_generation = cl.poll_generation + 1,
    poll_lease_until = now() + interval '2 minutes'
WHERE cl.id = $1 AND cl.workspace_id = $2
  AND cl.status IN ('pending', 'shelved')
  AND cl.poll_generation = sqlc.arg('expected_generation')
  AND (cl.daemon_id = sqlc.arg('daemon_id') OR cl.daemon_id = '' OR (
      COALESCE(cl.poll_lease_until, cl.updated_at + interval '2 minutes') <= now()
      AND NOT EXISTS (
          SELECT 1 FROM agent_runtime ar
          WHERE ar.workspace_id = cl.workspace_id AND ar.daemon_id = cl.daemon_id
            AND ar.status = 'online' AND ar.last_seen_at > now() - interval '90 seconds'
      )
  ))
RETURNING poll_generation;

-- name: UpdateP4ChangelistStatus :one
-- One poll result. A zero number means the poll had nothing new to say about
-- numbering, so the stored number stands. An unowned row is claimed by the
-- daemon applying the update; an owned row only accepts its own daemon.
UPDATE p4_changelist SET
    status = CASE WHEN status IN ('submitted', 'deleted') THEN status ELSE sqlc.arg('status') END,
    number = CASE WHEN status = 'submitted' THEN number WHEN sqlc.arg('number')::int > 0 THEN sqlc.arg('number')::int ELSE number END,
    submitted_at = COALESCE(sqlc.narg('submitted_at'), submitted_at),
    daemon_id = CASE WHEN daemon_id = '' THEN sqlc.arg('daemon_id') ELSE daemon_id END,
    updated_at = now()
WHERE id = $1
  AND workspace_id = $2
  AND (daemon_id = '' OR daemon_id = sqlc.arg('daemon_id'))
  AND poll_generation = sqlc.arg('poll_generation')
  AND (poll_generation = 0 OR poll_lease_until > now())
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
  AND NOT EXISTS (SELECT 1 FROM requirement r WHERE r.issue_id = i.id)
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

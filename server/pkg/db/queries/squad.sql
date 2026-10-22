-- name: CreateSquad :one
INSERT INTO squad (workspace_id, name, description, leader_id, creator_id, avatar_url, max_concurrent_tasks)
VALUES ($1, $2, $3, $4, $5, $6, sqlc.narg('max_concurrent_tasks'))
RETURNING *;

-- name: GetSquad :one
SELECT * FROM squad WHERE id = $1;

-- name: GetSquadInWorkspace :one
SELECT * FROM squad WHERE id = $1 AND workspace_id = $2;

-- name: LockSquadForAutopilotAssignment :one
-- Stabilizes the squad-to-leader resolution while an active Autopilot is
-- created, retargeted, or resumed. FOR SHARE conflicts with an ordinary
-- leader_id update, so the caller subsequently locks the same leader Agent
-- whose row Runtime teardown serializes against.
SELECT * FROM squad
WHERE id = $1 AND workspace_id = $2
FOR SHARE;

-- name: LockSquadForUpdate :one
-- Squad leader changes take the exclusive side of the same lock used by
-- Autopilot assignment. The handler then locks the proposed leader Agent and
-- pauses active squad Autopilots when that Agent is unbound.
SELECT * FROM squad
WHERE id = $1 AND workspace_id = $2
FOR UPDATE;

-- name: ListSquads :many
SELECT * FROM squad WHERE workspace_id = $1 AND archived_at IS NULL ORDER BY created_at ASC;

-- name: ListSquadMemberPreviewRows :many
-- Static squad membership summary for list/hover previews. This deliberately
-- excludes derived runtime/task status; the squad detail members-status
-- endpoint owns live state.
SELECT
    sm.squad_id,
    sm.member_type,
    sm.member_id,
    sm.role
FROM squad_member sm
JOIN squad s ON s.id = sm.squad_id
WHERE s.workspace_id = $1 AND s.archived_at IS NULL
ORDER BY
    sm.squad_id ASC,
    (sm.member_type = 'agent' AND sm.member_id = s.leader_id) DESC,
    sm.created_at ASC;

-- name: ListSquadMemberPreviewRowsBySquad :many
SELECT
    sm.squad_id,
    sm.member_type,
    sm.member_id,
    sm.role
FROM squad_member sm
JOIN squad s ON s.id = sm.squad_id
WHERE sm.squad_id = $1
ORDER BY
    (sm.member_type = 'agent' AND sm.member_id = s.leader_id) DESC,
    sm.created_at ASC;

-- name: ListAllSquads :many
SELECT * FROM squad WHERE workspace_id = $1 ORDER BY created_at ASC;

-- name: UpdateSquad :one
-- max_concurrent_tasks is tri-state: clear_max_concurrent_tasks wins and resets
-- the limit to NULL (unlimited); otherwise a provided value sets it and an
-- omitted value leaves the current limit untouched.
UPDATE squad SET
    name = COALESCE(sqlc.narg('name'), name),
    description = COALESCE(sqlc.narg('description'), description),
    leader_id = COALESCE(sqlc.narg('leader_id'), leader_id),
    avatar_url = COALESCE(sqlc.narg('avatar_url'), avatar_url),
    instructions = COALESCE(sqlc.narg('instructions'), instructions),
    max_concurrent_tasks = CASE
        WHEN @clear_max_concurrent_tasks::boolean THEN NULL
        ELSE COALESCE(sqlc.narg('max_concurrent_tasks'), max_concurrent_tasks)
    END,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ArchiveSquad :one
UPDATE squad SET archived_at = now(), archived_by = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: AddSquadMember :one
INSERT INTO squad_member (squad_id, member_type, member_id, role)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: RemoveSquadMember :execrows
DELETE FROM squad_member
WHERE squad_id = $1 AND member_type = $2 AND member_id = $3;

-- name: ListSquadMembers :many
SELECT * FROM squad_member WHERE squad_id = $1 ORDER BY created_at ASC;

-- name: UpdateSquadMemberRole :one
UPDATE squad_member SET role = $4
WHERE squad_id = $1 AND member_type = $2 AND member_id = $3
RETURNING *;

-- name: IsSquadMember :one
SELECT EXISTS(
    SELECT 1 FROM squad_member
    WHERE squad_id = $1 AND member_type = $2 AND member_id = $3
) AS is_member;

-- name: CountSquadMembers :one
SELECT count(*) FROM squad_member WHERE squad_id = $1;

-- name: GetSquadByAssignee :one
-- Look up the squad when an issue is assigned to a squad.
SELECT s.* FROM squad s WHERE s.id = $1 AND s.workspace_id = $2;

-- name: ListSquadsByMember :many
-- Find all squads a given entity belongs to in a workspace.
SELECT s.* FROM squad s
JOIN squad_member sm ON sm.squad_id = s.id
WHERE s.workspace_id = $1 AND sm.member_type = $2 AND sm.member_id = $3
ORDER BY s.created_at ASC;

-- name: TransferSquadAssignees :exec
-- Transfer all issues assigned to a squad to the squad's leader agent.
UPDATE issue SET assignee_type = 'agent', assignee_id = $2, revision = revision + 1, updated_at = now()
WHERE assignee_type = 'squad' AND assignee_id = $1;

-- name: TransferSquadAutopilotsToLeader :exec
-- Mirrors TransferSquadAssignees for autopilot rows: when a squad is archived,
-- any autopilot still pointing at the squad would otherwise dangle and the
-- admission gate would skip every subsequent dispatch with "assignee squad
-- cannot be resolved". Rewrite the assignee in place to the leader agent so
-- the autopilot keeps firing under the same leader-only execution semantics
-- it had a moment before the archive (Path A from MUL-2429).
UPDATE autopilot
SET assignee_type = 'agent',
    assignee_id = $2,
    updated_at = now()
WHERE assignee_type = 'squad' AND assignee_id = $1;

-- name: ListSquadMemberStatusRows :many
-- Per-row join used to build the squad-members status view. One row per
-- (squad_member × in_flight_task); members with no in-flight task return a
-- single row with NULL task_* columns. Human members and agent members
-- with no agent row also return one row with NULL agent_/runtime_ columns.
-- waiting_local_directory stays in the row set so its issue remains visible,
-- but the handler only treats dispatched/running rows as working because the
-- squad status vocabulary has no queued bucket.
SELECT
    sm.id              AS squad_member_id,
    sm.member_type     AS member_type,
    sm.member_id       AS member_id,
    a.archived_at      AS agent_archived_at,
    ar.status          AS runtime_status,
    ar.last_seen_at    AS runtime_last_seen_at,
    atq.id             AS task_id,
    atq.status         AS task_status,
    atq.issue_id       AS task_issue_id,
    atq.dispatched_at  AS task_dispatched_at,
    i.number           AS issue_number,
    i.title            AS issue_title,
    i.status           AS issue_status
FROM squad_member sm
LEFT JOIN agent a
       ON sm.member_type = 'agent' AND a.id = sm.member_id
LEFT JOIN agent_runtime ar
       ON ar.id = a.runtime_id
LEFT JOIN agent_task_queue atq
       ON sm.member_type = 'agent'
      AND atq.agent_id = sm.member_id
      AND atq.status IN ('dispatched', 'running', 'waiting_local_directory')
LEFT JOIN issue i
       ON i.id = atq.issue_id
WHERE sm.squad_id = $1
ORDER BY sm.created_at ASC, atq.dispatched_at DESC NULLS LAST;

-- name: GetEffectiveSquadConcurrencyForTask :one
-- Resolve scheduling ownership without changing squad_id's leader-role meaning.
-- Explicit task, autopilot and quick-create ownership follows delegation before
-- falling back to issue assignment. All gates and wakeups share this resolver.
-- Unlimited squads still return an ID so claims serialize with cap changes.
SELECT
    s.id AS squad_id,
    s.max_concurrent_tasks
FROM agent_task_queue t
JOIN squad s ON s.id = task_effective_squad_id(t.id)
WHERE t.id = @task_id
  AND s.archived_at IS NULL;

-- name: LockSquadClaimAdvisory :exec
-- Serializes squad capacity checks for claims. An advisory lock is used
-- instead of locking the squad row so claimTask's agent -> squad access order
-- can never invert LockSquadForUpdate's squad -> agent order.
-- Cap writers acquire any needed agent rows before taking this lock too.
SELECT pg_advisory_xact_lock(hashtext('squad_claim:' || (@squad_id::uuid)::text));

-- name: CountActiveTasksForEffectiveSquad :one
-- Active execution capacity for one squad, using the same effective-squad
-- resolution as ClaimAgentTask. The caller holds the squad claim advisory lock
-- and compares this count with the squad's max_concurrent_tasks.
SELECT count(*)
FROM agent_task_queue t
WHERE t.status IN ('dispatched', 'running', 'waiting_local_directory')
  AND task_effective_squad_id(t.id) = @squad_id::uuid;

-- name: CountOtherInFlightIssuesForEffectiveSquad :one
-- Counts distinct other issues holding an in-flight chain slot for the squad
-- ahead of the claimed task. Evaluated under LockSquadClaimAdvisory so two
-- concurrent claims across different issues cannot both slip past Gate 2 when
-- an earlier issue is in its queued handoff gap.
SELECT count(DISTINCT in_flight.issue_id)
FROM agent_task_queue in_flight
WHERE in_flight.issue_id IS NOT NULL
  AND in_flight.issue_id <> @issue_id::uuid
  AND task_effective_squad_id(in_flight.id) = @squad_id::uuid
  AND (
    in_flight.status IN ('dispatched', 'running', 'waiting_local_directory')
    OR (
      in_flight.status = 'queued'
      AND in_flight.delegated_from_task_id IS NOT NULL
      AND (
        sqlc.narg('delegated_from_task_id')::uuid IS NULL
        OR (in_flight.created_at, in_flight.id) < (@task_created_at::timestamptz, @task_id::uuid)
      )
    )
  );

-- name: ListQueuedTaskRuntimesBySquad :many
-- Runtimes holding queued work for a specific squad. Used when a squad's
-- concurrency cap is raised or cleared so blocked runtimes wake immediately.
SELECT DISTINCT queued.runtime_id
FROM agent_task_queue queued
WHERE queued.status = 'queued'
  AND queued.runtime_id IS NOT NULL
  AND task_effective_squad_id(queued.id) = @squad_id::uuid;

-- name: ListQueuedTaskRuntimesByIssues :many
-- Runtimes holding queued work on specific issues. Used when issues move off
-- a squad so tasks that only resolved their squad via issue.assignee_id are
-- woken even after the issue row has been updated.
SELECT DISTINCT queued.runtime_id
FROM agent_task_queue queued
WHERE queued.status = 'queued'
  AND queued.runtime_id IS NOT NULL
  AND queued.issue_id = ANY(@issue_ids::uuid[]);

-- name: ListQueuedSquadTaskRuntimes :many
-- Runtimes holding queued work for the finished task's squad. A squad limit
-- couples agents across machines, so finishing one task must wake peers on
-- other runtimes instead of leaving them parked behind their empty-claim cache.
WITH finished AS (
    SELECT task_effective_squad_id(t.id) AS squad_id
    FROM agent_task_queue t
    WHERE t.id = @task_id
)
SELECT DISTINCT queued.runtime_id
FROM agent_task_queue queued
CROSS JOIN finished
WHERE finished.squad_id IS NOT NULL
  AND queued.status = 'queued'
  AND queued.runtime_id IS NOT NULL
  AND queued.runtime_id IS DISTINCT FROM @finished_runtime_id::uuid
  AND task_effective_squad_id(queued.id) = finished.squad_id;

-- name: CreateTaskToken :one
INSERT INTO task_token (token_hash, task_id, agent_id, workspace_id, user_id, expires_at, id)
VALUES ($1, $2, $3, $4, $5, $6, COALESCE(sqlc.narg('id')::uuid, gen_random_uuid()))
RETURNING *;

-- name: GetTaskTokenByHash :one
SELECT task_token.*, task.mode, task.requirement_id, task.chat_session_id, task.issue_id
FROM task_token
JOIN agent_task_queue AS task
  ON task.id = task_token.task_id
 AND task.agent_id = task_token.agent_id
WHERE token_hash = $1 AND expires_at > now();

-- name: DeleteTaskTokensByTask :exec
DELETE FROM task_token WHERE task_id = $1;

-- name: DeleteExpiredTaskTokens :exec
DELETE FROM task_token WHERE expires_at <= now();

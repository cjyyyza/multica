-- name: UpsertSwarmReview :one
INSERT INTO swarm_review (
    workspace_id, swarm_url, review_number, changelist, title, description, author, state, html_url
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
)
ON CONFLICT (workspace_id, swarm_url, review_number) DO UPDATE SET
    changelist = EXCLUDED.changelist,
    title = EXCLUDED.title,
    description = EXCLUDED.description,
    author = EXCLUDED.author,
    state = EXCLUDED.state,
    html_url = EXCLUDED.html_url,
    updated_at = now()
RETURNING *;

-- name: LinkIssueSwarmReview :exec
INSERT INTO issue_swarm_review (issue_id, swarm_review_id, workspace_id, close_intent)
VALUES ($1, $2, $3, $4)
ON CONFLICT (issue_id, swarm_review_id) DO UPDATE SET
    close_intent = issue_swarm_review.close_intent OR EXCLUDED.close_intent;

-- name: ListSwarmReviewsByIssue :many
SELECT
    sr.id,
    sr.workspace_id,
    sr.swarm_url,
    sr.review_number,
    sr.changelist,
    sr.title,
    sr.description,
    sr.author,
    sr.state,
    sr.html_url,
    sr.created_at,
    sr.updated_at,
    isr.close_intent
FROM swarm_review sr
JOIN issue_swarm_review isr ON isr.swarm_review_id = sr.id
WHERE isr.issue_id = $1
  AND isr.workspace_id = $2
ORDER BY sr.updated_at DESC;

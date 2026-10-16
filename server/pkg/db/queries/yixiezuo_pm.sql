-- name: FindYixiezuoPMMarkerIssues :many
SELECT * FROM issue
WHERE workspace_id = @workspace_id
  AND description ~ @marker_pattern::text
ORDER BY id
FOR UPDATE;

-- name: FindYixiezuoPMTitleIssues :many
SELECT * FROM issue
WHERE workspace_id = @workspace_id AND project_id = @project_id
  AND lower(btrim(regexp_replace(title, '[[:space:]]+', ' ', 'g'))) = @normalized_title::text
ORDER BY id
FOR UPDATE;

-- name: UpdateYixiezuoPMState :exec
UPDATE yixiezuo_import SET snapshot = @snapshot::jsonb, pm_state = @pm_state::jsonb
WHERE workspace_id = @workspace_id AND issue_id = @issue_id;

-- name: ListYixiezuoPMReconcileIssues :many
SELECT i.*, COALESCE(link.source_url, '')::text AS source_url
FROM issue i
LEFT JOIN yixiezuo_import link ON link.issue_id = i.id AND link.workspace_id = i.workspace_id
WHERE i.workspace_id = @workspace_id AND i.project_id = @project_id
  AND NOT (i.status = ANY(@terminal_status_keys::text[]))
  AND (link.source_host = @source_host::text
       OR (link.id IS NULL AND i.description ~ '(^|\n)[[:blank:]]*外部单号[：:][[:blank:]]*[1-9][0-9]*[[:blank:]]*(\r?\n|$)'))
ORDER BY i.id
LIMIT @page_limit OFFSET @page_offset;

-- name: ListYixiezuoPMTriageIssues :many
SELECT i.* FROM issue i
WHERE i.workspace_id = @workspace_id AND i.project_id = @project_id
  AND i.assignee_id IS NULL
  AND (i.triage_state IS NOT NULL OR i.status = ANY(@backlog_status_keys::text[]) OR i.description LIKE '%待分诊%')
  AND NOT (i.status = ANY(@terminal_status_keys::text[]))
  AND ((@category_property_id::text = '' AND NOT EXISTS (SELECT 1 FROM issue_to_label il WHERE il.issue_id = i.id))
       OR (@category_property_id::text <> '' AND (i.properties -> @category_property_id::text IS NULL
           OR i.properties -> @category_property_id::text IN ('null'::jsonb, '[]'::jsonb, '""'::jsonb))))
ORDER BY i.id
LIMIT @page_limit OFFSET @page_offset;

-- name: YixiezuoPMHumanHandled :one
SELECT EXISTS (
    SELECT 1 FROM activity_log a WHERE a.workspace_id = @workspace_id AND a.issue_id = @issue_id
      AND a.actor_type = 'member' AND a.action <> 'issue_created'
      AND COALESCE(a.details->>'source', '') <> 'yixiezuo_pm'
    UNION ALL
    SELECT 1 FROM comment c WHERE c.workspace_id = @workspace_id AND c.issue_id = @issue_id
      AND c.author_type = 'member' AND c.deleted_at IS NULL
      AND NOT EXISTS (SELECT 1 FROM yixiezuo_pm_notification n
          WHERE n.workspace_id = c.workspace_id AND n.issue_id = c.issue_id AND n.comment_id = c.id)
)::boolean AS handled;

-- name: GetYixiezuoPMNotification :one
SELECT * FROM yixiezuo_pm_notification
WHERE workspace_id = @workspace_id AND issue_id = @issue_id AND agent_id = @agent_id;

-- name: CreateYixiezuoPMNotification :exec
INSERT INTO yixiezuo_pm_notification (workspace_id, issue_id, agent_id, comment_id, originator_user_id)
VALUES (@workspace_id, @issue_id, @agent_id, @comment_id, sqlc.narg('originator_user_id'));

-- name: CompleteYixiezuoPMNotification :one
UPDATE yixiezuo_pm_notification SET state = @state, receipt = @receipt::jsonb, error = @error, updated_at = now()
WHERE workspace_id = @workspace_id AND issue_id = @issue_id AND agent_id = @agent_id
RETURNING *;

-- name: YixiezuoPMCommentHasTask :one
SELECT EXISTS (SELECT 1 FROM agent_task_queue t
    JOIN agent a ON a.id = t.agent_id AND a.workspace_id = @workspace_id
    WHERE t.issue_id = @issue_id AND t.agent_id = @agent_id
      AND (t.trigger_comment_id = @comment_id OR @comment_id::uuid = ANY(t.coalesced_comment_ids)))::boolean AS covered;

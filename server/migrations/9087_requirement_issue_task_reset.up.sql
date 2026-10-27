-- Add an explicit, sweeper-only task_reset action to the Requirement issue
-- status policy. Generic edits and ordinary task writes remain fail-closed.
CREATE OR REPLACE FUNCTION requirement_issue_status_allowed(
    p_workspace_id UUID,
    p_issue_id UUID,
    p_current_status TEXT,
    p_next_status TEXT,
    p_action TEXT
) RETURNS BOOLEAN
LANGUAGE sql
STABLE
PARALLEL SAFE
AS $function$
    SELECT CASE
        WHEN NOT EXISTS (
            SELECT 1 FROM requirement r WHERE r.issue_id = p_issue_id
        ) THEN TRUE
        WHEN p_next_status IS NOT DISTINCT FROM p_current_status THEN TRUE
        WHEN p_action IN ('task_start', 'start') THEN
            issue_effective_status(p_workspace_id, p_current_status)
                NOT IN ('done', 'cancelled', 'in_review', 'blocked')
            AND issue_status_category(COALESCE((
                SELECT s.category FROM issue_status s
                WHERE s.workspace_id = p_workspace_id AND s.key = p_current_status
            ), p_current_status)) IN ('unstarted', 'started')
            AND p_next_status = 'in_progress'
        WHEN p_action IN ('task_complete', 'complete_task') THEN
            issue_effective_status(p_workspace_id, p_current_status)
                NOT IN ('done', 'cancelled', 'in_review', 'blocked')
            AND issue_status_category(COALESCE((
                SELECT s.category FROM issue_status s
                WHERE s.workspace_id = p_workspace_id AND s.key = p_current_status
            ), p_current_status)) IN ('unstarted', 'started')
            AND p_next_status = 'in_review'
        WHEN p_action = 'task_reset' THEN
            issue_effective_status(p_workspace_id, p_current_status)
                NOT IN ('done', 'cancelled', 'in_review', 'blocked')
            AND issue_status_category(COALESCE((
                SELECT s.category FROM issue_status s
                WHERE s.workspace_id = p_workspace_id AND s.key = p_current_status
            ), p_current_status)) IN ('unstarted', 'started')
            AND p_next_status = 'todo'
        WHEN p_action = 'accept' THEN
            issue_effective_status(p_workspace_id, p_current_status) = 'in_review'
            AND p_next_status = 'done'
        WHEN p_action = 'reject' THEN
            issue_effective_status(p_workspace_id, p_current_status) = 'in_review'
            AND p_next_status = 'in_progress'
        WHEN p_action = 'block' THEN
            issue_effective_status(p_workspace_id, p_current_status)
                NOT IN ('done', 'cancelled', 'in_review')
            AND (
                p_current_status = 'blocked'
                OR issue_status_category(COALESCE((
                    SELECT s.category FROM issue_status s
                    WHERE s.workspace_id = p_workspace_id AND s.key = p_current_status
                ), p_current_status)) IN ('unstarted', 'started')
            )
            AND p_next_status = 'blocked'
        WHEN p_action IN ('wakeup_resume', 'resume') THEN
            p_current_status = 'blocked'
            AND p_next_status = 'in_progress'
        WHEN p_action = 'cancel' THEN
            issue_effective_status(p_workspace_id, p_current_status)
                NOT IN ('done', 'cancelled')
            AND p_next_status = 'cancelled'
        ELSE FALSE
    END
$function$;

CREATE OR REPLACE FUNCTION requirement_issue_status_allowed(
    p_workspace_id UUID,
    p_issue_id UUID,
    p_current_status TEXT,
    p_next_status TEXT
) RETURNS BOOLEAN
LANGUAGE sql
STABLE
PARALLEL SAFE
AS $function$
    SELECT requirement_issue_status_allowed(
        p_workspace_id, p_issue_id, p_current_status, p_next_status, ''
    )
$function$;

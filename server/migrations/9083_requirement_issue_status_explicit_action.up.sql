-- Requirement issue status policy: explicit action instead of a transaction GUC.
--
-- 9081 authorized status transitions for a requirement-bound issue with the
-- transaction-local setting multica.requirement_status_action. Any write path
-- could therefore change the status as long as it ran inside a transaction
-- that happened to set the GUC, and the policy only checked the action against
-- the target status per rule, leaving every unlisted combination allowed
-- (ELSE TRUE).
--
-- This migration replaces the authorization with an explicit p_action
-- argument and validates the whole (action, current, next) tuple:
--
--   ''                               generic edit: no status change
--   'task_start' / 'start'            executing -> in_progress (a task beginning
--                                     work inside the requirement's executing
--                                     state; the runtime brief asks every run
--                                     to mark in_progress first)
--   'task_complete' / 'complete_task' executing -> in_review
--   'accept'                          in_review -> done
--   'reject'                          in_review -> in_progress
--   'block'                           executing/blocked -> blocked
--   'wakeup_resume' / 'resume'        blocked -> in_progress
--   'cancel'                          non-terminal -> cancelled
--
-- Anything else returns FALSE. Issues without a requirement binding keep the
-- unrestricted behaviour, and a write that does not change the status stays
-- allowed so title, description, assignee, project and other field edits keep
-- working on a requirement issue.
--
-- UpdateIssue / UpdateIssueStatus pass the empty action for generic edits and
-- their dedicated callers (requirement actions, task completion) pass the
-- explicit action. The previous 4-argument signature remains as a wrapper
-- around the empty-action policy.
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
        -- Only issues bound to a requirement are governed by this policy.
        WHEN NOT EXISTS (
            SELECT 1 FROM requirement r WHERE r.issue_id = p_issue_id
        ) THEN TRUE
        -- A same-status write is not a transition: field edits on a
        -- requirement issue must keep working.
        WHEN p_next_status IS NOT DISTINCT FROM p_current_status THEN TRUE
        -- A task starting work moves an executing issue to in_progress. This is
        -- still inside the requirement's executing state; blocked resumes
        -- through wakeup_resume, not here.
        WHEN p_action IN ('task_start', 'start') THEN
            issue_effective_status(p_workspace_id, p_current_status)
                NOT IN ('done', 'cancelled', 'in_review', 'blocked')
            AND issue_status_category(COALESCE((
                SELECT s.category FROM issue_status s
                WHERE s.workspace_id = p_workspace_id AND s.key = p_current_status
            ), p_current_status)) IN ('unstarted', 'started')
            AND p_next_status = 'in_progress'
        -- Task completion: an executing requirement issue enters review.
        WHEN p_action IN ('task_complete', 'complete_task') THEN
            issue_effective_status(p_workspace_id, p_current_status)
                NOT IN ('done', 'cancelled', 'in_review', 'blocked')
            AND issue_status_category(COALESCE((
                SELECT s.category FROM issue_status s
                WHERE s.workspace_id = p_workspace_id AND s.key = p_current_status
            ), p_current_status)) IN ('unstarted', 'started')
            AND p_next_status = 'in_review'
        -- Acceptance: review is the only state a human may accept.
        WHEN p_action = 'accept' THEN
            issue_effective_status(p_workspace_id, p_current_status) = 'in_review'
            AND p_next_status = 'done'
        -- Rejection sends the issue back to execution.
        WHEN p_action = 'reject' THEN
            issue_effective_status(p_workspace_id, p_current_status) = 'in_review'
            AND p_next_status = 'in_progress'
        -- Blocking: an executing (or already blocked) issue asks a question.
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
        -- Answering a blocking question resumes execution.
        WHEN p_action IN ('wakeup_resume', 'resume') THEN
            p_current_status = 'blocked'
            AND p_next_status = 'in_progress'
        -- Cancellation is allowed up to (not including) a terminal issue.
        WHEN p_action = 'cancel' THEN
            issue_effective_status(p_workspace_id, p_current_status)
                NOT IN ('done', 'cancelled')
            AND p_next_status = 'cancelled'
        ELSE FALSE
    END
$function$;

-- The 4-argument form is the generic-edit policy. Keeping it lets any caller
-- that never had an action argument keep the fail-closed behaviour.
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

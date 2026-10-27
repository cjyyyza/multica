-- 9083 rollback: restore the 4-argument GUC-authorized predicate from 9081 and
-- drop the explicit-action overload. Application code must be rolled back in
-- the same release because it no longer sets
-- multica.requirement_status_action.
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
    SELECT CASE
        WHEN NOT EXISTS (
            SELECT 1 FROM requirement r WHERE r.issue_id = p_issue_id
        ) THEN TRUE
        ELSE CASE
            -- R1: no transition.
            WHEN p_next_status IS NOT DISTINCT FROM p_current_status THEN TRUE
            -- R2: terminal issues stay terminal unless a requirement action
            -- explicitly authorizes accept/cancel.
            WHEN issue_effective_status(p_workspace_id, p_current_status) IN ('done', 'cancelled')
                AND COALESCE(current_setting('multica.requirement_status_action', true), '') NOT IN ('accept', 'cancel') THEN FALSE
            -- R3: in_review may only move back to executing through reject.
            WHEN issue_effective_status(p_workspace_id, p_current_status) = 'in_review'
                AND COALESCE(current_setting('multica.requirement_status_action', true), '') <> 'reject'
                AND p_next_status NOT IN ('in_review', 'blocked')
                AND COALESCE((
                    SELECT s.category FROM issue_status s
                    WHERE s.workspace_id = p_workspace_id AND s.key = p_next_status
                ), p_next_status) NOT IN ('in_review', 'blocked')
                AND issue_status_category(COALESCE((
                    SELECT s.category FROM issue_status s
                    WHERE s.workspace_id = p_workspace_id AND s.key = p_next_status
                ), p_next_status)) IN ('unstarted', 'started') THEN FALSE
            -- R4: entering blocked requires the block action.
            WHEN p_next_status = 'blocked'
                AND COALESCE(current_setting('multica.requirement_status_action', true), '') <> 'block' THEN FALSE
            -- R5: legacy category form of the blocked rule.
            WHEN COALESCE((
                SELECT s.category FROM issue_status s
                WHERE s.workspace_id = p_workspace_id AND s.key = p_next_status
            ), p_next_status) = 'blocked'
                AND COALESCE(current_setting('multica.requirement_status_action', true), '') <> 'block' THEN FALSE
            -- R6: entering done requires accept.
            WHEN issue_effective_status(p_workspace_id, p_next_status) = 'done'
                AND COALESCE(current_setting('multica.requirement_status_action', true), '') <> 'accept' THEN FALSE
            -- R7: entering cancelled requires cancel.
            WHEN issue_effective_status(p_workspace_id, p_next_status) = 'cancelled'
                AND COALESCE(current_setting('multica.requirement_status_action', true), '') <> 'cancel' THEN FALSE
            ELSE TRUE
        END
    END
$function$;

DROP FUNCTION IF EXISTS requirement_issue_status_allowed(UUID, UUID, TEXT, TEXT, TEXT);

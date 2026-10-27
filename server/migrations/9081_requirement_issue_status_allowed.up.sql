-- P3-B batch 1: single SQL policy for requirement-bound issue status writes.
--
-- UpdateIssue and UpdateIssueStatus used to carry the same inline CASE policy
-- twice. The policy is now defined once here so both main write paths, and any
-- future caller of the shared predicate, cannot drift apart.
--
-- Behaviour is intentionally identical to the removed inline CASE:
--   * an issue without a requirement binding is always allowed (ELSE TRUE);
--   * otherwise the ordered rules R1-R7 apply, including the transaction-local
--     GUC multica.requirement_status_action set by requirement actions;
--   * the requirement lookup deliberately stays unqualified by workspace to
--     match the previous "EXISTS (SELECT 1 FROM requirement r WHERE
--     r.issue_id = ...)" predicate exactly.
--
-- The function only reads requirement / issue_status and helper functions
-- issue_effective_status / issue_status_category, so STABLE PARALLEL SAFE is
-- correct and keeps the existing plan shape. The transaction-scoped GUC keeps
-- the authorization semantics: only requirement actions set it, and it resets
-- with the transaction.
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
            -- R5: legacy category form of the blocked rule. Kept verbatim for
            -- equivalence even though migrations 469/478 no longer store a
            -- 'blocked' category.
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

-- Fold Requirement wakeups into issue_wakeup. One Requirement question keeps one
-- stable issue_wakeup row for its whole life: requirement_id marks the owner,
-- parent_comment_id marks the question, and revision is the CAS generation that
-- rearm advances in place.
ALTER TABLE issue_wakeup
    ADD COLUMN last_triggered_comment_id UUID;

-- Copy the logical ledger's evidence onto the wakeup rows that represent the
-- same question, so no generation is lost when the application stops reading
-- requirement_wakeup.
UPDATE issue_wakeup AS wakeup
SET requirement_id = logical.requirement_id,
    last_triggered_comment_id = logical.last_triggered_comment_id,
    revision = GREATEST(wakeup.revision, logical.generation),
    enabled = CASE WHEN logical.state = 'disabled' THEN false ELSE wakeup.enabled END,
    disabled_at = CASE WHEN logical.state = 'disabled' THEN COALESCE(wakeup.disabled_at, logical.updated_at) ELSE wakeup.disabled_at END,
    updated_at = clock_timestamp()
FROM requirement_wakeup AS logical
WHERE wakeup.workspace_id = logical.workspace_id
  AND (
      wakeup.id = logical.current_wakeup_id
      OR (
          wakeup.requirement_id = logical.requirement_id
          AND wakeup.parent_comment_id = logical.question_comment_id
      )
  );

-- Only the current generation may still dispatch. Earlier physical rows for the
-- same question remain as history but must never be claimable again, so fence
-- them with disabled_at instead of deleting the audit trail.
UPDATE issue_wakeup AS wakeup
SET enabled = false,
    disabled_at = COALESCE(wakeup.disabled_at, clock_timestamp()),
    updated_at = clock_timestamp()
FROM requirement_wakeup AS logical
WHERE wakeup.workspace_id = logical.workspace_id
  AND wakeup.requirement_id = logical.requirement_id
  AND wakeup.parent_comment_id = logical.question_comment_id
  AND wakeup.id <> logical.current_wakeup_id
  AND wakeup.disabled_at IS NULL;

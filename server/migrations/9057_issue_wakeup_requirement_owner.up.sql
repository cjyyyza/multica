ALTER TABLE issue_wakeup
    ADD COLUMN requirement_id UUID;

UPDATE issue_wakeup AS wakeup
SET requirement_id = logical.requirement_id
FROM requirement_wakeup AS logical
JOIN requirement ON requirement.id = logical.requirement_id
  AND requirement.workspace_id = logical.workspace_id
WHERE wakeup.workspace_id = logical.workspace_id
  AND wakeup.issue_id = requirement.issue_id
  AND wakeup.parent_comment_id = logical.question_comment_id
  AND wakeup.kind = 'event'
  AND wakeup.mode = 'once'
  AND wakeup.filter_actor_type = 'member'
  AND wakeup.requirement_id IS NULL;

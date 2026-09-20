-- Nullable issue/comment pointers so a quoted channel message can be resolved
-- to the issue or comment thread it belongs to. No foreign keys (repo rule).
ALTER TABLE channel_outbound_message
    ADD COLUMN IF NOT EXISTS issue_id UUID,
    ADD COLUMN IF NOT EXISTS comment_id UUID;

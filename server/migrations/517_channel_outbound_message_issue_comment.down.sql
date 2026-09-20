ALTER TABLE channel_outbound_message
    DROP COLUMN IF EXISTS comment_id,
    DROP COLUMN IF EXISTS issue_id;

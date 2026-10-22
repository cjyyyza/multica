-- Set only by a manual rename. Automatic titling compare-and-swaps on this
-- column so a rename that keeps the same text still wins. Archive and
-- resume-pointer writes bump updated_at and must not count as a rename.
ALTER TABLE chat_session ADD COLUMN IF NOT EXISTS title_edited_at TIMESTAMPTZ;

-- Legacy rows have no marker. A first-party empty chat renamed before its
-- first user message has a title and a post-insert updated_at; mark it so the
-- first message after the upgrade does not auto-title over the manual name.
-- Insert-time seeds keep created_at = updated_at and stay eligible.
UPDATE chat_session AS cs
SET title_edited_at = cs.updated_at
WHERE cs.title_edited_at IS NULL
  AND cs.title <> ''
  AND cs.updated_at > cs.created_at
  AND NOT EXISTS (
    SELECT 1
    FROM chat_message AS m
    WHERE m.chat_session_id = cs.id
      AND m.role = 'user'
      AND m.message_kind != 'channel_command'
  )
  AND NOT EXISTS (
    SELECT 1
    FROM channel_chat_session_binding AS b
    WHERE b.chat_session_id = cs.id
  );

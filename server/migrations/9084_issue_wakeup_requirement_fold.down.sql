-- This down is safe only before the folded rows receive new writes. The old
-- requirement_wakeup ledger is not updated by the folded runtime.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM issue_wakeup AS wakeup
        JOIN requirement_wakeup AS logical ON logical.current_wakeup_id = wakeup.id
        WHERE wakeup.updated_at > COALESCE((
            SELECT applied_at FROM schema_migrations
            WHERE version = '9084_issue_wakeup_requirement_fold'
        ), 'infinity'::timestamptz)
    ) THEN
        RAISE EXCEPTION '9084 rollback is unavailable after folded wakeup writes; use a forward migration';
    END IF;
END
$$;

ALTER TABLE issue_wakeup
    DROP COLUMN IF EXISTS last_triggered_comment_id;

ALTER TABLE IF EXISTS yixiezuo_import ADD COLUMN IF NOT EXISTS pm_state JSONB NOT NULL DEFAULT '{}'::jsonb;

CREATE TABLE IF NOT EXISTS yixiezuo_pm_notification (
    workspace_id UUID NOT NULL,
    issue_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    comment_id UUID NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'notified', 'failed')),
    receipt JSONB NOT NULL DEFAULT '{}'::jsonb,
    error TEXT NOT NULL DEFAULT '',
    originator_user_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

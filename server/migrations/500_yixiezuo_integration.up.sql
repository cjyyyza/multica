-- 易协作 board sync: workspace connection + issue↔card links.
-- No foreign keys or cascades (project rule). Cleanup is application-owned:
-- DeleteYixiezuoConnection, DeleteIssue, and DeleteWorkspace.
-- Secondary indexes live in follow-up CONCURRENTLY migrations.

CREATE TABLE IF NOT EXISTS yixiezuo_connection (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL,
    project_id      UUID,
    cli_bin         TEXT NOT NULL DEFAULT 'pm-cli',
    list_query_id   TEXT NOT NULL DEFAULT '',
    status_map      JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_pulled_at  TIMESTAMPTZ,
    last_pushed_at  TIMESTAMPTZ,
    created_by_id   UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id)
);

CREATE TABLE IF NOT EXISTS yixiezuo_card_link (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id        UUID NOT NULL,
    connection_id       UUID NOT NULL,
    issue_id            UUID NOT NULL,
    external_issue_id   TEXT NOT NULL,
    external_updated_at TIMESTAMPTZ,
    issue_revision      BIGINT NOT NULL DEFAULT 0,
    dirty               BOOLEAN NOT NULL DEFAULT FALSE,
    last_direction      TEXT NOT NULL DEFAULT 'pull'
        CHECK (last_direction IN ('pull', 'push')),
    last_error          TEXT NOT NULL DEFAULT '',
    last_sync_at        TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (connection_id, external_issue_id),
    UNIQUE (connection_id, issue_id)
);

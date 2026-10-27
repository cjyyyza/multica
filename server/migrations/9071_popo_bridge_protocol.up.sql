ALTER TABLE popo_bridge
    ADD COLUMN supported_protocol_versions JSONB NOT NULL DEFAULT '[1]'::jsonb,
    ADD COLUMN selected_protocol_version INTEGER NOT NULL DEFAULT 1 CHECK (selected_protocol_version IN (1, 2)),
    ADD COLUMN protocol_health_json JSONB NOT NULL DEFAULT '{}'::jsonb;

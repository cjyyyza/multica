ALTER TABLE popo_bridge
    ADD COLUMN capabilities_json JSONB NOT NULL DEFAULT '["inbound", "send"]'::jsonb;

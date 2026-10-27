ALTER TABLE popo_bridge
    DROP COLUMN IF EXISTS protocol_health_json,
    DROP COLUMN IF EXISTS selected_protocol_version,
    DROP COLUMN IF EXISTS supported_protocol_versions;

-- name: UpdatePopoMemberConversation :one
-- Preferences and selected resource pointers belong to the authenticated member
-- binding. Updating one chat must preserve other conversations on that binding.
UPDATE channel_user_binding AS binding
SET config = jsonb_set(
    binding.config, '{popo_conversations}',
    COALESCE(binding.config->'popo_conversations', '{}'::jsonb)
        || jsonb_build_object(@scope_key::text, @conversation::jsonb), true
)
WHERE binding.workspace_id = @workspace_id
  AND binding.installation_id = @installation_id
  AND binding.multica_user_id = @user_id
  AND binding.channel_user_id = @channel_user_id
  AND binding.channel_type = 'popo'
  AND EXISTS (SELECT 1 FROM member m WHERE m.workspace_id = @workspace_id AND m.user_id = @user_id)
RETURNING binding.*;

-- name: ClaimPopoConversationCommand :one
-- Commit the replay fence in the same transaction as the preference change.
INSERT INTO channel_inbound_write (workspace_id, installation_id, channel_type, message_id, kind)
VALUES (@workspace_id, @installation_id, 'popo', @message_id, 'popo_conversation')
ON CONFLICT (installation_id, message_id, kind) DO NOTHING
RETURNING *;

-- name: ListRequirementsForPopoMember :many
SELECT r.* FROM requirement r
WHERE r.workspace_id = @workspace_id AND r.agent_id = @agent_id
  AND EXISTS (
      SELECT 1 FROM requirement_participant p
      WHERE p.workspace_id = r.workspace_id AND p.requirement_id = r.id AND p.user_id = @user_id
  )
ORDER BY r.updated_at DESC, r.id DESC
LIMIT 10;

-- name: ListPopoBindingsForMember :many
SELECT binding.* FROM channel_user_binding AS binding
JOIN channel_installation AS installation ON installation.id = binding.installation_id
WHERE binding.workspace_id = @workspace_id
  AND binding.multica_user_id = @user_id
  AND binding.channel_type = 'popo'
  AND installation.channel_type = 'popo'
ORDER BY binding.installation_id, binding.channel_user_id;

-- name: DeletePopoBindingForMember :one
DELETE FROM channel_user_binding AS binding
WHERE binding.workspace_id = @workspace_id
  AND binding.installation_id = @installation_id
  AND binding.multica_user_id = @user_id
  AND binding.channel_type = 'popo'
RETURNING binding.*;

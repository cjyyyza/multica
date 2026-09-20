# P3 QR registration implementation summary

Agent Integrations can start a Windows QR scan to create a POPO robot, or bind an existing idle one. Extract lives on Windows; Multica stores only public robot identity and the agent binding. Never `appSecret` / `aesKey`.

P4 autostart and DJ01Bot trees were not modified.

## Protocol

`POST /api/workspaces/{id}/popo/registrations?agent_id=`
Auth: `canManageAgent`. Creates `pending`, TTL 10 minutes, enqueues:

```json
{"type":"register_qr","payload":{"registration_id":"<uuid>","agent_id":"<uuid>","env":"production"}}
```

No online bridge → 409 `pair a Windows host first`. Several online bridges → newest heartbeat. Optional body `bridge_id`.

`GET /api/workspaces/{id}/popo/registrations/{registrationId}`
Initiator or workspace owner/admin. JSON:

```json
{
  "id": "...",
  "status": "pending|awaiting_scan|success|error|expired",
  "qr_url": "",
  "robot_id": "",
  "installation_id": "",
  "error_reason": "",
  "poll_interval_seconds": 2
}
```

zod + `parseWithFallback` with defaults.

Windows (bridge token): `POST /api/popo/bridge/registrations/{id}/progress`

- `{"qr_url":"https://..."}` → `awaiting_scan`
- `{"robot_id":"...","robot_name":"optional"}` → installation `{app_id, robot_name, bridge_id}`, status `success`
- `{"error_reason":"denied|expired|protocol"}` → `error`

Secret fields in the body are ignored. One-bot-one-agent uniqueness is unchanged. Occupancy lock stays on Windows.

`DELETE` expires the session and enqueues `cancel_registration`. Later progress is ignored.

## UI

Connect POPO dialog keeps idle-robot bind and adds **Scan to create**. It polls status and renders `qr_url` with `react-qr-code`. Settings POPO tab has no second scan flow. Locales: en, zh-Hans, ja, ko, fr.

## Schema (534–536)

No FKs or cascades. Concurrent index is its own migration.

- `popo_registration`
- `popo_bridge_command.installation_id` nullable (QR commands have no install yet)

## Tests

- begin requires an online bridge
- progress `qr_url` then `robot_id` creates an installation
- secret fields in progress are ignored
- duplicate robot conflict
- cancel ignores later progress
- UI: scan button, poll success, idle bind still works
- malformed schema fallbacks for the new endpoints

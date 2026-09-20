# P1c implementation summary

Web/Desktop pairing and bind-existing-robot UI on the P1a Windows-bridge APIs (plan §8 and §9 P1c). QR scan, media, group chat, `/popo/bind` changes, and a mobile connection-management page are not in this change.

## Files changed

### Core API contract

- `packages/core/types/popo.ts` — `PopoBridge`, `PopoBridgeRobot`, `PopoBridgePairing`, `RegisterPopoRequest` is `{bridge_id, robot_id, robot_name?}`; `webhook_url` stays on `PopoInstallation` with default `""`; `isIdlePopoRobot`
- `packages/core/api/schemas.ts` — zod + `parseWithFallback` empties for bridges and pairing; unknown `status` / `occupied_by` stay strings
- `packages/core/api/client.ts` — `listPopoBridges`, `createPopoBridgePairing`, `revokePopoBridge`; install body no longer sends `webhook_url`
- `packages/core/popo/queries.ts` — workspace-scoped `popoKeys.bridges(wsId)`; 15s refetch so online/offline tracks the 45s heartbeat window

### Web/Desktop UI

- `packages/views/settings/components/popo-tab.tsx` — Settings → Integrations → POPO: enablement copy, pairing dialog (code once + Windows command + copy), bridge list, revoke
- `packages/views/agents/components/tabs/integrations-tab.tsx` — Connect POPO for workspace owner/admin **or** the agent owner (`canManageAgent`); bind dialog selects an online host then an idle robot
- `packages/views/locales/{en,zh-Hans,ja,ko,fr}/settings.json` — `popo` keys in sync

### Tests

- `packages/core/api/schemas.test.ts`, `packages/core/api/schema.test.ts` — malformed list/pairing/install responses
- `packages/core/types/popo.test.ts` — idle occupancy
- `packages/views/settings/components/popo-tab.test.tsx` — not-enabled copy, pairing shown once, idle robots only, agent owner CTA
- `packages/views/agents/components/tabs/integrations-tab.test.tsx` — non-admin agent owner sees POPO bind

## Decisions

- Enablement copy names `MULTICA_POPO_ENABLED=true`, not `MULTICA_POPO_SECRET_KEY`. List `configured: false` or 503/`popo_not_configured` both show that state.
- Pairing plaintext lives only in dialog state and is cleared on close. It is not written to Query cache or Zustand.
- The pair command is exactly `python -m nanobot.multica_bridge pair --server <origin> --pairing-code <code>`. `<origin>` is `api.getBaseUrl()` (desktop) or `window.location.origin`.
- Idle = `connected` and empty `occupied_by`. Occupied robots (`dj01bot` / `sparse` / `multica` / unknown) are listed on the host but cannot be selected in the bind dialog.
- Install/disconnect permission mirrors Lark: agent owner or workspace owner/admin. Pairing and host revoke stay workspace owner/admin.
- Existing `popo_installation:*` realtime invalidation is enough for installs. Bridges refresh on a 15s query interval; no new WS event.

## Deviations

- None versus plan §8 / §9 P1c. Go/SQL were not changed.

## What was not implemented

QR scan (P3), media, group chat, `/popo/bind` changes, mobile connection-management page.

## Verification

Ran from the worktree:

```
pnpm --filter @multica/core exec node ./node_modules/vitest/vitest.mjs run api/schemas.test.ts api/schema.test.ts types/popo.test.ts
pnpm --filter @multica/views exec node ./node_modules/vitest/vitest.mjs run settings/components/popo-tab.test.tsx agents/components/tabs/integrations-tab.test.tsx
```

278 core tests passed (3 files). 37 views tests passed (2 files). `pnpm typecheck`, `pnpm lint`, `make test`, and Playwright were not run.
